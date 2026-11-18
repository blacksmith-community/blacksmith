package services_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"blacksmith/internal/services"
)

// The differential check runs only through `make schema-parity`, which
// points these at scripts/schema-parity/oracle.rb and the gems Cloud
// Controller pins.
const (
	parityOracleEnv = "SCHEMA_PARITY_ORACLE"
	parityCountEnv  = "SCHEMA_PARITY_COUNT"
	paritySeedEnv   = "SCHEMA_PARITY_SEED"

	parityDefaultCount = 20000
	parityDefaultSeed  = 1
	parityShownFaults  = 20
)

// TestSchemaParity hands Cloud Controller's json-schema gem every schema
// the profile publishes in these tests, and a run of random schemas, and
// fails when the gem refuses any schema the profile publishes. It runs
// the gem once with a fresh cache for each schema, and once with one
// cache for the whole run, seeded with schemas whose ids try to take
// over keys.
//
//nolint:paralleltest // it runs one oracle process at a time
func TestSchemaParity(t *testing.T) {
	oracle := os.Getenv(parityOracleEnv)
	if oracle == "" {
		t.Skip("run make schema-parity to check the schema profile against Cloud Controller's json-schema gem")
	}

	corpus := parityCorpus(t)
	random := randomSchemas(t, parityEnvInt(t, paritySeedEnv, parityDefaultSeed), parityEnvInt(t, parityCountEnv, parityDefaultCount))
	lines := append(append([]string{}, corpus...), random...)

	published := make([]bool, len(lines))
	for index, line := range lines {
		published[index] = services.ValidateParameterSchema(decodeSchema(t, line)) == nil
	}

	for _, mode := range []string{"fresh", "shared"} {
		verdicts := runOracle(t, oracle, mode, lines)

		faults, gemAccepts, strict := 0, 0, 0

		for index, verdict := range verdicts {
			accepted := verdict == "accept"
			if accepted {
				gemAccepts++
			}

			switch {
			case published[index] && !accepted:
				faults++
				if faults <= parityShownFaults {
					t.Errorf("%s cache: the profile publishes a schema the gem refuses (%s): %s", mode, verdict, shorten(lines[index]))
				}
			case !published[index] && accepted:
				strict++
			}
		}

		t.Logf("%s cache: %d schemas (%d from the tests, %d random), the profile publishes %d, the gem accepts %d, "+
			"the gem refuses %d the profile publishes, and the profile refuses %d the gem accepts",
			mode, len(lines), len(corpus), len(random), countTrue(published), gemAccepts, faults, strict)
	}

	if randomPublished := countTrue(published[len(corpus):]); randomPublished < len(random)/10 {
		t.Errorf("the profile publishes only %d of %d random schemas, too few to check it", randomPublished, len(random))
	}
}

// parityCorpus is the JSON the broker serves for every schema these
// tests expect the profile to publish.
func parityCorpus(t *testing.T) []string {
	t.Helper()

	texts := []string{publishedSchemaJSON}

	for _, check := range append(ccSpecCases(), profileCases()...) {
		if check.want == nil {
			texts = append(texts, check.schema)
		}
	}

	for _, plan := range schemaPlans() {
		if plan.published {
			texts = append(texts, plan.schema)
		}
	}

	corpus := make([]string, 0, len(texts))

	for _, text := range texts {
		schema := decodeSchema(t, text)

		err := services.ValidateParameterSchema(schema)
		if err != nil {
			t.Fatalf("the profile refuses a schema the tests expect it to publish: %v", err)
		}

		corpus = append(corpus, servedJSON(t, schema))
	}

	return corpus
}

// servedJSON is schema's JSON as the catalog serves it.
func servedJSON(t *testing.T, schema map[string]interface{}) string {
	t.Helper()

	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("encode a schema: %v", err)
	}

	return string(data)
}

// runOracle runs the oracle command in mode over lines and returns its
// verdict on each.
func runOracle(t *testing.T, oracle, mode string, lines []string) []string {
	t.Helper()

	command := exec.CommandContext(t.Context(), "sh", "-c", oracle+" "+mode) //nolint:gosec // the command is the developer's own setting
	command.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")

	var stderr bytes.Buffer

	command.Stderr = &stderr

	output, err := command.Output()
	if err != nil {
		t.Fatalf("the oracle failed in %s mode: %v\n%s", mode, err, stderr.String())
	}

	var verdicts []string

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		verdicts = append(verdicts, scanner.Text())
	}

	if len(verdicts) != len(lines) {
		t.Fatalf("the oracle gave %d verdicts in %s mode for %d schemas\n%s", len(verdicts), mode, len(lines), stderr.String())
	}

	return verdicts
}

func parityEnvInt(t *testing.T, name string, fallback int) int {
	t.Helper()

	text := os.Getenv(name)
	if text == "" {
		return fallback
	}

	value, err := strconv.Atoi(text)
	if err != nil || value < 0 {
		t.Fatalf("%s is not a whole number", name)
	}

	return value
}

func countTrue(values []bool) int {
	count := 0

	for _, value := range values {
		if value {
			count++
		}
	}

	return count
}

func shorten(text string) string {
	const limit = 400

	if len(text) > limit {
		return text[:limit] + "..."
	}

	return text
}

// randomSchemas is count random schemas, as served JSON, drawn mostly
// from the profile's keywords and partly from the rest of draft-04 and
// the gem.
func randomSchemas(t *testing.T, seed, count int) []string {
	t.Helper()

	generator := &schemaGenerator{rng: rand.New(rand.NewPCG(uint64(seed), 0))} //nolint:gosec // a seeded generator, so a run repeats

	schemas := make([]string, 0, count)

	for range count {
		schemas = append(schemas, servedJSON(t, generator.root()))
	}

	return schemas
}

// schemaGenerator draws random schemas.
type schemaGenerator struct {
	rng      *rand.Rand
	keywords []string
}

// The keywords the generator gives a structure of its own.
const (
	generatorMaxDepth = 3

	genType       = "type"
	genRef        = "$ref"
	genID         = "id"
	genDependency = "dependencies"
	genPatterns   = "patternProperties"
	genURN        = "urn:x"
	genDraft4     = "http://json-schema.org/draft-04/schema#"
)

// generatorKeywords are the keywords a schema object draws from, with
// the profile's listed three times so that most draws stay inside it.
func generatorKeywords() []string {
	profile := strings.Fields(`type properties required additionalProperties items enum default title description
		format pattern minimum maximum exclusiveMinimum exclusiveMaximum minLength maxLength minItems maxItems
		uniqueItems $ref`)
	others := strings.Fields(`id $schema definitions patternProperties allOf anyOf oneOf not dependencies
		additionalItems multipleOf extends disallow x-ui`)

	return append(append(append(append([]string{}, profile...), profile...), profile...), others...)
}

//nolint:gochecknoglobals // a fixed table, only read
var generatorRefs = []string{
	"#/definitions/a", "#/definitions/a", "#/definitions/b", "#/definitions/c", "#/definitions/nope", "#",
	"#/properties/a", "#/definitions/a~1b", "http://example.com/x.json", genURN, "a.json", genDraft4,
}

//nolint:gochecknoglobals // a fixed table, only read
var generatorNames = []string{"a", "b", "c", genID, genRef, "a/b", "é", "<x>", ""}

//nolint:gochecknoglobals // a fixed table, only read
var generatorStrings = []string{
	"", "x", "^[a-z]+$", "([", "(?<=a+)b", "^(?=.*\\d).{8,}$", "\\", "(?i)a", "\\p{L}", "a{2,1}", "email", "uri",
	"date-time", "<&>", " ",
}

func (g *schemaGenerator) pick(options []string) string {
	return options[g.rng.IntN(len(options))]
}

func (g *schemaGenerator) chance(oneIn int) bool {
	return g.rng.IntN(oneIn) == 0
}

func (g *schemaGenerator) root() map[string]any {
	schema := g.object(0)
	if g.chance(2) {
		schema["definitions"] = g.objectMap([]string{"a", "b", "c", "a/b"}, 1)
	}

	// A root id and a root $ref are what a cached id can break, so the
	// root draws them more often than other members do.
	if g.chance(10) {
		schema[genID] = g.value(genID, 0)
	}

	if g.chance(4) {
		schema[genRef] = g.pick(generatorRefs)
	}

	switch g.rng.IntN(24) {
	case 0:
		schema["$schema"] = "http://json-schema.org/draft-06/schema#"
	case 1:
		schema["$schema"] = "http://json-schema.org/draft-04/schema"
	case 2:
		schema["$schema"] = "http://json-schema.org/draft-04/schema#foo"
	default:
		schema["$schema"] = genDraft4
	}

	return schema
}

func (g *schemaGenerator) object(depth int) map[string]any {
	schema := map[string]any{}

	if g.keywords == nil {
		g.keywords = generatorKeywords()
	}

	for range g.rng.IntN(4) {
		key := g.pick(g.keywords)
		if depth >= generatorMaxDepth && g.holdsSchema(key) {
			continue
		}

		schema[key] = g.value(key, depth)
	}

	return schema
}

func (g *schemaGenerator) holdsSchema(key string) bool {
	switch key {
	case "properties", "additionalProperties", "items", "definitions", genPatterns, "allOf", "anyOf", "oneOf",
		"not", genDependency:
		return true
	default:
		return false
	}
}

func (g *schemaGenerator) objectMap(names []string, depth int) map[string]any {
	members := map[string]any{}

	for range g.rng.IntN(3) + 1 {
		members[g.pick(names)] = g.object(depth)
	}

	return members
}

//nolint:cyclop // one case per keyword
func (g *schemaGenerator) value(key string, depth int) any {
	switch key {
	case genType:
		return g.typeValue()
	case "properties", "definitions", genPatterns:
		if g.chance(30) {
			return true
		}

		names := generatorNames
		if key == genPatterns {
			names = generatorStrings
		}

		return g.objectMap(names, depth+1)
	case "additionalProperties", "items", "not":
		return g.schemaOr(depth, []any{true, false, []any{map[string]any{}}})
	case "allOf", "anyOf", "oneOf":
		return []any{g.object(depth + 1)}
	case genDependency:
		return map[string]any{"a": []any{"b"}}
	case "required":
		return g.list(func() any { return g.pick(generatorNames) })
	case "enum":
		return g.list(g.data)
	case "default":
		return g.data()
	case genRef, "extends":
		return g.pick(generatorRefs)
	case genID:
		return g.pick([]string{genURN, "http://example.com/x", "#", "a.json"})
	case "$schema":
		return genDraft4
	}

	return g.plainValue(key)
}

func (g *schemaGenerator) plainValue(key string) any {
	switch key {
	case "minimum", "maximum", "multipleOf":
		return g.number()
	case "exclusiveMinimum", "exclusiveMaximum", "uniqueItems", "additionalItems":
		if g.chance(20) {
			return 1
		}

		return g.chance(2)
	case "minLength", "maxLength", "minItems", "maxItems":
		return g.pickAny([]any{0, 1, 5, 64, -1, 1.5, 1e300})
	default:
		if g.chance(25) {
			return 5
		}

		return g.pick(generatorStrings)
	}
}

func (g *schemaGenerator) schemaOr(depth int, others []any) any {
	if g.chance(3) {
		return g.pickAny(others)
	}

	return g.object(depth + 1)
}

func (g *schemaGenerator) typeValue() any {
	types := []string{"array", "boolean", "integer", "null", "number", "object", "string"}

	switch g.rng.IntN(10) {
	case 0:
		return []any{g.pick(types), g.pick(types)}
	case 1:
		return g.pick([]string{"any", "Object"})
	default:
		return g.pick(types)
	}
}

func (g *schemaGenerator) list(member func() any) []any {
	size := g.rng.IntN(4)
	values := make([]any, 0, size)

	for range size {
		values = append(values, member())
	}

	return values
}

func (g *schemaGenerator) number() any {
	return g.pickAny([]any{0, 1, -3, 2.5, 0.1, 1e300, -1e-7, "1"})
}

func (g *schemaGenerator) data() any {
	return g.pickAny([]any{
		nil, true, "x", 1, 1.0, 2.5, []any{}, map[string]any{},
		map[string]any{genRef: "http://example.com/x.json"}, map[string]any{genID: genURN},
		[]any{1, "a"},
	})
}

func (g *schemaGenerator) pickAny(options []any) any {
	return options[g.rng.IntN(len(options))]
}
