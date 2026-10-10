package graphstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	graph "github.com/steveyegge/beads/graphops"
)

// These are real installed informational Types, not aliases for RelatedTypeURL.
// The identities and original four descriptors remain immutable.
func ExampleFollowsTypeURL(scope string) string { return scope + "types/example-follows" }
func ExampleCitesTypeURL(scope string) string   { return scope + "types/example-cites" }

func IsInformationalTypeURL(scope, id string) bool {
	return id == RelatedTypeURL(scope) || id == ExampleFollowsTypeURL(scope) || id == ExampleCitesTypeURL(scope)
}

type previewTypeDefinition struct {
	name  string
	build func(string) (graph.TypeDescriptor, error)
}

// The original four entries are a complete legacy installation. Examples are
// installed together only in new workspaces; reads never upgrade an old store.
func previewTypeDefinitions() []previewTypeDefinition {
	return []previewTypeDefinition{
		{"memory", memoryDescriptor}, {"issue", issueDescriptor},
		{"dependency", dependencyDescriptor}, {"related", relatedDescriptor},
		{"example-follows", exampleFollowsDescriptor}, {"example-cites", exampleCitesDescriptor},
	}
}

// FreshTypeNames lists, in installation order, the Types a fresh init installs.
// The workspace format generation recorded by the CLI must change whenever this
// list does, so the CLI checks the generation against it.
func FreshTypeNames() []string {
	definitions := previewTypeDefinitions()
	names := make([]string, len(definitions))
	for i, definition := range definitions {
		names[i] = definition.name
	}
	return names
}

func exampleFollowsDescriptor(scope string) (graph.TypeDescriptor, error) {
	return exampleInformationalDescriptor(ExampleFollowsTypeURL(scope), "Example follows", "The source follows a policy or model described by the target.")
}

func exampleCitesDescriptor(scope string) (graph.TypeDescriptor, error) {
	return exampleInformationalDescriptor(ExampleCitesTypeURL(scope), "Example cites", "The source cites the target as supporting context.")
}

func exampleInformationalDescriptor(id, name, meaning string) (graph.TypeDescriptor, error) {
	// conformsTo lists are conjunctions, not alternative Memory/Issue Types.
	endpoint, err := graph.NewEndpointConstraint(nil, graph.ExternalNone)
	if err != nil {
		return graph.TypeDescriptor{}, err
	}
	return graph.NewTypeDescriptor(graph.TypeDescriptorSpec{
		ID: id, Name: name, Describes: graph.KindLink,
		Description: meaning + " Informational example with live unpinned local Issue or Memory endpoints and independent multiedge identity. Closed properties admit only an optional UTF-8 string note. Memory sources own this Link; Issue sources do not. No scheduling effect.",
		Source:      &endpoint, Target: &endpoint,
	})
}

// installedPreviewTypes admits exactly the original four or all six immutable
// descriptors. Count and SQL byte-length guards bound metadata acquisition and
// reject partial, unknown, replaced or oversized installations.
func installedPreviewTypes(ctx context.Context, tx *sql.Tx, scope string) ([]graph.TypeDescriptor, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_preview_types`).Scan(&count); err != nil {
		return nil, err
	}
	definitions := previewTypeDefinitions()
	if count != 4 && count != len(definitions) {
		return nil, fmt.Errorf("%w: unsupported Type installation", ErrInvalidStore)
	}
	result := make([]graph.TypeDescriptor, 0, count)
	for _, definition := range definitions[:count] {
		expected, err := definition.build(scope)
		if err != nil {
			return nil, err
		}
		var raw []byte
		var fingerprint string
		if err := tx.QueryRowContext(ctx, `SELECT descriptor, fingerprint FROM graph_preview_types WHERE name=? AND OCTET_LENGTH(descriptor)=?`, definition.name, len(expected.CanonicalJSON())).Scan(&raw, &fingerprint); err != nil {
			return nil, fmt.Errorf("%w: read %s descriptor: %v", ErrInvalidStore, definition.name, err)
		}
		parsed, err := graph.ParseTypeDescriptor(raw)
		if err != nil || !bytes.Equal(raw, expected.CanonicalJSON()) || fingerprint != expected.Fingerprint() || fingerprint != parsed.Fingerprint() {
			return nil, fmt.Errorf("%w: %s descriptor differs", ErrInvalidStore, definition.name)
		}
		result = append(result, parsed)
	}
	return result, nil
}

func (s *Store) informationalTypeInTx(ctx context.Context, tx *sql.Tx, id string) error {
	if !IsInformationalTypeURL(s.ScopeURL(), id) {
		return fmt.Errorf("%w: invalid informational Type", ErrInvalidStore)
	}
	if _, err := s.readTypeInTx(ctx, tx, id); err != nil {
		return fmt.Errorf("%w: informational Type is not installed: %v", ErrInvalidStore, err)
	}
	return nil
}

// Keep collection integrity based on the installed set, not every Type the
// binary knows. A legacy store cannot silently acquire an example allocation.
func (s *Store) installedInformationalTypes(ctx context.Context, tx *sql.Tx) ([]string, error) {
	descriptors, err := installedPreviewTypes(ctx, tx, s.ScopeURL())
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, descriptor := range descriptors {
		if IsInformationalTypeURL(s.ScopeURL(), descriptor.ID()) {
			ids = append(ids, descriptor.ID())
		}
	}
	return ids, nil
}

func informationalTypeSQL(ids []string) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","), args
}

// User selection of a supported but absent example is unavailable, whereas an
// authoritative record referring to that absent Type is corrupt store state.
func (s *Store) selectedLinkTypeInTx(ctx context.Context, tx *sql.Tx, id string) error {
	if _, err := s.readTypeInTx(ctx, tx, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: example Link Types require a freshly initialized graph workspace", ErrCapabilityUnavailable)
		}
		return err
	}
	return nil
}
