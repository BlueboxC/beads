package db

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/domain"
)

func TestConfigSelectionPropagatesReadFailures(t *testing.T) {
	failure := errors.New("selected config read failed")
	for _, tc := range []struct {
		name string
		rows *sqlmock.Rows
	}{
		{"query", nil},
		{"scan", sqlmock.NewRows([]string{"key", "value"}).AddRow(nil, "unreadable key")},
		{"iteration", sqlmock.NewRows([]string{"key", "value"}).AddRow("kv.memory.direction", "keep solution").RowError(0, failure)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			expectation := mock.ExpectQuery("SELECT.*FROM config WHERE").WithArgs("kv.memory.", "kv.memory.", "kv.memory.@", "kv.memory.@")
			if tc.rows == nil {
				expectation.WillReturnError(failure)
			} else {
				expectation.WillReturnRows(tc.rows)
			}
			useCase := domain.NewConfigUseCase(NewConfigSQLRepository(database))
			got, err := useCase.GetConfigByPrefix(context.Background(), "kv.memory.", "kv.memory.@")
			if err == nil || got != nil {
				t.Fatalf("failed read must not publish partial config: got %v, err %v", got, err)
			}
			if tc.name != "scan" && !errors.Is(err, failure) {
				t.Fatalf("underlying failure lost: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
