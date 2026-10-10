// Package graphops provides experimental graph values, validation laws and role
// declarations. It imports only the standard library and beadserrors. Value
// constructors validate their input, and shared laws define canonical paths,
// JSON admission and equality, collection ordering, Scope URLs and ledger hashes.
//
// This foundation does not install a storage accessor, graph schema, History
// recorder, CLI command or HTTP route. The role interfaces declare possible
// consumers of these values; their presence does not advertise a served
// capability. In particular, they do not establish native History commit times,
// authority recovery or complete Memory semantics.
//
// Requests contain no authority credentials. A future storage implementation
// must establish its own authority before serving them; accepting a request
// value alone supplies no authority. This package performs no I/O.
//
// # Stability
//
// EXPERIMENTAL. These declarations are not covered by the project's compatibility
// promise and may change in a minor release. The extraction preserves the graph
// leaf from donnabox/beads at 9c86d6d1559ffcfcd9770b64e17a7f1f654690b3, including
// its independent law tests. Original implementation and review are recorded in
// https://github.com/gastownhall/beads/pull/6422. Integration planning and open
// decisions remain in https://github.com/donnabox/beads/pull/18.
package graphops
