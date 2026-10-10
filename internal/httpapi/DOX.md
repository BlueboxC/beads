# DOX.md — HTTP transport

## Purpose

Own the bounded HTTP API transport and the fork's exclusive graph viewer mode.

## Ownership

Listener, Host allowlist, optional bearer authentication, request/concurrency limits, shutdown and route selection. Graphview owns presentation; callers own database source selection.

## Local Contracts

- Normal API mode retains its complete provider/roles source requirement and per-request atomic writes. Embedded Dolt remains refused for that mode.
- GraphViewer is exclusive: register only GET /viewer, GET /viewer/graph and liveness /healthz. Refuse any simultaneous provider, issue role or events journal. Do not advertise issue-write capabilities or expose v0 routes.
- Reuse the existing bind, Host, auth, request deadline, semaphore and shutdown controls. The generic bootstrap is public and contains no workspace data; graph data requires bearer authentication when configured. Loopback retains the existing default trust boundary.

- CLI JSONL, HTTP pages and SSE share the optional journal `actor_source` field through the canonical Record alias. Keep OpenAPI and the wire-tag bijection current; caller provenance never changes bearer authorization or authenticates a human.

- Normal API callers may supply ExternalResolver; bind it to each request context behind existing Host/auth controls. Provider paths remain caller-owned and missing resolution stays blocked. The exclusive graph viewer does not acquire external provider configuration.

## Work Guidance

Reuse the transport; do not create a second API or persistence engine for graphs.

## Verification

Run affected httpapi tests, including exclusive route, Host/auth and mixed-source refusal checks.

## Child DOX Index
