package graphstore

import (
	"context"
	"database/sql"
)

// VersionPair contains two complete retained records of the same subject.
// Direction is caller supplied; tokens establish no chronological order.
type VersionPair struct {
	From any
	To   any
}

// ReadVersionPair resolves both operands in one authority-checked transaction.
// Each retained snapshot acquisition has a 16 MiB input budget. A deleted
// Memory operand also validates its final live snapshot before reading an older
// selected snapshot: two distinct operands can acquire up to 64 MiB in total.
// This excludes decoding/output overhead. Equal tokens are resolved once, and
// both fields share that accepted value. Any
// error returns a zero pair, never a partial comparison input.
func (s *Store) ReadVersionPair(ctx context.Context, path, from, to string) (VersionPair, error) {
	if err := validateResourcePath(path); err != nil {
		return VersionPair{}, err
	}
	for _, token := range []string{from, to} {
		if err := validateVersionToken(token); err != nil {
			return VersionPair{}, err
		}
	}
	var result VersionPair
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		if err := checkBinding(ctx, tx, s.options); err != nil {
			return err
		}
		var err error
		result.From, err = s.readVersionInTx(ctx, tx, path, from)
		if err != nil {
			return err
		}
		if from == to {
			result.To = result.From
			return nil
		}
		result.To, err = s.readVersionInTx(ctx, tx, path, to)
		return err
	})
	if err != nil {
		return VersionPair{}, err
	}
	return result, nil
}
