package deployments

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/parity"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// updateDirector keeps the manifest text an update sends to the director.
type updateDirector struct {
	bosh.Director

	sent string
}

func (d *updateDirector) UpdateDeployment(_ context.Context, _, manifest string) (*bosh.Task, error) {
	d.sent = manifest

	return &bosh.Task{ID: 7}, nil
}

// updateVault holds the service index and keeps what the handler stores.
type updateVault struct {
	testutil.InstanceVault

	stored map[string]interface{}
}

func (v *updateVault) Get(_ context.Context, path string, out interface{}) (bool, error) {
	var value interface{}

	switch path {
	case "db":
		value = map[string]interface{}{"inst-1": map[string]interface{}{}}
	case "inst-1":
		value = map[string]interface{}{"deployment_name": "parity"}
	default:
		return false, nil
	}

	body, err := json.Marshal(value)
	if err != nil {
		return false, err //nolint:wrapcheck // a test double
	}

	return true, json.Unmarshal(body, out) //nolint:wrapcheck // a test double
}

func (v *updateVault) Put(_ context.Context, path string, data interface{}) error {
	if path == "inst-1/manifest" {
		v.stored, _ = data.(map[string]interface{})
	}

	return nil
}

// updateBodies are JSON manifests, written as the UI sends them. They hold
// the values whose YAML form is a choice of the library: strings that read as
// other types, numbers of every size, and text that needs quoting.
var updateBodies = []parity.Document{
	{Name: "plain", Body: `{"name":"parity","releases":[{"name":"r","version":"1.2.3"}],"instance_groups":[{"name":"g","instances":3,"azs":["z1","z2"]}]}`},
	{Name: "yaml11-looking-strings", Body: `{"name":"parity","properties":{"a":"yes","b":"no","c":"on","d":"off","e":"y","f":"n","g":"true","h":"null","i":"~","j":"1e3","k":"0755","l":"0x1f","m":"2026-01-02","n":"12:30:45","o":".inf","p":"1_000","q":"08"}}`},
	{Name: "numbers", Body: `{"name":"parity","properties":{"int":3,"neg":-7,"float":1.5,"whole-float":3.0,"exp":1e3,"small":0.000001,"big":12345678901234567890,"safe-max":9007199254740993,"zero":0,"neg-zero":-0.0,"version":1.10}}`},
	{Name: "bools-and-null", Body: `{"name":"parity","properties":{"t":true,"f":false,"nothing":null,"empty-string":"","empty-map":{},"empty-list":[]}}`},
	{Name: "needs-quoting", Body: `{"name":"parity","properties":{"colon":"a: b","hash":"a #b","dash":"- x","lead-colon":":x","star":"*x","amp":"&x","bang":"!x","pct":"%x","at":"@x","backtick":"` + "`x" + `","brace":"{x}","bracket":"[x]","quote":"say \"hi\"","apos":"it's","lead-space":" x","trail-space":"x ","tab":"a\tb","unicode":"café ☃","emoji":"😀"}}`},
	{Name: "multiline", Body: `{"name":"parity","properties":{"two":"line1\nline2","trailing":"line1\n","blank":"a\n\nb","cr":"a\r\nb","cert":"-----BEGIN CERTIFICATE-----\nFIXTURE-PLACEHOLDER\n-----END CERTIFICATE-----\n"}}`},
	{Name: "key-names", Body: `{"name":"parity","properties":{"":"empty-key","yes":"k","1":"numeric","a b":"space","a:b":"colon","-":"dash","~":"tilde","null":"n","true":"t","long-key-` + strings.Repeat("x", 120) + `":"long"}}`},
	{Name: "key-order", Body: `{"zebra":1,"alpha":2,"name":"parity","mid":{"z":1,"a":2,"m":3},"Alpha":4,"_under":5,"10":6,"9":7}`},
	{Name: "long-string", Body: `{"name":"parity","properties":{"long":"` + strings.Repeat("word ", 60) + `","nospace":"` + strings.Repeat("x", 300) + `"}}`},
	{Name: "nested", Body: `{"name":"parity","instance_groups":[{"name":"g","jobs":[{"name":"j","properties":{"deep":{"deeper":{"deepest":[1,[2,[3]],{"k":"v"}]}}}}]}]}`},
	{Name: "duplicate-json-keys", Body: `{"name":"first","name":"second"}`},
	{Name: "empty-object", Body: `{}`},
	{Name: "null", Body: `null`},
	{Name: "list", Body: `[1,2]`},
	{Name: "scalar", Body: `"just a string"`},
	{Name: "not-json", Body: "name: parity\n"},
	{Name: "truncated", Body: `{"name":`},
}

// TestUpdateManifestParity records the YAML text an update sends to the
// director and stores in the vault for each JSON manifest, and the answer the
// endpoint gives. The spruce-era recording is in update-manifest.txt and
// update-manifest/, and the graft recording is in update-manifest-graft/. The text is a YAML marshal of a map, so key order, quoting,
// and number forms are all the library's.
func TestUpdateManifestParity(t *testing.T) {
	t.Parallel()

	var recorded strings.Builder

	for _, doc := range updateBodies {
		director := &updateDirector{}
		vault := &updateVault{}
		handler := NewHandler(Dependencies{Logger: logger.Get().Named("test"), Director: director, Vault: vault})

		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/b/deployments/parity/manifest", strings.NewReader(doc.Body))
		handler.UpdateManifest(recorder, request, "parity")

		body := "status " + http.StatusText(recorder.Code) + "\nanswer " + strings.TrimSpace(recorder.Body.String()) + "\n--- sent to director\n" + director.sent
		if stored, ok := vault.stored["manifest"].(string); ok {
			body += "--- stored in vault, same as sent: " + boolWord(stored == director.sent) + "\n"
		}

		recorded.WriteString(parity.Section(doc.Name, body))

		if director.sent != "" {
			parity.GoldenYAML(t, filepath.Join("testdata", "parity", "update-manifest-graft", doc.Name+".yml"), director.sent)
		}
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "update-manifest-graft", "update-manifest.txt"), recorded.String())
}

func boolWord(value bool) string {
	if value {
		return "yes"
	}

	return "no"
}
