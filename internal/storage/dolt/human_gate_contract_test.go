package dolt

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

func TestHumanGatePolicy(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	store.SetEventsJournalEnabled(true)
	kit := newDoltRoleFixtureKit(store, "hgate")
	source, ok := any(store).(conformance.HumanGateSource)
	if !ok {
		t.Fatalf("%T lacks human-gate mutation roles", store)
	}
	conformance.CheckHumanGatePolicy(t, ctx, conformance.HumanGateFixture{
		Source: source, Prefix: "hgate", CreateIssue: kit.CreateIssue, CreateWisp: kit.CreateWisp,
		AddDependency: kit.AddDependency, SetConfig: kit.SetConfig, QueryScalar: kit.QueryScalar,
	})
}
