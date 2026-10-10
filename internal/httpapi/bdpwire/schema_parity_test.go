package bdpwire

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// These tests are the drift gate between the vendored bundle and the
// hand-written types — the `make api-check` of this package (GENERATOR.md).
// They parse the embedded bundle and hold every Go declaration to it in both
// directions, so a member added upstream with no Go field fails here, and so
// does a Go field the bundle never promised. They are pure and run in the
// PR workflow's unconditional Go test job.

// defBinding says which Go declaration carries one `$defs` entry.
type defBinding struct {
	// goType is the Go type a `$ref` to this definition resolves to.
	goType reflect.Type
	// object bindings are checked property by property; enum and constant
	// bindings are checked value by value; primitive and sum bindings are
	// checked through their references; new History sums also have explicit
	// branch parity and strict codec tests.
	kind string
	// enumValues are the Go constants' values, for kind "enum".
	enumValues []string
	// constValue is the Go constant's value, for kind "const".
	constValue string
}

func object(v any) defBinding    { return defBinding{goType: reflect.TypeOf(v), kind: "object"} }
func primitive(v any) defBinding { return defBinding{goType: reflect.TypeOf(v), kind: "primitive"} }
func sum(v any) defBinding       { return defBinding{goType: reflect.TypeOf(v), kind: "sum"} }
func constant(v any, c string) defBinding {
	return defBinding{goType: reflect.TypeOf(v), kind: "const", constValue: c}
}

func enum[E ~string](values ...E) defBinding {
	b := defBinding{goType: reflect.TypeOf(values[0]), kind: "enum"}
	for _, v := range values {
		b.enumValues = append(b.enumValues, string(v))
	}
	return b
}

// defsToGo binds every definition in the upstream named Read projection. TestEverySelectedDefinitionIsBound
// holds the key set equal to the named projection in both directions, so a
// definition added upstream must be bound here — and bound to something that
// then has to pass the property checks — before the pin can move.
var defsToGo = map[string]defBinding{
	"changeContext": object(ChangeContext{}),
	"contextTime":   sum(ContextTime{}), "contextString": sum(ContextString{}), "contextMessage": sum(ContextMessage{}),
	"dateTime": primitive(""), "revision": primitive(""), "jsonPointer": primitive(""),
	"historicalBeadRecord": object(HistoricalBeadRecord{}), "historicalLinkRecord": object(HistoricalLinkRecord{}),
	"historyCapability": object(HistoryCapability{}), "historyMissing": object(HistoryMissing{}), "historyMissingItem": sum(HistoryMissingItem{}),
	"historyWindow": object(HistoryWindow{}), "historyVersionsPage": object(HistoryVersionsPage{}), "historyVersionRow": object(HistoryVersionRow{}),

	"absoluteHttpUrl":                   primitive(""),
	"absoluteUri":                       primitive(""),
	"bdpVersion":                        constant("", BDPVersion),
	"protocolProfile":                   enum(ProfileRead, ProfileReadUpdate, ProfileTransactional),
	"retryDisposition":                  enum(RetryNever, RetryAfterStateChange, RetryAfterDelay),
	"readProblemCode":                   enum(CodeMalformedRequest, CodeInvalidParameter, CodeUnauthenticated, CodeForbidden, CodeResourceNotFound, CodeResourcePruned, CodeResourceErased, CodeForeignView, CodeCursorExpired, CodeRequestTooLarge, CodeLimitExceeded, CodeRateLimited, CodeTemporarilyUnavailable, CodeRevisionUnknown, CodeRevisionUnretained, CodeRevisionReorganized, CodeRevisionNotTracked, CodeRevisionUnrepresentable),
	"readProblem":                       object(ReadProblem{}),
	"typeIdArray":                       primitive(TypeIDs(nil)),
	"endpointConstraint":                object(EndpointConstraint{}),
	"typeDescriptor":                    object(TypeDescriptor{}),
	"typeSummary":                       object(TypeSummary{}),
	"typesInventory":                    object(TypesInventory{}),
	"properties":                        primitive(Properties(nil)),
	"metadata":                          primitive(Metadata(nil)),
	"beadRecord":                        object(BeadRecord{}),
	"linkRecord":                        object(LinkRecord{}),
	"currentBeadRecord":                 object(CurrentBeadRecord{}),
	"currentLinkRecord":                 object(CurrentLinkRecord{}),
	"currentBeadCollection":             object(CurrentBeadCollection{}),
	"currentLinkCollection":             object(CurrentLinkCollection{}),
	"beadCollection":                    object(BeadCollection{}),
	"linkCollection":                    object(LinkCollection{}),
	"positiveInteger":                   primitive(0),
	"iso8601Duration":                   primitive(""),
	"advertisedLimits":                  object(AdvertisedLimits{}),
	"maximumEndpointMultiplicityPolicy": object(MaximumEndpointMultiplicityPolicy{}),
	"readDiscovery":                     object(ReadDiscovery{}),
	"reference":                         sum(Reference{}),
	"pinnedReference":                   object(pinnedReferenceJSON{}),
	"ownedLinkDeclaration":              object(OwnedLinkDeclaration{}),
	"ownedWildcardDeclaration":          object(OwnedWildcardDeclaration{}),
	"attribution":                       object(Attribution{}),
}

// inlineEnums are the vocabularies the bundle declares inline on a property
// rather than as a shared definition, keyed by the Go type that carries them.
// A property with an inline string enum must be typed with one of these, and
// the sets must match.
var inlineEnums = map[reflect.Type][]string{
	reflect.TypeOf(HistoryLineage("")):       {string(HistoryCurrent), string(HistoryReplaced)},
	reflect.TypeOf(HistoryBody("")):          {string(HistoryComplete), string(HistoryIncomplete)},
	reflect.TypeOf(HistoryParticipation("")): {string(HistoryTracked), string(HistoryNotTracked), string(HistoryUndetermined)},
	reflect.TypeOf(Describes("")):            {string(DescribesBead), string(DescribesLink)},
	reflect.TypeOf(ExternalPolicy("")):       {string(ExternalNone), string(ExternalOpaque), string(ExternalBead)},
	reflect.TypeOf(Endpoint("")):             {string(EndpointSource), string(EndpointTarget)},
	reflect.TypeOf(AttributionBasis("")):     {string(AttributionWriterSupplied), string(AttributionUnknown)},
	reflect.TypeOf(CollectionOrder("")):      {string(OrderCanonicalURI)},
}

// propertyConsts are the members the bundle pins to one value inline.
var propertyConsts = map[string]any{
	"historyCapability/version":      json.Number("1"),
	"historyVersionsPage/population": HistoryPopulation,
	"readDiscovery/profile":          string(ProfileRead),
}

type goField struct {
	field     reflect.StructField
	omitempty bool
}

// jsonFields collects a struct's wire members the way encoding/json does: the
// tag name, or the Go name when untagged (an untagged exported field IS a
// wire member), with `json:"-"` fields returned separately as carriers.
func jsonFields(t *testing.T, rt reflect.Type) (fields map[string]goField, carriers []string) {
	t.Helper()
	if rt.Kind() != reflect.Struct {
		t.Fatalf("%s is not a struct", rt)
	}
	fields = map[string]goField{}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			carriers = append(carriers, f.Name)
			continue
		}
		if name == "" {
			name = f.Name
		}
		if _, dup := fields[name]; dup {
			t.Fatalf("%s: two fields marshal as %q", rt, name)
		}
		fields[name] = goField{field: f, omitempty: strings.Contains(","+opts+",", ",omitempty,") || strings.Contains(","+opts+",", ",omitzero,")}
	}
	return fields, carriers
}

func derefPointer(rt reflect.Type) reflect.Type {
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	return rt
}

func refName(t *testing.T, schema map[string]any) (string, bool) {
	t.Helper()
	ref, ok := schema["$ref"]
	if !ok {
		return "", false
	}
	name := strings.TrimPrefix(asString(t, ref, "$ref"), "#/$defs/")
	if name == ref {
		t.Fatalf("$ref %q does not point into the bundle's $defs", ref)
	}
	return name, true
}

func stringSet(t *testing.T, values []any) map[string]bool {
	t.Helper()
	set := map[string]bool{}
	for _, v := range values {
		set[asString(t, v, "enum member")] = true
	}
	return set
}

func sliceSet(values []string) map[string]bool {
	set := map[string]bool{}
	for _, v := range values {
		set[v] = true
	}
	return set
}

func TestEverySelectedDefinitionIsBound(t *testing.T) {
	defs := selectedBundleDefs(t)
	if unbound := diff(defs, defsToGo); len(unbound) > 0 {
		t.Errorf("bundle definitions with no Go binding: %v\nbind each in defsToGo (schema_parity_test.go) to the type that carries it", unbound)
	}
	if stale := diff(defsToGo, defs); len(stale) > 0 {
		t.Errorf("Go bindings for definitions the bundle no longer has: %v", stale)
	}
}

func TestEnumAndConstDefinitionsMatchTheBundle(t *testing.T) {
	defs := loadBundleDefs(t)
	for name, binding := range defsToGo {
		def := asMap(t, defs[name], name)
		switch binding.kind {
		case "enum":
			want := stringSet(t, asSlice(t, def["enum"], name+".enum"))
			got := sliceSet(binding.enumValues)
			if missing := diff(want, got); len(missing) > 0 {
				t.Errorf("%s: bundle members with no Go constant: %v", name, missing)
			}
			if extra := diff(got, want); len(extra) > 0 {
				t.Errorf("%s: Go constants the bundle does not define: %v", name, extra)
			}
			// The Valid method is the enum's membership test; it must agree
			// with the constants and reject the empty string.
			for member := range want {
				if !reflect.ValueOf(member).Convert(binding.goType).MethodByName("Valid").Call(nil)[0].Bool() {
					t.Errorf("%s: %s(%q).Valid() = false", name, binding.goType, member)
				}
			}
			if reflect.ValueOf("").Convert(binding.goType).MethodByName("Valid").Call(nil)[0].Bool() {
				t.Errorf("%s: %s(\"\").Valid() = true", name, binding.goType)
			}
		case "const":
			if got := def["const"]; got != binding.constValue {
				t.Errorf("%s: bundle const %v, Go const %q", name, got, binding.constValue)
			}
		}
	}
}

// collectProperties returns an object schema's members: `properties` plus
// every member a conditional branch (`allOf[*].then` / `.else`) adds, minus
// members a branch forbids with a `false` schema — which is how readProblem
// acquires archivedAt (allowed only for resource-pruned) and how
// typeDescriptor's endpoint constraints and ownsOutgoing are conditioned.
func collectProperties(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	props := map[string]any{}
	for k, v := range asMapOrEmpty(t, schema["properties"]) {
		props[k] = v
	}
	if all, ok := schema["allOf"]; ok {
		for _, item := range asSlice(t, all, "allOf") {
			for _, branch := range []string{"then", "else"} {
				b, ok := asMap(t, item, "allOf item")[branch]
				if !ok {
					continue
				}
				for k, v := range asMapOrEmpty(t, asMap(t, b, branch)["properties"]) {
					if v == false {
						continue
					}
					if _, seen := props[k]; !seen {
						props[k] = v
					}
				}
			}
		}
	}
	return props
}

func asMapOrEmpty(t *testing.T, v any) map[string]any {
	t.Helper()
	if v == nil {
		return map[string]any{}
	}
	return asMap(t, v, "properties")
}

func TestObjectDefinitionsMatchTheirStructs(t *testing.T) {
	defs := loadBundleDefs(t)
	for name, binding := range defsToGo {
		if binding.kind != "object" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			checkObject(t, defs, name, resolvedObject(t, defs, name), binding.goType)
		})
	}
}

// checkObject holds one object schema to one struct: member bijection,
// required ⇔ !omitempty, closed ⇔ no carrier, and for every member the Go
// field's type against what the schema says the member is.
func checkObject(t *testing.T, defs map[string]any, path string, schema map[string]any, rt reflect.Type) {
	t.Helper()
	rt = derefPointer(rt)
	props := collectProperties(t, schema)
	fields, carriers := jsonFields(t, rt)

	if missing := diff(props, fields); len(missing) > 0 {
		t.Errorf("%s: schema members with no Go field on %s: %v", path, rt, missing)
	}
	if extra := diff(fields, props); len(extra) > 0 {
		t.Errorf("%s: Go fields on %s the schema never promised: %v", path, rt, extra)
	}

	required := map[string]bool{}
	if req, ok := schema["required"]; ok {
		required = stringSet(t, asSlice(t, req, path+".required"))
	}
	for member, f := range fields {
		if _, ok := props[member]; !ok {
			continue
		}
		if required[member] && f.omitempty {
			t.Errorf("%s/%s is required but %s.%s carries omitempty: a zero value would vanish from the wire", path, member, rt, f.field.Name)
		}
		if !required[member] && !f.omitempty {
			t.Errorf("%s/%s is optional but %s.%s lacks omitempty: an unset value would ship as a member", path, member, rt, f.field.Name)
		}
	}

	closed := schema["additionalProperties"] == false
	switch {
	case closed && len(carriers) > 0:
		t.Errorf("%s is closed (additionalProperties: false) but %s carries %v for unknown members", path, rt, carriers)
	case !closed && len(carriers) != 1:
		t.Errorf("%s is open but %s has %d unknown-member carriers, want exactly one", path, rt, len(carriers))
	}

	if additional, ok := schema["additionalProperties"].(map[string]any); ok && len(carriers) == 1 {
		carrier, _ := rt.FieldByName(carriers[0])
		if carrier.Type.Kind() != reflect.Map || carrier.Type.Key().Kind() != reflect.String {
			t.Fatalf("%s: typed additional properties need a string-keyed map", path)
		}
		checkMember(t, defs, path+"/*", additional, carrier.Type.Elem())
	}

	for member, f := range fields {
		prop, ok := props[member]
		if !ok {
			continue
		}
		checkMember(t, defs, path+"/"+member, asMap(t, prop, path+"/"+member), f.field.Type)
	}
}

// checkMember holds one member's Go type to its schema.
func checkMember(t *testing.T, defs map[string]any, path string, schema map[string]any, ft reflect.Type) {
	t.Helper()
	if name, ok := refName(t, schema); ok {
		binding, bound := defsToGo[name]
		if !bound {
			t.Errorf("%s: $ref to unbound definition %q", path, name)
			return
		}
		if got := derefPointer(ft); got != binding.goType {
			t.Errorf("%s: Go type %s, but the member is #/$defs/%s which is carried by %s", path, ft, name, binding.goType)
		}
		return
	}
	for _, keyword := range []string{"oneOf", "anyOf"} {
		if alternatives, ok := schema[keyword]; ok {
			alts := asSlice(t, alternatives, path+keyword)
			if len(alts) != 2 || !schemaNullable(t, schema) {
				t.Fatalf("%s: unsupported alternative form", path)
			}
			if ft.Kind() != reflect.Pointer {
				t.Fatalf("%s nullable member is not a pointer", path)
			}
			count := 0
			for _, alt := range alts {
				m := asMap(t, alt, path)
				if m["type"] != "null" {
					count++
					checkMember(t, defs, path, m, ft.Elem())
				}
			}
			if count != 1 {
				t.Fatalf("%s: not exactly one nonnull arm", path)
			}
			return
		}
	}
	if all, ok := schema["allOf"]; ok {
		for _, part := range asSlice(t, all, path) {
			m := asMap(t, part, path)
			if _, ok := m["$ref"]; ok {
				checkMember(t, defs, path, m, ft)
				return
			}
		}
		t.Fatalf("%s unsupported member allOf", path)
	}
	if c, ok := schema["const"]; ok {
		want, listed := propertyConsts[path]
		if !listed {
			t.Errorf("%s: inline const %v not listed in propertyConsts", path, c)
		} else if c != want {
			t.Errorf("%s: bundle const %v, Go const %q", path, c, want)
		}
		wantKind := reflect.String
		if _, ok := c.(json.Number); ok {
			wantKind = reflect.Int
		}
		if derefPointer(ft).Kind() != wantKind {
			t.Errorf("%s: wrong const kind %s", path, ft)
		}
		return
	}
	if members, ok := schema["enum"]; ok {
		values := asSlice(t, members, path+".enum")
		if _, isString := values[0].(string); !isString {
			// readProblem.status: an integer enum, held to the problem table
			// by TestProblemStatusEnumIsTheTableImage.
			if derefPointer(ft).Kind() != reflect.Int {
				t.Errorf("%s: integer enum must be int-kinded, got %s", path, ft)
			}
			return
		}
		want := stringSet(t, values)
		got, known := inlineEnums[derefPointer(ft)]
		if !known {
			t.Errorf("%s: inline enum %v must be carried by a named enum type listed in inlineEnums, got %s", path, sortedKeys(want), ft)
			return
		}
		if missing, extra := diff(want, sliceSet(got)), diff(sliceSet(got), want); len(missing) > 0 || len(extra) > 0 {
			t.Errorf("%s: enum drift for %s: bundle-only %v, Go-only %v", path, ft, missing, extra)
		}
		return
	}
	switch schema["type"] {
	case "object":
		if _, hasProps := schema["properties"]; hasProps {
			if derefPointer(ft).Kind() != reflect.Struct {
				t.Errorf("%s: inline object with properties must be a struct, got %s", path, ft)
				return
			}
			checkObject(t, defs, path, schema, ft)
			return
		}
		ap, ok := schema["additionalProperties"].(map[string]any)
		if !ok {
			t.Errorf("%s: object with neither properties nor a schema-valued additionalProperties", path)
			return
		}
		mt := derefPointer(ft)
		if mt.Kind() != reflect.Map || mt.Key().Kind() != reflect.String {
			t.Errorf("%s: schema-valued additionalProperties must be a string-keyed map, got %s", path, ft)
			return
		}
		checkMember(t, defs, path+"/*", ap, mt.Elem())
	case "array":
		st := derefPointer(ft)
		if st.Kind() != reflect.Slice {
			t.Errorf("%s: array member must be a slice, got %s", path, ft)
			return
		}
		checkMember(t, defs, path+"/[]", asMap(t, schema["items"], path+".items"), st.Elem())
	case "string":
		if derefPointer(ft).Kind() != reflect.String {
			t.Errorf("%s: string member typed %s", path, ft)
		}
	case "boolean":
		if derefPointer(ft).Kind() != reflect.Bool {
			t.Errorf("%s: boolean typed %s", path, ft)
		}
	case "integer":
		if derefPointer(ft).Kind() != reflect.Int {
			t.Errorf("%s: integer member typed %s", path, ft)
		}
	default:
		t.Errorf("%s: unhandled schema shape %v", path, sortedKeys(schema))
	}
}

// problemRows derives the closed problem table from the bundle's readProblem
// conditionals: each `if code == X then {type, status, retry}` row.
func problemRows(t *testing.T, defs map[string]any) map[string]map[string]any {
	t.Helper()
	rows := map[string]map[string]any{}
	for code := range readProblemTable {
		rows[string(code)] = map[string]any{}
	}
	problem := asMap(t, defs["readProblem"], "readProblem")
	for _, item := range asSlice(t, problem["allOf"], "allOf") {
		clause := asMap(t, item, "clause")
		condition := asMap(t, clause["if"], "if")
		for code, row := range rows {
			branch := "else"
			if problemCodeCondition(t, condition, code) {
				branch = "then"
			}
			if b, ok := clause[branch]; ok {
				for k, v := range asMapOrEmpty(t, asMap(t, b, branch)["properties"]) {
					row[k] = v
				}
			}
		}
	}
	return rows
}
func problemCodeCondition(t *testing.T, condition map[string]any, code string) bool {
	t.Helper()
	if n, ok := condition["not"]; ok {
		if len(condition) != 1 {
			t.Fatal("unknown not guard")
		}
		return !problemCodeCondition(t, asMap(t, n, "not"), code)
	}
	if len(condition) != 2 || !reflect.DeepEqual(condition["required"], []any{"code"}) {
		t.Fatal("unsupported code guard")
	}
	props := asMap(t, condition["properties"], "guard properties")
	if len(props) != 1 {
		t.Fatal("unknown guard properties")
	}
	c := asMap(t, props["code"], "code")
	if len(c) != 1 {
		t.Fatal("unknown code constraint")
	}
	if v, ok := c["const"]; ok {
		return code == asString(t, v, "code const")
	}
	if values, ok := c["enum"]; ok {
		return stringSet(t, asSlice(t, values, "enum"))[code]
	}
	t.Fatal("unsupported code guard")
	return false
}

func TestProblemTableMatchesTheBundle(t *testing.T) {
	defs := loadBundleDefs(t)
	rows := problemRows(t, defs)

	table := map[string]bool{}
	for code := range readProblemTable {
		table[string(code)] = true
	}
	if missing := diff(rows, table); len(missing) > 0 {
		t.Errorf("bundle problem rows with no table entry: %v", missing)
	}
	if extra := diff(table, rows); len(extra) > 0 {
		t.Errorf("table entries the bundle has no conditional row for: %v", extra)
	}

	for codeName, row := range rows {
		code := ReadProblemCode(codeName)
		if got, want := code.Type(), asMap(t, row["type"], codeName+".type")["const"]; got != want {
			t.Errorf("%s: Type() = %q, bundle says %v", codeName, got, want)
		}
		if got, want := json.Number(itoa(code.Status())), asMap(t, row["status"], codeName+".status")["const"]; got != want {
			t.Errorf("%s: Status() = %v, bundle says %v", codeName, got, want)
		}
		if got, want := string(code.Retry()), asMap(t, row["retry"], codeName+".retry")["const"]; got != want {
			t.Errorf("%s: Retry() = %q, bundle says %v", codeName, got, want)
		}
		archivedAt := row["archivedAt"] != nil && row["archivedAt"] != false
		if archivedAt != (code == CodeResourcePruned) {
			t.Errorf("%s: bundle allows archivedAt = %v, Validate allows it only for %s", codeName, archivedAt, CodeResourcePruned)
		}
	}
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func TestProblemStatusEnumIsTheTableImage(t *testing.T) {
	defs := loadBundleDefs(t)
	status := asMap(t, asMap(t, asMap(t, defs["readProblem"], "readProblem")["properties"], "properties")["status"], "status")
	want := map[string]bool{}
	for _, v := range asSlice(t, status["enum"], "status.enum") {
		want[v.(json.Number).String()] = true
	}
	got := map[string]bool{}
	for _, row := range readProblemTable {
		got[json.Number(itoa(row.status)).String()] = true
	}
	if missing, extra := diff(want, got), diff(got, want); len(missing) > 0 || len(extra) > 0 {
		t.Errorf("readProblem.status enum drift: bundle-only %v, table-only %v", missing, extra)
	}
}

// TestZeroValuesMarshalEveryRequiredMember is the shape half of the contract
// an authority needs: a zero envelope must still put every required member on
// the wire (`items: []`, `properties: {}`, `next: null`, `conformsTo: []`) and
// no optional one. It is driven by the bundle's `required` lists, so a member
// that becomes required upstream fails here until the Go side serves it.
func TestZeroValuesMarshalEveryRequiredMember(t *testing.T) {
	defs := loadBundleDefs(t)
	for name, binding := range defsToGo {
		if binding.kind != "object" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			def := resolvedObject(t, defs, name)
			zero := reflect.New(binding.goType).Interface()
			data, err := json.Marshal(zero)
			if err != nil {
				if invalidHistoryZero[name] {
					return
				}
				t.Fatalf("marshal zero %s: %v", binding.goType, err)
			}
			if invalidHistoryZero[name] {
				t.Fatalf("invalid zero %s marshaled successfully", name)
			}
			var members map[string]json.RawMessage
			if err := json.Unmarshal(data, &members); err != nil {
				t.Fatalf("zero %s marshaled as %s, not an object: %v", binding.goType, data, err)
			}
			required := map[string]bool{}
			if req, ok := def["required"]; ok {
				required = stringSet(t, asSlice(t, req, "required"))
			}
			for member := range required {
				if _, ok := members[member]; !ok {
					t.Errorf("zero %s omits required member %q: %s", binding.goType, member, data)
				}
			}
			for member, raw := range members {
				if !required[member] {
					t.Errorf("zero %s serves optional member %q: %s", binding.goType, member, data)
				}
				if string(raw) == "null" && !schemaNullable(t, asMap(t, collectProperties(t, def)[member], member)) {
					t.Errorf("zero %s serves %q as null, which no Read schema permits: %s", binding.goType, member, data)
				}
			}
		})
	}
}

// TestValidMethodsRejectStrangers checks both directions for every inline enum
// and the closed sum vocabularies, as well as the inherited shared enums.
func TestValidMethodsRejectStrangers(t *testing.T) {
	vocabularies := map[reflect.Type][]string{}
	for rt, values := range inlineEnums {
		vocabularies[rt] = values
	}
	vocabularies[reflect.TypeOf(HistoryMissingKind(""))] = []string{string(MissingRecord), string(MissingProperty), string(MissingOwnedLinks)}
	vocabularies[reflect.TypeOf(ContextState(""))] = []string{string(ContextPresent), string(ContextAbsent), string(ContextUndetermined)}
	vocabularies[reflect.TypeOf(ContextTimeState(""))] = []string{string(ContextTimePresent), string(ContextTimeUndetermined)}
	for _, binding := range defsToGo {
		if binding.kind == "enum" {
			vocabularies[binding.goType] = binding.enumValues
		}
	}
	for rt, values := range vocabularies {
		for _, value := range append(append([]string{}, values...), "", "bogus") {
			valid := reflect.ValueOf(value).Convert(rt).Interface().(interface{ Valid() bool }).Valid()
			if valid != sliceSet(values)[value] {
				t.Errorf("%s(%q).Valid() = %v", rt, value, valid)
			}
		}
	}
	if ContextTimeState(ContextAbsent).Valid() {
		t.Error("time context admits absent")
	}
}
