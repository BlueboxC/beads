//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

func TestHumanGatePolicy(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "hgate")
	ctx := t.Context()
	te.store.SetEventsJournalEnabled(true)
	kit := newEmbeddedRoleFixtureKit(te, "hgate")
	source, ok := any(te.store).(conformance.HumanGateSource)
	if !ok {
		t.Fatalf("%T lacks human-gate mutation roles", te.store)
	}
	conformance.CheckHumanGatePolicy(t, ctx, conformance.HumanGateFixture{
		Source: source, Prefix: "hgate", CreateIssue: kit.CreateIssue, CreateWisp: kit.CreateWisp,
		AddDependency: kit.AddDependency, SetConfig: kit.SetConfig, QueryScalar: kit.QueryScalar,
	})
}
