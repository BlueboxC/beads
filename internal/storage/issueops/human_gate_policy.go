package issueops

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/journalops"
)

const HumanGateResolversKey = "gates.human.resolvers"

// A caller-asserted allowlist prevents accidental self-approval. It does not
// authenticate a person; the caller must control config, SQL and actor access.
func requireHumanGateActor(ctx context.Context, raw, gate, actor string) error {
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil || names == nil {
		return fmt.Errorf("%w: human gate %s: %s must be a JSON array of nonempty actor names", storage.ErrValidation, gate, HumanGateResolversKey)
	}
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: human gate %s: %s contains an empty actor name", storage.ErrValidation, gate, HumanGateResolversKey)
		}
	}
	source := journalops.ActorSource(ctx, actor)
	if actor != "" && slices.Contains(names, actor) && (source == "flag" || source == "env" || source == "provided") {
		return nil
	}
	return fmt.Errorf("%w: human gate %s requires an explicitly supplied actor listed in %s (actor %q, source %q); --force does not waive this policy", storage.ErrValidation, gate, HumanGateResolversKey, actor, source)
}

func humanGatePolicyInTx(ctx context.Context, tx DBTX) (string, bool, error) {
	values, err := getConfigKeysInTx(ctx, tx, HumanGateResolversKey)
	if err != nil {
		return "", false, fmt.Errorf("read human gate policy: %w", err)
	}
	value, present := values[HumanGateResolversKey]
	return value, present, nil
}

// enforceHumanGateIDsInTx probes only the selected ids, including ephemeral
// gates. AwaitType survives an issue-type edit, so it remains the discriminator.
func enforceHumanGateIDsInTx(ctx context.Context, tx DBTX, raw string, ids []string, actor string, openOnly bool) error {
	for start := 0; start < len(ids); start += deleteBatchSize {
		end := min(start+deleteBatchSize, len(ids))
		in, args := buildSQLInClause(ids[start:end])
		live := ""
		if openOnly {
			live = " AND status != 'closed'"
		}
		for _, table := range []string{"issues", "wisps"} {
			//nolint:gosec // G201: table and live are fixed fragments; in contains only ? placeholders.
			rows, err := tx.QueryContext(ctx, "SELECT id FROM "+table+" WHERE await_type = 'human' AND id IN ("+in+")"+live+" ORDER BY id", args...)
			if err != nil {
				if optionalBlockedTable(table) && isTableNotExistError(err) {
					continue
				}
				return fmt.Errorf("read protected human gates: %w", err)
			}
			var gates []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					_ = rows.Close()
					return err
				}
				gates = append(gates, id)
			}
			readErr := rows.Err()
			closeErr := rows.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
			for _, gate := range gates {
				if err := requireHumanGateActor(ctx, raw, gate, actor); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func EnforceHumanGateMutationInTx(ctx context.Context, tx DBTX, id, actor string) error {
	raw, on, err := humanGatePolicyInTx(ctx, tx)
	if err != nil || !on {
		return err
	}
	return enforceHumanGateIDsInTx(ctx, tx, raw, []string{id}, actor, false)
}

// Closing a directly gated task with force is an approval bypass too. Protect
// live direct blockers even if their deferred status masks them from ready.
func EnforceHumanGateCloseInTx(ctx context.Context, tx DBTX, id, actor string) error {
	raw, on, err := humanGatePolicyInTx(ctx, tx)
	if err != nil || !on {
		return err
	}
	if err := enforceHumanGateIDsInTx(ctx, tx, raw, []string{id}, actor, false); err != nil {
		return err
	}
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		//nolint:gosec // G201: table comes from the constant list; DepTargetExpr is a fixed expression.
		rows, err := tx.QueryContext(ctx, "SELECT "+DepTargetExpr+" FROM "+table+" WHERE issue_id = ? AND type IN ('blocks','waits-for','conditional-blocks')", id)
		if err != nil {
			if optionalBlockedTable(table) && isTableNotExistError(err) {
				continue
			}
			return err
		}
		var targets []string
		for rows.Next() {
			var target string
			if err := rows.Scan(&target); err != nil {
				_ = rows.Close()
				return err
			}
			targets = append(targets, target)
		}
		readErr := rows.Err()
		closeErr := rows.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := enforceHumanGateIDsInTx(ctx, tx, raw, targets, actor, true); err != nil {
			return err
		}
	}
	return nil
}

func EnforceHumanGateUpdateInTx(ctx context.Context, tx DBTX, current *types.Issue, updates map[string]any, actor string) error {
	if current.AwaitType != "human" {
		return nil
	}
	for _, field := range []string{"status", "issue_type", "defer_until", "pinned", "wisp", "no_history", "await_type"} {
		if _, changing := updates[field]; changing {
			return EnforceHumanGateMutationInTx(ctx, tx, current.ID, actor)
		}
	}
	return nil
}

func EnforceHumanGateEdgeInTx(ctx context.Context, tx DBTX, target string, kind types.DependencyType, actor string) error {
	if !kind.IsBlockingEdge() && kind != types.DepParentChild {
		return nil
	}
	return EnforceHumanGateMutationInTx(ctx, tx, target, actor)
}

// Deletion checks the expanded cascade set and its disappearing edges before
// writing. No journal setting, force flag or actorless system delete waives it.
func EnforceHumanGateDeletionInTx(ctx context.Context, tx DBTX, ids []string, actor string) error {
	if len(ids) == 0 {
		return nil
	}
	raw, on, err := humanGatePolicyInTx(ctx, tx)
	if err != nil || !on {
		return err
	}
	if err := enforceHumanGateIDsInTx(ctx, tx, raw, ids, actor, false); err != nil {
		return err
	}
	edges, err := dependencyEdgesForIssueIDsInTx(ctx, tx, ids)
	if err != nil {
		return err
	}
	targets := map[string]bool{}
	for _, edge := range edges {
		kind := types.DependencyType(edge.kind)
		if kind.IsBlockingEdge() || kind == types.DepParentChild {
			targets[edge.target] = true
		}
	}
	var protectedTargets []string
	for target := range targets {
		protectedTargets = append(protectedTargets, target)
	}
	slices.Sort(protectedTargets)
	return enforceHumanGateIDsInTx(ctx, tx, raw, protectedTargets, actor, false)
}
