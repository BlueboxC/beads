package bdpwire

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const readProjectionSHA256 = "2db57a82d9bf99f3fb5d18646e8bd6c7646590d1e806aa266de8af415ee83545"

// restrictedCanonical is a test-only serializer for this exact schema corpus.
// ASCII avoids UTF-16 ordering/escaping ambiguities; integral int64 numbers
// are its only numeric domain. This is deliberately not a general JCS API.
func restrictedCanonical(v any) ([]byte, error) {
	var b bytes.Buffer
	var emit func(any) error
	quote := func(s string) error {
		for _, c := range s {
			if c > 127 {
				return fmt.Errorf("non-ASCII canonical input")
			}
		}
		var q bytes.Buffer
		e := json.NewEncoder(&q)
		e.SetEscapeHTML(false)
		if err := e.Encode(s); err != nil {
			return err
		}
		b.Write(bytes.TrimSuffix(q.Bytes(), []byte("\n")))
		return nil
	}
	emit = func(v any) error {
		switch x := v.(type) {
		case nil:
			b.WriteString("null")
		case bool:
			if x {
				b.WriteString("true")
			} else {
				b.WriteString("false")
			}
		case string:
			return quote(x)
		case json.Number:
			n, err := strconv.ParseInt(string(x), 10, 64)
			if err != nil {
				return fmt.Errorf("unsupported canonical number %s", x)
			}
			b.WriteString(strconv.FormatInt(n, 10))
		case []any:
			b.WriteByte('[')
			for i, y := range x {
				if i > 0 {
					b.WriteByte(',')
				}
				if err := emit(y); err != nil {
					return err
				}
			}
			b.WriteByte(']')
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			b.WriteByte('{')
			for i, k := range keys {
				if i > 0 {
					b.WriteByte(',')
				}
				if err := quote(k); err != nil {
					return err
				}
				b.WriteByte(':')
				if err := emit(x[k]); err != nil {
					return err
				}
			}
			b.WriteByte('}')
		default:
			return fmt.Errorf("unsupported canonical input %T", v)
		}
		return nil
	}
	if err := emit(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func quotedDeclaration(source, name, open, close string) ([]string, error) {
	// Only the two pinned declaration forms are supported. Matching the whole
	// identifier and assignment prevents a renamed export or unrelated later
	// Object.freeze call from silently supplying this declaration's values.
	declaration := regexp.MustCompile(`(?m)^export const ` + regexp.QuoteMeta(name) + `(?:[^A-Za-z0-9_$]|$)`)
	matches := declaration.FindAllStringIndex(source, -1)
	if len(matches) != 1 {
		return nil, fmt.Errorf("missing/duplicate declaration %s", name)
	}
	tail := source[matches[0][0]+len("export const ")+len(name):]
	assignment := " = "
	if open == "Object.freeze([" {
		assignment = ": readonly string[] = "
	}
	if !strings.HasPrefix(tail, assignment+open) {
		return nil, fmt.Errorf("unsupported declaration assignment %s", name)
	}
	tail = tail[len(assignment)+len(open):]
	j := strings.Index(tail, close)
	if j < 0 {
		return nil, fmt.Errorf("missing closer")
	}
	body := tail[:j]
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pattern := `^"([A-Za-z][A-Za-z0-9]*)",$`
		if open == "Object.freeze({" {
			pattern = `^[A-Za-z][A-Za-z0-9]*: "(#/\$defs/[A-Za-z][A-Za-z0-9]*)",$`
		}
		m := regexp.MustCompile(pattern).FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("unsupported declaration syntax %q", line)
		}
		out = append(out, m[1])
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty declaration")
	}
	seen := map[string]bool{}
	for _, x := range out {
		if seen[x] {
			return nil, fmt.Errorf("duplicate declaration value %s", x)
		}
		seen[x] = true
	}
	return out, nil
}
func projectionNames(t *testing.T) []string {
	t.Helper()
	v, err := quotedDeclaration(string(readSchemaFile(t, "upstream/schema-read-projection.ts")), "READ_SCHEMA_SEALED_DEFINITIONS", "Object.freeze([", "]);")
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func projectionRoots(t *testing.T) []string {
	t.Helper()
	set := map[string]bool{}
	for _, input := range []struct{ file, name string }{{"read-values.ts", "READ_VALUE_SCHEMA_REFS"}, {"history-values.ts", "HISTORY_VALUE_SCHEMA_REFS"}} {
		v, err := quotedDeclaration(string(readSchemaFile(t, "upstream/"+input.file)), input.name, "Object.freeze({", "} as const)")
		if err != nil {
			t.Fatal(err)
		}
		for _, ref := range v {
			set[ref] = true
		}
	}
	manifest := asMap(t, loadJSON(t, "conformance/read-v1.matrix.json"), "matrix")
	for _, step := range matrixSteps(t, manifest) {
		for _, a := range asSlice(t, step.step["assertions"], "assertions") {
			m := asMap(t, a, "assertion")
			if m["kind"] == "json-schema" {
				set[asString(t, m["schema"], "schema")] = true
			}
		}
	}
	names := []string{}
	for ref := range set {
		n, err := localDefinition(ref)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
func localDefinition(ref string) (string, error) {
	m := regexp.MustCompile(`^#/\$defs/([^/~]+)$`).FindStringSubmatch(ref)
	if m == nil {
		return "", fmt.Errorf("unsupported schema ref %q", ref)
	}
	return m[1], nil
}
func projectionClosure(defs map[string]any, roots, sealed []string) ([]string, error) {
	if len(roots) == 0 || len(sealed) == 0 {
		return nil, fmt.Errorf("empty roots/seal")
	}
	allowed := map[string]bool{}
	for _, n := range sealed {
		if allowed[n] {
			return nil, fmt.Errorf("duplicate seal")
		}
		if _, ok := defs[n]; !ok {
			return nil, fmt.Errorf("missing sealed definition")
		}
		allowed[n] = true
	}
	seen := map[string]bool{}
	pending := append([]string{}, roots...)
	var refs func(any) error
	refs = func(v any) error {
		switch x := v.(type) {
		case []any:
			for _, y := range x {
				if err := refs(y); err != nil {
					return err
				}
			}
		case map[string]any:
			for k, y := range x {
				switch k {
				case "$ref":
					s, ok := y.(string)
					if !ok {
						return fmt.Errorf("nonstring ref")
					}
					n, err := localDefinition(s)
					if err != nil {
						return err
					}
					pending = append(pending, n)
				case "$dynamicRef", "$recursiveRef":
					return fmt.Errorf("unsupported reference form")
				case "const", "default", "enum", "examples":
				case "$defs", "definitions", "dependentSchemas", "patternProperties", "properties":
					if m, ok := y.(map[string]any); ok {
						for _, z := range m {
							if err := refs(z); err != nil {
								return err
							}
						}
					}
				default:
					if err := refs(y); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	for len(pending) > 0 {
		n := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[n] {
			continue
		}
		v, ok := defs[n]
		if !ok {
			return nil, fmt.Errorf("dangling reference %s", n)
		}
		if !allowed[n] {
			return nil, fmt.Errorf("unsealed reachable definition %s", n)
		}
		seen[n] = true
		if err := refs(v); err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}
func selectedBundleDefs(t *testing.T) map[string]any {
	t.Helper()
	all := loadBundleDefs(t)
	out := map[string]any{}
	for _, n := range projectionNames(t) {
		v, ok := all[n]
		if !ok {
			t.Fatalf("missing %s", n)
		}
		out[n] = v
	}
	return out
}
func deriveProjectionArtifacts(t *testing.T) ([]byte, []byte) {
	t.Helper()
	defs := loadBundleDefs(t)
	names := projectionNames(t)
	roots := projectionRoots(t)
	reached, err := projectionClosure(defs, roots, names)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 47 || len(defs) != 158 || len(roots) != 20 || len(reached) != 46 {
		t.Fatalf("projection census changed: %d/%d/%d/%d", len(names), len(defs), len(roots), len(reached))
	}
	pairs := []any{}
	selected := map[string]bool{}
	for _, n := range names {
		pairs = append(pairs, []any{n, defs[n]})
		selected[n] = true
	}
	excluded := []any{}
	for _, n := range sortedKeys(defs) {
		if !selected[n] {
			excluded = append(excluded, n)
		}
	}
	projection, err := restrictedCanonical(pairs)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(projection)
	if hex.EncodeToString(h[:]) != readProjectionSHA256 {
		t.Fatal("Read projection digest changed")
	}
	whole := sha256.Sum256(SchemaBundle())
	ns := []any{}
	for _, n := range names {
		ns = append(ns, n)
	}
	rs := []any{}
	for _, n := range roots {
		rs = append(rs, n)
	}
	manifest, err := restrictedCanonical(map[string]any{"pin": Pin, "schemaSHA256": hex.EncodeToString(whole[:]), "projectionSHA256": readProjectionSHA256, "definitions": ns, "roots": rs, "excluded": excluded})
	if err != nil {
		t.Fatal(err)
	}
	return projection, manifest
}
func checkRecipeDependencies(e pinEntry, entries []pinEntry) error {
	if e.upstream != "schemas/bdp-v0.schema.json+upstream-read-sources+read-v1.matrix" {
		return fmt.Errorf("unknown recipe dependency descriptor")
	}
	listed := map[string]bool{}
	for _, x := range entries {
		listed[x.local] = lowerHex40.MatchString(x.blob)
	}
	for _, p := range []string{"bdp-v0.schema.json", "upstream/schema-read-projection.ts", "upstream/read-values.ts", "upstream/history-values.ts", "conformance/read-v1.matrix.json"} {
		if !listed[p] {
			return fmt.Errorf("recipe dependency not verbatim-pinned: %s", p)
		}
	}
	return nil
}
func reproduceRecipe(t *testing.T, e pinEntry) []byte {
	t.Helper()
	if err := checkRecipeDependencies(e, loadPin(t).entries); err != nil {
		t.Fatal(err)
	}
	p, m := deriveProjectionArtifacts(t)
	switch e.blob {
	case "recipe:read-projection":
		if e.local != "read-projection.jcs.json" {
			t.Fatal("recipe path mismatch")
		}
		return p
	case "recipe:read-manifest":
		if e.local != "read-projection.manifest.json" {
			t.Fatal("recipe path mismatch")
		}
		return m
	default:
		t.Fatalf("unknown recipe %s", e.blob)
	}
	return nil
}
func TestReadProjectionAndPartition(t *testing.T) {
	p, m := deriveProjectionArtifacts(t)
	if !bytes.Equal(p, readSchemaFile(t, "read-projection.jcs.json")) || !bytes.Equal(m, readSchemaFile(t, "read-projection.manifest.json")) {
		t.Fatal("derived projection/manifest mismatch")
	}
	if !reflect.DeepEqual(sortedKeys(selectedBundleDefs(t)), sortedKeys(defsToGo)) {
		t.Fatal("Go bindings do not match selected definitions")
	}
}
func TestRestrictedCanonicalRefusesUnsupportedInput(t *testing.T) {
	for _, v := range []any{"é", map[string]any{"é": true}, json.Number("1.1"), json.Number("9223372036854775808"), 1.0} {
		if _, err := restrictedCanonical(v); err == nil {
			t.Errorf("accepted %v", v)
		}
	}
	b, err := restrictedCanonical(map[string]any{"<": "\n\"&"})
	if err != nil || string(b) != `{"<":"\n\"&"}` {
		t.Fatalf("escaping %s: %v", b, err)
	}
}
func TestProjectionClosureFailsClosed(t *testing.T) {
	for _, v := range []any{map[string]any{"$ref": "https://example.test/x"}, map[string]any{"$ref": "#/$defs/missing"}, map[string]any{"$dynamicRef": "#x"}, map[string]any{"$ref": true}, map[string]any{"$ref": "#/$defs/unsealed"}} {
		defs := map[string]any{"root": v, "unsealed": map[string]any{}}
		if _, err := projectionClosure(defs, []string{"root"}, []string{"root"}); err == nil {
			t.Errorf("accepted %v", v)
		}
	}
	if _, err := projectionClosure(map[string]any{"a": nil}, []string{"a"}, []string{"a", "a"}); err == nil {
		t.Fatal("duplicate seal")
	}
	if _, err := projectionClosure(map[string]any{}, nil, nil); err == nil {
		t.Fatal("empty seal")
	}
}

func TestProjectionSealAndDefinitionEditsMoveTheWitness(t *testing.T) {
	defs := loadBundleDefs(t)
	names := projectionNames(t)
	witness := func(names []string, defs map[string]any) string {
		pairs := []any{}
		for _, n := range names {
			pairs = append(pairs, []any{n, defs[n]})
		}
		b, err := restrictedCanonical(pairs)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	for _, changed := range [][]string{names[1:], append(append([]string{}, names...), "sequenceRequest"), append([]string{names[1], names[0]}, names[2:]...)} {
		if witness(changed, defs) == readProjectionSHA256 {
			t.Error("seal edit did not move witness")
		}
	}
	copyDefs := func() map[string]any {
		m := map[string]any{}
		for k, v := range defs {
			m[k] = v
		}
		return m
	}
	for _, name := range []string{"beadRecord", "protocolProfile"} {
		m := copyDefs()
		m[name] = map[string]any{"type": "string"}
		if witness(names, m) == readProjectionSHA256 {
			t.Errorf("edit to %s did not move witness", name)
		}
	}
	m := copyDefs()
	m["sequenceRequest"] = map[string]any{"type": "string"}
	if witness(names, m) != readProjectionSHA256 {
		t.Error("excluded edit moved projection")
	}
	for _, source := range []string{`export const X: readonly string[] = Object.freeze([
"a",
"a",
]);`, `export const X: readonly string[] = Object.freeze([
"a" as const,
]);`} {
		if _, err := quotedDeclaration(source, "X", "Object.freeze([", "]);"); err == nil {
			t.Error("accepted duplicate/unsupported source syntax")
		}
	}
}

func TestProjectionRecipeRequiresVerbatimMatrix(t *testing.T) {
	pin := loadPin(t)
	for _, recipe := range pin.entries {
		if !strings.HasPrefix(recipe.blob, "recipe:") {
			continue
		}
		if err := checkRecipeDependencies(recipe, pin.entries); err != nil {
			t.Fatal(err)
		}
		for _, replacement := range []string{"-", "recipe:read-manifest", ""} {
			entries := append([]pinEntry{}, pin.entries...)
			found := false
			for i := range entries {
				if entries[i].local == "conformance/read-v1.matrix.json" {
					entries[i].blob = replacement
					found = true
				}
			}
			if !found || checkRecipeDependencies(recipe, entries) == nil {
				t.Errorf("%s accepted non-verbatim matrix %q", recipe.blob, replacement)
			}
		}
	}
}

func TestQuotedDeclarationRequiresExactExportAndAssignment(t *testing.T) {
	for _, form := range []struct{ name, open, close, suffix, body string }{
		{"X", "Object.freeze([", "]);", ": readonly string[] = ", `"a",`},
		{"X", "Object.freeze({", "} as const)", " = ", `a: "#/$defs/a",`},
	} {
		source := "export const " + form.name + form.suffix + form.open + "\n" + form.body + "\n" + form.close
		if _, err := quotedDeclaration(source, form.name, form.open, form.close); err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{
			strings.Replace(source, "export const X", "export const X_V2", 1),
			strings.Replace(source, "export const X", "export const X$V2", 1),
			source + "\n" + source,
			strings.Replace(source, form.suffix, " = other;\nconst later = ", 1),
		} {
			if _, err := quotedDeclaration(bad, form.name, form.open, form.close); err == nil {
				t.Errorf("accepted unsupported export/assignment %q", bad)
			}
		}
	}
}

func TestRestrictedCanonicalControlEscapes(t *testing.T) {
	// Go uses the same short escapes for backspace/form-feed as ECMAScript.
	// Other C0 controls use lowercase four-digit escapes; HTML stays unescaped.
	got, err := restrictedCanonical(map[string]any{"\b\f": "\x00\x1f\b\f\n\r\t<>&"})
	if err != nil || string(got) != `{"\b\f":"\u0000\u001f\b\f\n\r\t<>&"}` {
		t.Fatalf("control escape bytes %s: %v", got, err)
	}
}
