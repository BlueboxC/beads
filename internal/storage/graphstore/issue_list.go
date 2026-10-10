package graphstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// These are acquisition limits for the preview's database-backed list policy,
// not new configuration or schema constraints. Workspace YAML is loaded by the
// existing frontend configuration machinery before this adapter runs.
const (
	PreviewIssueListRequestByteLimit = 64 << 10
	PreviewIssueListLabelLimit       = 256
	PreviewIssueListConfigByteLimit  = 64 << 10
	PreviewIssueListConfigRowLimit   = 256
)

// IssueListPage contains complete canonical Issue records. HasMore reports an
// intentional page limit, not a snapshot cursor or a complete BDP collection.
// It does not promise the legacy IssueWithCounts JSON representation.
type IssueListPage struct {
	Items   []IssueRecord `json:"items"`
	HasMore bool          `json:"hasMore"`
}

// ListIssues uses the existing Issue query policy inside the same read
// transaction as authority checks and complete canonical record validation.
// No legacy store, advisory wakeup, mutation or cross-request cursor is opened.
func (s *Store) ListIssues(ctx context.Context, request publicops.ListRequest) (IssueListPage, error) {
	request, err := prepareIssueListRequest(request)
	if err != nil {
		return IssueListPage{}, err
	}
	var result IssueListPage
	err = s.withTx(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = s.listIssuesInTx(ctx, tx, request)
		return err
	})
	if err != nil {
		return IssueListPage{}, err
	}
	return result, nil
}

func prepareIssueListRequest(in publicops.ListRequest) (publicops.ListRequest, error) {
	allowed := publicops.ListRequest{
		Status: in.Status, IssueType: in.IssueType, TitleSearch: in.TitleSearch, TitleContains: in.TitleContains,
		Assignee: in.Assignee, NoAssignee: in.NoAssignee,
		DueBefore: in.DueBefore, DueAfter: in.DueAfter, OverdueFlag: in.OverdueFlag,
		Labels: in.Labels, LabelsAny: in.LabelsAny, ExcludeLabels: in.ExcludeLabels,
		Priority: in.Priority, PriorityMin: in.PriorityMin, PriorityMax: in.PriorityMax,
		PinnedFlag: in.PinnedFlag, NoPinnedFlag: in.NoPinnedFlag, AllFlag: in.AllFlag,
		SortBy: in.SortBy, Reverse: in.Reverse, Limit: in.Limit, MaxRows: in.MaxRows, MaxRowsSource: in.MaxRowsSource,
	}
	if !reflect.DeepEqual(in, allowed) {
		return publicops.ListRequest{}, fmt.Errorf("%w: unsupported graph Issue list option", ErrCapabilityUnavailable)
	}
	switch in.SortBy {
	case "", "priority", "created", "updated", "title", "status", "type":
	default:
		return publicops.ListRequest{}, fmt.Errorf("%w: unsupported graph Issue list sort", ErrCapabilityUnavailable)
	}
	if in.PinnedFlag && in.NoPinnedFlag {
		return publicops.ListRequest{}, fmt.Errorf("%w: pinned and no-pinned are mutually exclusive", storage.ErrValidation)
	}
	if (in.Limit != nil && *in.Limit < 0) || in.MaxRows < 0 {
		return publicops.ListRequest{}, fmt.Errorf("%w: list limits cannot be negative", storage.ErrValidation)
	}
	for _, p := range []*int{in.Priority, in.PriorityMin, in.PriorityMax} {
		if p != nil && (*p < 0 || *p > 4) {
			return publicops.ListRequest{}, fmt.Errorf("%w: priority must be between 0 and 4", storage.ErrValidation)
		}
	}
	requestBytes := 0
	for _, value := range []string{in.Status, in.IssueType, in.TitleSearch, in.TitleContains, in.MaxRowsSource, in.Assignee} {
		if len(value) > PreviewIssueListRequestByteLimit-requestBytes {
			return publicops.ListRequest{}, fmt.Errorf("%w: Issue list request text exceeds %d bytes", storage.ErrValidation, PreviewIssueListRequestByteLimit)
		}
		requestBytes += len(value)
		if !utf8.ValidString(value) {
			return publicops.ListRequest{}, fmt.Errorf("%w: Issue list text must be UTF-8", storage.ErrValidation)
		}
	}
	labelCount := 0
	for _, labels := range [][]string{in.Labels, in.LabelsAny, in.ExcludeLabels} {
		if len(labels) > PreviewIssueListLabelLimit-labelCount {
			return publicops.ListRequest{}, fmt.Errorf("%w: Issue list request exceeds %d labels", storage.ErrValidation, PreviewIssueListLabelLimit)
		}
		labelCount += len(labels)
		for _, label := range labels {
			if len(label) > PreviewIssueListRequestByteLimit-requestBytes {
				return publicops.ListRequest{}, fmt.Errorf("%w: Issue list request text exceeds %d bytes", storage.ErrValidation, PreviewIssueListRequestByteLimit)
			}
			requestBytes += len(label)
			if !utf8.ValidString(label) {
				return publicops.ListRequest{}, fmt.Errorf("%w: Issue list labels must be UTF-8", storage.ErrValidation)
			}
		}
	}
	copyInt := func(value *int) *int {
		if value == nil {
			return nil
		}
		copied := *value
		return &copied
	}
	allowed.Priority, allowed.PriorityMin, allowed.PriorityMax = copyInt(in.Priority), copyInt(in.PriorityMin), copyInt(in.PriorityMax)
	allowed.Limit = copyInt(in.Limit)
	allowed.Labels, allowed.LabelsAny, allowed.ExcludeLabels = slices.Clone(in.Labels), slices.Clone(in.LabelsAny), slices.Clone(in.ExcludeLabels)
	// Native query binds strict bounds as whole-second RFC3339 strings. Preserve
	// that truncation (rather than write rounding), but normalize API offsets
	// before formatting so comparisons use the same UTC instant as stored dates.
	for _, field := range []**time.Time{&allowed.DueBefore, &allowed.DueAfter} {
		if *field != nil {
			value := (*field).UTC().Truncate(time.Second)
			if value.Year() < 1 || value.Year() > 9999 {
				return publicops.ListRequest{}, fmt.Errorf("%w: Issue due filter year must be between 1 and 9999", storage.ErrValidation)
			}
			*field = &value
		}
	}
	return allowed, nil
}

func (s *Store) listIssuesInTx(ctx context.Context, tx *sql.Tx, request publicops.ListRequest) (IssueListPage, error) {
	if err := checkCurrentReadBytes(ctx, tx); err != nil {
		return IssueListPage{}, err
	}
	if err := checkBinding(ctx, tx, s.options); err != nil {
		return IssueListPage{}, err
	}
	if err := s.checkCollectionMappingsInTx(ctx, tx); err != nil {
		return IssueListPage{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_catalog WHERE allocation_state='live'").Scan(&count); err != nil {
		return IssueListPage{}, err
	}
	if count > PreviewSnapshotLimit {
		return IssueListPage{}, fmt.Errorf("%w: graph Issue list exceeds %d live Resources before filtering", ErrLimitExceeded, PreviewSnapshotLimit)
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_preview_catalog WHERE allocation_state NOT IN ('live','deleted')").Scan(&count); err != nil {
		return IssueListPage{}, err
	}
	if count != 0 {
		return IssueListPage{}, fmt.Errorf("%w: unknown allocation state", ErrInvalidStore)
	}
	if err := s.checkIssueListCatalogInTx(ctx, tx); err != nil {
		return IssueListPage{}, err
	}
	cfg, err := issueListConfigInTx(ctx, tx)
	if err != nil {
		return IssueListPage{}, err
	}
	if request.IssueType != "" && cfg.IsInfra(request.IssueType) {
		return IssueListPage{}, fmt.Errorf("%w: graph Issue list does not admit infrastructure or wisp scope", ErrCapabilityUnavailable)
	}
	filter, err := workapi.BuildListFilter(request, cfg)
	if err != nil {
		return IssueListPage{}, fmt.Errorf("%w: %v", storage.ErrValidation, err)
	}
	if !filter.SkipWisps || (filter.Ephemeral != nil && *filter.Ephemeral) {
		return IssueListPage{}, fmt.Errorf("%w: graph Issue list requires durable scope", ErrCapabilityUnavailable)
	}
	// Counts are not in this result's projection. Labels and complete Issue
	// properties remain present; callers cannot request partial graph records.
	filter.SkipCounts = true
	// Every admitted workspace has <=1000 live Resources. Bounding an enormous
	// requested SQL limit here cannot discard an admitted row, and prevents the
	// shared probe-row increment from overflowing an arbitrary caller's int.
	if filter.Limit > PreviewSnapshotLimit {
		filter.Limit = PreviewSnapshotLimit
	}
	filter = workapi.WithFetchOneExtra(workapi.WithRowsBeforeThePage(filter, request.Offset))
	rows, err := issueops.SearchIssuesWithCountsInTx(ctx, tx, "", filter)
	if err != nil {
		var capError *issueops.ErrTooManyRows
		if errors.As(err, &capError) {
			return IssueListPage{}, fmt.Errorf("%w: %w", ErrLimitExceeded, err)
		}
		return IssueListPage{}, err
	}
	records := make(map[string]IssueRecord, len(rows))
	for _, row := range rows {
		if row == nil || row.Issue == nil {
			return IssueListPage{}, fmt.Errorf("%w: nil Issue query row", ErrInvalidStore)
		}
		if _, duplicate := records[row.ID]; duplicate {
			return IssueListPage{}, fmt.Errorf("%w: duplicate Issue query row", ErrInvalidStore)
		}
		var path string
		if err := tx.QueryRowContext(ctx, "SELECT path FROM graph_preview_catalog WHERE backing='issue' AND backing_key=? AND allocation_state='live'", row.ID).Scan(&path); err != nil {
			return IssueListPage{}, fmt.Errorf("%w: unmapped listed Issue: %v", ErrInvalidStore, err)
		}
		if err := validatePath(path); err != nil {
			return IssueListPage{}, fmt.Errorf("%w: malformed Issue allocation path: %v", ErrInvalidStore, err)
		}
		record, err := s.showIssueInTx(ctx, tx, path)
		if err != nil {
			return IssueListPage{}, err
		}
		records[row.ID] = record
	}
	// Validate the over-fetched probe before trimming it: HasMore must never
	// conceal a malformed queried record. Preserve the shared query/page order.
	rows, more := workapi.FinishPageAt(rows, request.SortBy, request.Reverse, request.Offset, workapi.PageLimit(request), false)
	result := IssueListPage{Items: make([]IssueRecord, 0, len(rows)), HasMore: more}
	for _, row := range rows {
		result.Items = append(result.Items, records[row.ID])
	}
	return result, nil
}

type issueListConfigSource struct{ config workapi.ListConfig }

func (s issueListConfigSource) GetCustomStatuses(context.Context) ([]types.CustomStatus, error) {
	return s.config.CustomStatuses, nil
}
func (s issueListConfigSource) GetCustomTypes(context.Context) ([]string, error) {
	return s.config.CustomTypes, nil
}
func (s issueListConfigSource) GetInfraTypes(context.Context) (map[string]bool, error) {
	return s.config.InfraSet, nil
}

func issueListConfigInTx(ctx context.Context, tx *sql.Tx) (workapi.ListConfig, error) {
	var totalRows, totalBytes uint64
	// Package-owned SQL only. Clamp lengths in SQL before reading scalar counts;
	// do not acquire a corrupt huge config value and check its size afterwards.
	for _, query := range []string{
		"SELECT COUNT(*), CAST(LEAST(COALESCE(SUM(OCTET_LENGTH(name)+OCTET_LENGTH(category)),0),?) AS UNSIGNED) FROM custom_statuses",
		"SELECT COUNT(*), CAST(LEAST(COALESCE(SUM(OCTET_LENGTH(name)),0),?) AS UNSIGNED) FROM custom_types",
		"SELECT COUNT(*), CAST(LEAST(COALESCE(SUM(OCTET_LENGTH(`key`)+OCTET_LENGTH(value)),0),?) AS UNSIGNED) FROM config WHERE `key` IN ('status.custom','types.custom','types.infra')",
	} {
		var rows, size uint64
		if err := tx.QueryRowContext(ctx, query, PreviewIssueListConfigByteLimit+1).Scan(&rows, &size); err != nil {
			return workapi.ListConfig{}, err
		}
		if rows > PreviewIssueListConfigRowLimit-totalRows || size > PreviewIssueListConfigByteLimit-totalBytes {
			return workapi.ListConfig{}, fmt.Errorf("%w: graph Issue list configuration exceeds %d rows or %d bytes", ErrLimitExceeded, PreviewIssueListConfigRowLimit, PreviewIssueListConfigByteLimit)
		}
		totalRows, totalBytes = totalRows+rows, totalBytes+size
	}
	statuses, customTypes, err := issueops.ResolveCustomConfigStrictInTx(ctx, tx)
	if err != nil {
		return workapi.ListConfig{}, err
	}
	infra, err := issueops.ResolveInfraTypesStrictInTx(ctx, tx)
	if err != nil {
		return workapi.ListConfig{}, err
	}
	cfg, err := workapi.LoadListConfig(ctx, issueListConfigSource{workapi.ListConfig{CustomStatuses: statuses, CustomTypes: customTypes, InfraSet: infra}})
	if err != nil {
		return workapi.ListConfig{}, err
	}
	if err := checkResolvedIssueListConfig(cfg); err != nil {
		return workapi.ListConfig{}, err
	}
	return cfg, nil
}

// Catalog admission covers every live allocation, including filtered-out Issues
// and non-Issue records. It reads metadata/existence only: complete payload and
// retained-state verification remains scoped to queried Issues and their owned
// Links, including the query's probe row.
func (s *Store) checkIssueListCatalogInTx(ctx context.Context, tx *sql.Tx) error {
	ids, err := s.installedInformationalTypes(ctx, tx)
	if err != nil {
		return err
	}
	informational := make(map[string]bool, len(ids))
	for _, id := range ids {
		informational[id] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.path,c.resource_kind,c.type_url,c.revision,c.backing,c.backing_key,
 p.path IS NOT NULL,i.id IS NOT NULL,d.id IS NOT NULL,l.path IS NOT NULL
 FROM graph_preview_catalog c
 LEFT JOIN graph_preview_payloads p ON p.path=c.path
 LEFT JOIN issues i ON c.backing='issue' AND i.id=c.backing_key
 LEFT JOIN dependencies d ON c.backing='dependency' AND d.id=c.backing_key
 LEFT JOIN graph_preview_links l ON l.path=c.path
 WHERE c.allocation_state='live' LIMIT ?`, PreviewSnapshotLimit+1)
	if err != nil {
		return err
	}
	var readErr error
	count := 0
	for rows.Next() {
		var path, kind, typ, revision, backing string
		var key sql.NullString
		var memoryExists, issueExists, dependencyExists, informationalExists bool
		if err := rows.Scan(&path, &kind, &typ, &revision, &backing, &key, &memoryExists, &issueExists, &dependencyExists, &informationalExists); err != nil {
			readErr = err
			break
		}
		count++
		if count > PreviewSnapshotLimit {
			readErr = fmt.Errorf("%w: graph Issue list live Resource limit exceeded", ErrLimitExceeded)
			break
		}
		valid := validateResourcePath(path) == nil && authorityID.MatchString(revision)
		switch backing {
		case "generic":
			valid = valid && kind == "bead" && strings.HasPrefix(path, "beads/") && typ == MemoryTypeURL(s.ScopeURL()) && !key.Valid && memoryExists
		case "issue":
			valid = valid && kind == "bead" && strings.HasPrefix(path, "beads/") && typ == IssueTypeURL(s.ScopeURL()) && key.Valid && key.String != "" && issueExists
		case "dependency":
			valid = valid && kind == "link" && strings.HasPrefix(path, "links/") && typ == DependencyTypeURL(s.ScopeURL()) && key.Valid && key.String != "" && dependencyExists
		case "informational":
			valid = valid && kind == "link" && strings.HasPrefix(path, "links/") && informational[typ] && !key.Valid && informationalExists
		default:
			valid = false
		}
		if !valid {
			readErr = fmt.Errorf("%w: invalid live graph Issue list allocation", ErrInvalidStore)
			break
		}
	}
	return errors.Join(readErr, rows.Err(), rows.Close())
}

// checkResolvedIssueListConfig also charges frontend YAML fallbacks. This is a
// resolved-policy bound, not a limit on the frontend's earlier YAML acquisition.
// Each stored entry is charged, including duplicates retained in slices.
func checkResolvedIssueListConfig(cfg workapi.ListConfig) error {
	entries, bytes := 0, 0
	charge := func(parts ...string) error {
		if entries == PreviewIssueListConfigRowLimit {
			return fmt.Errorf("%w: resolved Issue list configuration exceeds %d entries", ErrLimitExceeded, PreviewIssueListConfigRowLimit)
		}
		entries++
		for _, part := range parts {
			if len(part) > PreviewIssueListConfigByteLimit-bytes {
				return fmt.Errorf("%w: resolved Issue list configuration exceeds %d bytes", ErrLimitExceeded, PreviewIssueListConfigByteLimit)
			}
			bytes += len(part)
		}
		return nil
	}
	for _, status := range cfg.CustomStatuses {
		if err := charge(status.Name, string(status.Category)); err != nil {
			return err
		}
	}
	for _, name := range cfg.CustomTypes {
		if err := charge(name); err != nil {
			return err
		}
	}
	for name := range cfg.InfraSet {
		if err := charge(name); err != nil {
			return err
		}
	}
	return nil
}
