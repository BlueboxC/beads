package uow

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

func TestHumanGatePolicy(t *testing.T) {
	ctx := t.Context()
	provider := newUOWRoleFixtureProvider(t, ctx, "hgate")
	provider.(*doltSQLProvider).SetEventsJournalEnabled(true)
	kit := newUOWRoleFixtureKit(provider, "hgate")
	source, ok := any(provider).(conformance.HumanGateSource)
	if !ok {
		t.Fatalf("%T lacks human-gate mutation roles", provider)
	}
	conformance.CheckHumanGatePolicy(t, ctx, conformance.HumanGateFixture{
		Source: source, Prefix: "hgate", CreateIssue: kit.CreateIssue, CreateWisp: kit.CreateWisp,
		AddDependency: kit.AddDependency, SetConfig: kit.SetConfig, QueryScalar: kit.QueryScalar,
	})
}

// Reuse the disposable native SQL server to exercise the classic adapter too;
// its package-level container fixture is unavailable on hosts without Docker.
func TestHumanGateClassicNative(t *testing.T) {
	ctx := t.Context()
	provider := newUOWRoleFixtureProvider(t, ctx, "hgate")
	p := provider.(*doltSQLProvider)
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(p.serverEndpoint, "tcp:"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	store, err := dolt.New(ctx, &dolt.Config{Path: t.TempDir(), Database: "beads", ServerHost: host, ServerPort: port, ServerUser: "root", MaxOpenConns: 1, CommitterName: "fixture", CommitterEmail: "fixture@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetEventsJournalEnabled(true)
	kit := newUOWRoleFixtureKit(provider, "hgate")
	conformance.CheckHumanGatePolicy(t, ctx, conformance.HumanGateFixture{
		Source: store, Prefix: "hgate", CreateIssue: store.CreateIssue, CreateWisp: store.CreateIssue,
		AddDependency: store.AddDependency, SetConfig: store.SetConfig, QueryScalar: kit.QueryScalar,
	})
}
