package issueops

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/journalops"
)

func TestHumanGateActorPolicy(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"flag", "env", "provided", "git", "user", "config", "unknown"} {
		t.Run(source, func(t *testing.T) {
			ctx := journalops.WithActorSource(context.Background(), "owner", source)
			err := requireHumanGateActor(ctx, `["owner"]`, "gate", "owner")
			allowed := source == "flag" || source == "env" || source == "provided"
			if (err == nil) != allowed {
				t.Fatalf("source %s: %v", source, err)
			}
		})
	}
	for _, actor := range []string{"", "Owner", "agent"} {
		if err := requireHumanGateActor(context.Background(), `["owner"]`, "gate", actor); !errors.Is(err, storage.ErrValidation) {
			t.Fatalf("actor %q: %v", actor, err)
		}
	}
}

func TestHumanGatePolicyReadFailsClosed(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"query", "scan", "iterate"} {
		t.Run(failure, func(t *testing.T) {
			_, mock, tx := beginMockTx(t)
			expectation := mock.ExpectQuery("SELECT .* FROM config WHERE").WithArgs(HumanGateResolversKey)
			want := errors.New("policy unavailable")
			switch failure {
			case "query":
				expectation.WillReturnError(want)
			case "scan":
				expectation.WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).AddRow(HumanGateResolversKey, nil))
			case "iterate":
				expectation.WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).AddRow(HumanGateResolversKey, `["owner"]`).RowError(0, want))
			}
			if err := EnforceHumanGateMutationInTx(context.Background(), tx, "gate", "owner"); err == nil {
				t.Fatal("unreadable policy admitted mutation")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
