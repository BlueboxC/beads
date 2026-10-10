// Package bdpwire contains experimental BDP Read and structural History wire
// codecs pinned to an upstream protocol schema bundle. It provides values and
// serialization checks only: no HTTP handler, client transport, storage adapter
// or History recorder is activated by this package.
//
// This extraction preserves the qualified wire implementation from donnabox/beads
// at 9c86d6d1559ffcfcd9770b64e17a7f1f654690b3. It is not evidence that a server
// implements the capabilities described by those values. Original provenance is
// recorded in https://github.com/gastownhall/beads/pull/6422 and
// https://github.com/donnabox/beads/pull/2; integration review remains in
// https://github.com/donnabox/beads/pull/18.
//
// THE PROVENANCE FILE (spec B8). The bundle is vendored verbatim at schema/bdp-v0.schema.json from
// gastownhall/bdp commit de99030d, with the Read-profile
// fixtures and the executable Read matrix from the same commit beside it.
// schema/PROVENANCE names every vendored file with its sha256 and its upstream git
// blob sha1, and pin_test.go recomputes both from the bytes on disk, so a
// re-pin is an edit to PROVENANCE that review can see and a drifted fixture is a
// failing test — never a network fetch. Nothing in this package touches the
// network, at build time or in tests.
// The complete bundle remains provenance; the checked derived manifest and
// canonical projection witness declare the selected Read DTO scope. All 111
// excluded definitions remain accounted for without write DTO admission.
//
// HAND-WRITTEN, VALIDATED AGAINST THE BUNDLE. The types here are written by
// hand and welded to the bundle by schema_parity_test.go, which parses the
// vendored bytes and asserts, for every definition in the upstream named Read
// projection (42 of the complete bundle's 153 definitions): two-way equality
// between the schema's property list and the struct's JSON tags; that a
// required member never carries omitempty and an optional one always does;
// that a closed envelope carries no extension carrier and the one open
// envelope does; that every enum vocabulary matches constant for constant;
// and that the problem code → family/status/retry table equals the bundle's
// conditional rows. GENERATOR.md records why no generator was used (spec
// Part D.3), with the evidence, and what would change that. The parity tests
// run under `go test ./internal/httpapi/...`, which is the second half of
// `make api-check`, so the existing OpenAPI drift gate covers this contract
// too and no Makefile change was needed.
//
// WIRE SHAPE AND BOUNDED DECLARATION CHECKS. A type says what a document looks
// like on the wire: which members exist, which are required, which are
// closed, which JSON type each has. DECISION: it does not generally validate
// URL grammar, canonical-ID spelling, Type contracts, or enum membership on the
// way in — those are model laws enforced by a domain implementation, not
// by this transport package. What the
// decoder DOES hold a document to is the structural facts the bundle states
// about the bytes themselves and that a Go value could not otherwise carry
// faithfully (decode.go): exact, case-sensitive member names; a required
// member present; null refused wherever the bundle gives no null (absent and
// null are different things; collection next and History bounds/next may be null);
// each member's JSON type; integers decoded exactly from any RFC 8259
// spelling within the documented int range; Reference's string-or-object sum
// (an object arm needs a nonempty revision, or the Go value would read as
// the string arm). The two constants the bundle pins on a Read discovery
// document (bdpVersion "0", profile "read") and the closed problem table are
// checked by ReadDiscovery.Validate and ReadProblem.Validate. The owned
// declaration sum additionally checks the pinned key pattern, positive bounds
// and the explicit-max versus wildcard-max rule when decoding or marshaling;
// OwnedLinks keys follow that same pinned pattern through both decoding entry
// points and Validate. resource-erased rejects pointer presence through those
// entry points and Validate. For OwnedOutgoingDeclarations and ReadProblem,
// both entry points dispatch to the same respective decoder; their tests
// prove custom-method wiring.
//
// DECODING POSTURE. DECISION: Unmarshal and Decode are strict — an unknown
// member in a closed envelope is an error, and so are the shape violations
// above — because the spec says the normative schemas alone decide where
// additional members are allowed ("no implicit minor-version rule") and
// every public envelope closes its protocol-owned members; the spec does not
// say what a CLIENT should do with a stranger, and rejecting is the reading
// a conformance check wants. The decoder is this package's own (decode.go):
// encoding/json matches member names case-insensitively and maps null onto
// the zero value, neither of which is a closed shape. The two open places —
// a Resource's `properties` document and permitted RFC 9457 extension members
// on a problem — are carried through byte for byte. A caller that wants a
// lenient read of a record uses encoding/json directly; Reference and
// ReadProblem and OwnedOutgoingDeclarations stay strict under it too, since
// they decode themselves. OwnedLinks also checks its key pattern under
// encoding/json, while leaving its records to ordinary decoding.
//
// HISTORY STRUCTURAL BOUNDARY. Optional historicalResolution and changeContext
// preserve received wire values without fabricating capability or legacy context.
// New Context, HistoryCapability, MissingItem/Missing, Window, VersionsPage,
// VersionRow, HistoricalBeadRecord and HistoricalLinkRecord codecs share strict custom entrypoints.
// They enforce tagged arms, member presence, boolean/integer kind, required
// nullable bounds, missing-item cardinality/uniqueness, paired bounds, page
// participation and unique revisions. Multiple current lineage rows remain legal.
// ReadDiscovery.Validate also validates a present capability version. New History
// problem payload/exclusion guards apply on decode, marshal and Validate; the
// inherited family/status/retry and ordinary archivedAt semantics remain explicit
// Validate checks. Date-time, URL and graph/provider admission semantics are not
// inferred from transport shapes. The historical Link schema is an exact alias
// of the ordinary Link schema. Its distinct Go type retains the same fields and
// tags while adding strict codecs; ordinary LinkRecord's encoding/json behavior
// remains unchanged.
//
// Before strict decoding, raw UTF-8 and escaped surrogate pairs are checked,
// including member names and nested raw properties/extensions. Duplicate decoded
// names are rejected without replacing the inherited nested diagnostic paths.
// New strict Marshal methods reject invalid Go strings before JSON encoding can
// replace bytes. Explicit null clears reused nullable destinations. The legacy
// ordinary-json envelope path remains lenient where documented above.
//
// The package imports the standard library and nothing else, and
// imports_test.go keeps it that way: it sits beneath internal/httpapi and,
// later, a client package, and must pull neither's dependencies into the
// other. It must never import graphops or any internal/storage package.
package bdpwire
