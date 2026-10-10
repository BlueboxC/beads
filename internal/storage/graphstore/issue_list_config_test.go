//go:build cgo

package graphstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

func TestGraphIssueListResolvedConfigBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  workapi.ListConfig
		bad  bool
	}{
		{name: "empty"},
		{name: "entry boundary", cfg: workapi.ListConfig{CustomTypes: make([]string, PreviewIssueListConfigRowLimit)}},
		{name: "entry overflow including duplicates", cfg: workapi.ListConfig{CustomTypes: make([]string, PreviewIssueListConfigRowLimit+1)}, bad: true},
		{name: "byte boundary", cfg: workapi.ListConfig{CustomTypes: []string{strings.Repeat("x", PreviewIssueListConfigByteLimit)}}},
		{name: "byte overflow", cfg: workapi.ListConfig{CustomTypes: []string{strings.Repeat("x", PreviewIssueListConfigByteLimit+1)}}, bad: true},
		{name: "category bytes boundary", cfg: workapi.ListConfig{CustomStatuses: []types.CustomStatus{{Name: strings.Repeat("x", PreviewIssueListConfigByteLimit-len(types.CategoryActive)), Category: types.CategoryActive}}}},
		{name: "category bytes overflow", cfg: workapi.ListConfig{CustomStatuses: []types.CustomStatus{{Name: strings.Repeat("x", PreviewIssueListConfigByteLimit), Category: types.CategoryActive}}}, bad: true},
		{name: "combined entry overflow", cfg: workapi.ListConfig{CustomStatuses: make([]types.CustomStatus, 100), CustomTypes: make([]string, 156), InfraSet: map[string]bool{"infra": true}}, bad: true},
		{name: "combined byte overflow", cfg: workapi.ListConfig{CustomStatuses: []types.CustomStatus{{Name: strings.Repeat("x", PreviewIssueListConfigByteLimit-1)}}, CustomTypes: []string{"y"}, InfraSet: map[string]bool{"z": true}}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkResolvedIssueListConfig(tc.cfg)
			if tc.bad {
				if !errors.Is(err, ErrLimitExceeded) {
					t.Fatalf("want limit error, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func issueListYAMLConfig(t *testing.T) {
	t.Helper()
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(config.ResetForTesting)
}

func issueListPolicyInTx(t *testing.T, ctx context.Context, s *Store) workapi.ListConfig {
	t.Helper()
	var got workapi.ListConfig
	if err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		var err error
		got, err = issueListConfigInTx(ctx, tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

// This exercises real initialized stores and API-authored Issues/configuration.
// YAML is the serial frontend context, not transactionally stored configuration.
func TestGraphIssueListResolvedConfigRealStores(t *testing.T) {
	issueListYAMLConfig(t)
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			for _, key := range []string{"status.custom", "types.custom", "types.infra"} {
				config.Set(key, []string{})
			}
			ctx, _, s := issueListFixture(t, backend)
			created, err := s.CreateIssue(ctx, "beads/policy", plainIssue("policy"))
			if err != nil {
				t.Fatal(err)
			}
			before := reopenState(t, ctx, s)
			defaultBytes := 0
			for _, name := range domain.DefaultInfraTypes() {
				defaultBytes += len(name)
			}
			names := func(prefix string, count int) []string {
				out := make([]string, count)
				for i := range out {
					out[i] = fmt.Sprintf("%s%d", prefix, i)
				}
				return out
			}
			for _, tc := range []struct {
				name                   string
				statuses, kinds, infra []string
				bad                    bool
			}{
				{name: "empty fallbacks"},
				{name: "resolved entry boundary", kinds: names("kind", PreviewIssueListConfigRowLimit-len(domain.DefaultInfraTypes()))},
				{name: "resolved entry overflow", kinds: names("kind", PreviewIssueListConfigRowLimit+1), bad: true},
				{name: "resolved byte boundary", kinds: []string{strings.Repeat("x", PreviewIssueListConfigByteLimit-defaultBytes)}},
				{name: "resolved byte overflow", kinds: []string{strings.Repeat("x", PreviewIssueListConfigByteLimit-defaultBytes+1)}, bad: true},
				{name: "status YAML overflow", statuses: names("status", PreviewIssueListConfigRowLimit+1), bad: true},
				{name: "infra YAML overflow", infra: names("infra", PreviewIssueListConfigRowLimit+1), bad: true},
				{name: "combined YAML overflow", statuses: names("status", 100), kinds: names("kind", 100), infra: names("infra", 100), bad: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					config.Set("status.custom", tc.statuses)
					config.Set("types.custom", tc.kinds)
					config.Set("types.infra", tc.infra)
					page, err := s.ListIssues(ctx, publicops.ListRequest{})
					if tc.bad {
						if !errors.Is(err, ErrLimitExceeded) || !reflect.DeepEqual(page, IssueListPage{}) {
							t.Fatalf("expected zero-page limit refusal: %+v %v", page, err)
						}
					} else if err != nil || len(page.Items) != 1 || page.Items[0].Version != created.Version {
						t.Fatalf("admitted policy changed result: %+v %v", page, err)
					}
					if after := reopenState(t, ctx, s); !reflect.DeepEqual(before, after) {
						t.Fatal("configuration read changed graph/domain state")
					}
				})
			}
			config.Set("status.custom", []string{"yaml-active:active"})
			config.Set("types.custom", []string{"yaml-kind"})
			config.Set("types.infra", []string{"yaml-infra"})
			want := workapi.ListConfig{CustomStatuses: []types.CustomStatus{{Name: "yaml-active", Category: types.CategoryActive}}, CustomTypes: []string{"yaml-kind"}, InfraSet: map[string]bool{"yaml-infra": true}}
			if got := issueListPolicyInTx(t, ctx, s); !reflect.DeepEqual(got, want) {
				t.Fatalf("YAML fallback: got=%+v want=%+v", got, want)
			}

			// Existing low-level config API without normalized-table population:
			// prove the supported config-string fallback before table-first policy.
			if err := s.withTx(ctx, true, func(tx *sql.Tx) error {
				for key, value := range map[string]string{"status.custom": "db-active:active", "types.custom": `["db-kind"]`, "types.infra": "db-infra"} {
					if err := issueops.SetConfigInTx(ctx, tx, key, value); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			want = workapi.ListConfig{CustomStatuses: []types.CustomStatus{{Name: "db-active", Category: types.CategoryActive}}, CustomTypes: []string{"db-kind"}, InfraSet: map[string]bool{"db-infra": true}}
			if got := issueListPolicyInTx(t, ctx, s); !reflect.DeepEqual(got, want) {
				t.Fatalf("config before YAML: got=%+v want=%+v", got, want)
			}

			setIssueListConfig(t, ctx, s, "status.custom", "table-active:active")
			setIssueListConfig(t, ctx, s, "types.custom", "table-kind")
			want.CustomStatuses = []types.CustomStatus{{Name: "table-active", Category: types.CategoryActive}}
			want.CustomTypes = []string{"table-kind"}
			// If populated normalized tables are authoritative, unsupported huge
			// YAML fallbacks are not resolved or charged as effective policy.
			config.Set("status.custom", names("ignoredstatus", 257))
			config.Set("types.custom", names("ignoredkind", 257))
			config.Set("types.infra", names("ignoredinfra", 257))
			if got := issueListPolicyInTx(t, ctx, s); !reflect.DeepEqual(got, want) {
				t.Fatalf("normalized precedence: got=%+v want=%+v", got, want)
			}
			stable := reopenState(t, ctx, s)
			page, err := s.ListIssues(ctx, publicops.ListRequest{})
			if err != nil || len(page.Items) != 1 || page.Items[0].Version != created.Version {
				t.Fatalf("effective DB policy: %+v %v", page, err)
			}
			if after := reopenState(t, ctx, s); !reflect.DeepEqual(stable, after) {
				t.Fatal("policy query mutated state")
			}
			assertIssueListRetained(t, ctx, s, "beads/policy", created)
		})
	}
}
