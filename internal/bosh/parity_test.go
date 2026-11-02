package bosh_test

import (
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/parity"
)

// parityDirector serves the documents of docs as configs 1, 2, and so on,
// both by ID and as the latest of type "fixture" and the name of the
// document.
func parityDirector(t *testing.T, docs []parity.Document) bosh.Director {
	t.Helper()

	byID := map[string]parity.Document{}
	byName := map[string]parity.Document{}

	for index, doc := range docs {
		byID[strconv.Itoa(index+1)] = doc
		byName[doc.Name] = doc
	}

	entry := func(id string, doc parity.Document) map[string]interface{} {
		return map[string]interface{}{
			"id":         id,
			"type":       "fixture",
			"name":       doc.Name,
			"content":    doc.Body,
			"created_at": "2026-10-01 12:00:00 UTC",
		}
	}

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		switch {
		case request.URL.Path == "/configs":
			doc, ok := byName[request.URL.Query().Get("name")]
			if !ok {
				boshtest.WriteJSON(writer, http.StatusOK, []interface{}{})

				return true
			}

			boshtest.WriteJSON(writer, http.StatusOK, []interface{}{entry("1", doc)})

			return true
		case strings.HasPrefix(request.URL.Path, "/configs/"):
			id := strings.TrimPrefix(request.URL.Path, "/configs/")

			doc, ok := byID[id]
			if !ok {
				boshtest.WriteError(writer, http.StatusNotFound, boshtest.CodeDirectorNotFound, "no such config")

				return true
			}

			boshtest.WriteJSON(writer, http.StatusOK, entry(id, doc))

			return true
		}

		return false
	})

	return newReadAdapter(t, server, nil)
}

// TestGetConfigParity records the map GetConfig makes of every document a
// director holds as a config.
func TestGetConfigParity(t *testing.T) {
	docs := parity.OddDocuments()
	director := parityDirector(t, docs)

	var recorded strings.Builder

	for _, doc := range docs {
		got, err := director.GetConfig(t.Context(), "fixture", doc.Name)
		if err != nil {
			recorded.WriteString(parity.Section(doc.Name, "ERROR: "+scrubURL(err.Error())+"\n"))

			continue
		}

		recorded.WriteString(parity.Section(doc.Name, parity.Dump(got)))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "get-config.dump"), recorded.String())
}

// TestComputeConfigDiffParity records the diff between each pair of a set
// of documents, including each against itself.
func TestComputeConfigDiffParity(t *testing.T) {
	var docs []parity.Document

	keep := map[string]bool{
		"yaml11-scalars": true, "anchors-and-merge": true, "duplicate-keys": true, "non-string-keys": true,
		"blacksmith-metadata": true, "multiline": true, "list-root": true, "null": true, "empty": true,
		"unterminated": true, "scalar-root": true,
	}

	for _, doc := range parity.OddDocuments() {
		if keep[doc.Name] {
			docs = append(docs, doc)
		}
	}

	director := parityDirector(t, docs)

	var recorded strings.Builder

	for from, fromDoc := range docs {
		for to, toDoc := range docs {
			diff, err := director.ComputeConfigDiff(t.Context(), strconv.Itoa(from+1), strconv.Itoa(to+1))

			name := fromDoc.Name + " to " + toDoc.Name
			if err != nil {
				recorded.WriteString(parity.Section(name, "ERROR: "+scrubURL(err.Error())+"\n"))

				continue
			}

			recorded.WriteString(parity.Section(name, parity.Dump(diff)))
		}
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "config-diff.dump"), recorded.String())
}

// TestCloudConfigPlanParity records how the boot cloud config planner reads
// cloud configs: whether one is empty, and what the planner does with the
// director's configs next to the broker's own.
func TestCloudConfigPlanParity(t *testing.T) {
	t.Parallel()

	var empty strings.Builder

	for _, doc := range parity.OddDocuments() {
		isEmpty, err := bosh.IsEmptyCloudConfig(doc.Body)
		empty.WriteString(parity.Section(doc.Name, parity.Dump(struct {
			Empty bool
			Err   string
		}{isEmpty, errString(err)})))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "cloud-config-empty.dump"), empty.String())

	const mine = "azs:\n- name: z1\nnetworks:\n- name: services\n  subnets: []\nvm_types:\n- name: default\n"

	others := map[string]string{
		"same":            mine,
		"overlap-az":      "azs:\n- name: z1\n- name: z9\n",
		"overlap-network": "networks:\n- {name: services}\n",
		"disjoint":        "azs:\n- name: z7\nvm_types:\n- name: big\n",
		"empty":           "",
		"null":            "~\n",
		"list":            "- a\n- b\n",
		"unterminated":    "azs: [\n",
		"scalars":         "azs:\n- name: 1e3\n- name: yes\n",
		"anchors":         "azs:\n- &z {name: z5}\n- *z\n",
		"duplicate-keys":  "azs:\n- name: z1\nazs:\n- name: z2\n",
		"non-string-keys": "azs:\n- name: z1\n  1: one\n",
		"numeric-name":    "azs:\n- name: 7\n",
	}

	names := make([]string, 0, len(others))
	for name := range others {
		names = append(names, name)
	}

	sort.Strings(names)

	var plans strings.Builder

	for _, name := range names {
		for _, brokerSide := range []struct{ name, content string }{{"mine", mine}, {"broker-" + name, others[name]}} {
			plan := bosh.PlanCloudConfigWrite([]bosh.CloudConfigEntry{
				{Name: "default", Content: others[name]},
				{Name: bosh.BoshCloudConfigName, Content: "azs: [stale]\n"},
			}, brokerSide.content)
			plans.WriteString(parity.Section("director "+name+" / "+brokerSide.name, parity.Dump(plan)))
		}
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "cloud-config-plan.dump"), plans.String())
}

func errString(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

// scrubURL removes the test director's address from an error.
func scrubURL(text string) string {
	for {
		start := strings.Index(text, "https://127.0.0.1:")
		if start < 0 {
			return text
		}

		end := start + len("https://127.0.0.1:")
		for end < len(text) && text[end] >= '0' && text[end] <= '9' {
			end++
		}

		text = text[:start] + "<director>" + text[end:]
	}
}
