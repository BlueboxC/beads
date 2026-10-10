package issueops

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/gowebpki/jcs"

	"github.com/steveyegge/beads/internal/types"
)

// Native Issue retention adapted from versioned-beads/beads; provenance is in
// graphops/PROVENANCE.md. In this fork, graphstore activates it per transaction
// and records once after the final successful native mutation. Ordinary stores
// do not enable it or acquire new schema columns.
//
// Snapshots are canonical RFC 8785 bytes in LONGBLOB. Unrepresentable numbers
// fail the writing transaction. Wisps are excluded; a create declares history
// participation, and update-shaped mints skip undeclared rows.
//
// Ordinals are local bookkeeping, not portable graph version addresses. Graph
// writes serialize through their coordination row and map snapshots to opaque
// tokens in the same transaction. Independent graph-store clone merging is not
// qualified. Fresh graph Init provisions the prerequisites; reads never migrate.

var versionedHistoryTransactions sync.Map // map[DBTX]bool; entries live for one transaction

// ScopeVersionedHistoryTransaction associates versioned-history activation
// with one concrete transaction and returns a cleanup function. Store
// implementations call it immediately after BeginTx, alongside (not instead
// of) ScopeEventsJournalTransaction. This is instance/project scoped even
// when many stores share a process; there is no process-wide activation
// switch.
func ScopeVersionedHistoryTransaction(tx DBTX, enabled bool) func() {
	if tx == nil {
		return func() {}
	}
	versionedHistoryTransactions.Store(tx, enabled)
	return func() { versionedHistoryTransactions.Delete(tx) }
}

func versionedHistoryEnabled(tx DBTX) bool {
	enabled, _ := versionedHistoryTransactions.Load(tx)
	on, _ := enabled.(bool)
	return on
}

// VersionedHistoryStagedTables names the retained-history tables and the
// revision-bearing Issue table. Graphstore commits them with the graph mapping
// in the same Dolt transaction; ordinary stores never enable this recorder.
func VersionedHistoryStagedTables() []string {
	return []string{"issue_versions", "store_epoch", "issues"}
}

// Attribution reflects a writer-supplied actor claim, never authenticated identity.
const (
	attributionStatusClaimed = "claimed"
	attributionStatusUnknown = "unknown"
)

// attributionStatusForActor derives issue_versions.attribution_status from
// the same actor string every RecordVersionInTx call site already passes: a
// non-empty actor is "claimed", an empty one is "unknown". There is no
// third value — an empty actor means the path had no identity to assert,
// which is exactly what "unknown" says; any stronger reading (a deliberate,
// confirmed absence of attribution) is a claim no call site makes.
func attributionStatusForActor(actor string) string {
	if actor == "" {
		return attributionStatusUnknown
	}
	return attributionStatusClaimed
}

// canonicalDurableState renders issue as the bytes RecordVersionInTx stores
// in issue_versions.durable_state: encoding/json's marshal of the issue,
// canonicalized per RFC 8785 (JCS). The canonical form is a function of the
// issue's content alone -- keys sorted, numbers in their one ES6 form
// (1.0 is 1, 1e300 is 1e+300), only the escapes RFC 8785 requires -- so the
// same issue state always yields the same bytes, whatever encoding/json's
// formatting happens to be. Numbers are canonicalized as IEEE-754 doubles
// (RFC 8785 section 3.2.2.3), so canonicalization alone would round a number
// past 2^53 here, once, by the writer, before anything hashes it; the admission
// gate (refuseUnrepresentableIntegers) refuses such a number instead, so the
// stored bytes and the hashed bytes are the same bytes and nothing is rounded
// silently. Two fields of types.Issue can reach that magnitude: Metadata, and
// Timeout, a time.Duration that encoding/json writes as a count of nanoseconds,
// so a gate timeout above 2^53-1 ns (about 104.25 days) is refused. Its other
// integers are priorities, ordinals and counts held in 32-bit columns.
//
// A refusal names the top-level field it was found in (nameRefusedField). The
// success path reads the document once; the field is located only after a
// refusal.
//
// Two properties worth stating because a hand-rolled canonicalizer would get
// them wrong. First, jcs.Transform normalizes away encoding/json's HTML
// escaping of <, > and &, so the token is a function of content, not of
// Go's escaping policy. Second, a snapshot that cannot be canonicalized
// deliberately FAILS the mutation: types.Issue.Metadata is a json.RawMessage
// passed through verbatim, RFC 8785 rejects duplicate keys, and this seam
// returns the error to its caller, which aborts the transaction. A backend that
// keeps duplicate keys in a metadata document would therefore let an issue
// mutate with the flag off and make it unmutatable with it on, if it participates
// in history; a legacy row stays writable, because the fence skips it before this
// function runs. Dolt does not keep them (see the note above), so on a Dolt
// store this refusal is never reached. Fail-closed is the policy for a backend
// that does -- bytes that could canonicalize two ways would not be a content
// token -- and whether to normalize such metadata first, or to find such rows with
// bd doctor, is gastownhall/beads#6379 (item 3).
func canonicalDurableState(issue any) ([]byte, error) {
	marshaled, err := json.Marshal(issue)
	if err != nil {
		return nil, err
	}
	if err := refuseUnrepresentableIntegers(marshaled); err != nil {
		return nil, nameRefusedField(marshaled, err)
	}
	canonical, err := jcsCanonicalize(marshaled)
	if err != nil {
		return nil, nameRefusedField(marshaled, err)
	}
	return canonical, nil
}

// refusedFieldError is a refusal from canonicalDurableState that has been traced
// to the top-level field of the issue it was found in. It unwraps to the refusal
// itself, so errors.Is(err, ErrIntegerNotRepresentable) still answers.
type refusedFieldError struct {
	field string
	err   error
}

func (e *refusedFieldError) Error() string { return fmt.Sprintf("%v (field %q)", e.err, e.field) }

func (e *refusedFieldError) Unwrap() error { return e.err }

// nameRefusedField finds the top-level field of marshaled, one issue as a JSON
// object, that the two checks canonicalDurableState runs refuse on their own, and
// returns err naming it. It runs only after a refusal, so what a successful
// canonicalization costs does not change. A document it cannot attribute, one
// that is not an object or in which no single field is refused alone, comes back
// unchanged.
func nameRefusedField(marshaled []byte, err error) error {
	dec := json.NewDecoder(bytes.NewReader(marshaled))
	if open, derr := dec.Token(); derr != nil || open != json.Delim('{') {
		return err
	}
	for dec.More() {
		keyToken, derr := dec.Token()
		key, isString := keyToken.(string)
		if derr != nil || !isString {
			return err
		}
		var value json.RawMessage
		if derr := dec.Decode(&value); derr != nil {
			return err
		}
		if refuseUnrepresentableIntegers(value) != nil {
			return &refusedFieldError{field: key, err: err}
		}
		if _, jerr := jcsCanonicalize(value); jerr != nil {
			return &refusedFieldError{field: key, err: err}
		}
	}
	return err
}

// jcsCanonicalize is canonicalDurableState's RFC 8785 (JCS) step alone, below
// the admission gate rather than behind it.
//
// IT MUST EXIST SEPARATELY because refuseUnrepresentableIntegers and RFC 8785
// disagree on purpose, not by oversight: RFC 8785 section 3.2.2.3 canonicalizes
// ANY number as a double, whatever its magnitude, while the admission gate
// refuses any number whose nearest double is past 2^53-1 outright (see
// refuseUnrepresentableIntegers's doc comment on why that rule has no
// notation carve-out). One of the RFC 8785 vectors this store pins conformance
// against, testdata/jcs/input/values.json, both ships from the spec's own
// reference suite AND contains 1E30 -- a value the gate refuses and RFC 8785
// requires canonicalizing to 1e+30. Routing that vector through
// canonicalDurableState would force a choice between weakening spec-conformance
// coverage and putting a magnitude exception in the gate, and neither is
// right: the gate's refusal is a policy decision about what this store admits,
// not a claim about what JCS can canonicalize. jcsCanonicalize lets a caller
// ask the second question without the first, so TestCanonicalDurableStateIsJCS
// can hold the RFC 8785 vectors to the spec's own bytes -- including
// values.json -- while refuseUnrepresentableIntegers's refusal of the same
// magnitude is pinned separately, in the admission tests
// (version_history_integer_admission_test.go), where it belongs.
func jcsCanonicalize(marshaled []byte) ([]byte, error) {
	canonical, err := jcs.Transform(marshaled)
	if err != nil {
		return nil, fmt.Errorf("canonicalize (RFC 8785): %w", err)
	}
	return canonical, nil
}

// ErrIntegerNotRepresentable is the refusal for a JSON number outside the I-JSON
// exact-integer range: one whose nearest binary64 has magnitude above 2^53-1, or
// that overflows binary64.
//
// The name is historical. It began as the refusal for an integer literal binary64
// cannot hold exactly, and it now also covers a literal spelled as a fraction whose
// nearest double lies at or beyond 2^53, which RFC 8785 would round to an integer
// just the same. It is kept rather than renamed so that nothing matching it with
// errors.Is has to change.
var ErrIntegerNotRepresentable = errors.New("number is outside the I-JSON exact-integer range (magnitude above 2^53-1)")

// maxExactJSONInteger is 2^53-1, the largest magnitude in the I-JSON
// interoperable integer range (RFC 7493 section 2.2), and BDP's admission law
// (gastownhall/bdp#21) makes a literal past it invalid rather than merely lossy.
// binary64 does represent 2^53 exactly, but 2^53 and 2^53+1 round to the same
// double, so from 2^53 up a literal can no longer be told from its neighbor.
const maxExactJSONInteger = 1<<53 - 1

// refuseUnrepresentableIntegers rejects marshaled if any JSON number literal in
// it is outside the I-JSON exact-integer range: the binary64 nearest to the
// literal has magnitude above 2^53-1, or the literal overflows binary64. Which of
// JSON's number forms spells the value -- bare integer, decimal or exponential --
// does not matter.
//
// WHY REFUSE RATHER THAN ROUND. jcs.Transform canonicalizes numbers as
// IEEE-754 doubles (RFC 8785 section 3.2.2.3), so 9007199254740993 becomes
// 9007199254740992 on the way in. That is not a formatting difference, it is
// a DIFFERENT VALUE, and it collapses two distinct issue states onto one
// durable_state and therefore one content token -- the token can no longer
// tell them apart, and a reader comparing tokens concludes the second write
// was a no-op. Rounding is silent; refusing is not.
//
// This seam is the admission gate, one layer above RecordVersionInTx, which
// is where BDP puts it: an authority adapting an existing store "MUST map or
// refuse values outside this contract before it serves them as BDP
// Resources" (gastownhall/bdp#20 section 5.1).
//
// THE NEAREST DOUBLE, NOT THE LITERAL'S SPELLING. Each literal is classified
// with strconv.ParseFloat, which returns the correctly rounded binary64 in time
// linear in the length of the literal, and that is exactly the value RFC 8785
// canonicalizes the literal to. So "the nearest double is past the range" and
// "canonicalization would move this literal past the range" are one question,
// and the gate is closed under its own canonicalization: whatever it admits
// canonicalizes to a form it also admits. A rule keyed to integer VALUES was
// not: 9007199254740991.5 is not an integer, so it was admitted, yet it rounds
// to 2^53 and its canonical bytes were then refused. Here 9007199254740993,
// 9007199254740993.0 and 9.007199254740993e15 are refused alike, and so is
// 9007199254740993.5, whose nearest double is 2^53+2. An overflow
// (strconv.ErrRange) is a refusal, not a syntax error; a magnitude too small for
// binary64 rounds to zero and is admitted.
//
// NOTHING IS BUILT PER EXPONENT. ParseFloat never constructs the number, so a
// literal such as 1e1000000 is an overflow refused in the time it takes to read
// it, not a million-digit integer built first and then compared. The gate runs
// inside the writer's transaction, so a classifier whose cost grew with an
// exponent would be a way to hold that transaction open with a few
// exponent-heavy metadata values.
//
// SCOPE. Ordinary fractions are admitted: 0.1 is canonicalized to its nearest
// double, which is how the store holds it. Whether two literals that share a
// nearest double can be told apart inside the store is a property of the store,
// not of this gate, and it is the one thing a Dolt upgrade could change: the
// store-level tests on each leg assert that no two literals the store reads back
// as different numbers are both admitted with one canonical form, and log what
// each Dolt does to a number, rather than assuming it. An integer past 2^53 is
// refused whatever the store does to it: a store may keep it exactly, where RFC
// 8785 would round it, and a store that already rounds it hands the gate an
// integer-valued double, which the rule refuses just the same. A JSON null is a
// token, not a number: only json.Number tokens are classified. Of types.Issue's
// own fields only Metadata (a json.RawMessage passed through verbatim) and
// Timeout (a time.Duration, written as nanoseconds) can reach this bound; its
// other integers are priorities, ordinals and counts held in 32-bit columns.
func refuseUnrepresentableIntegers(marshaled []byte) error {
	dec := json.NewDecoder(bytes.NewReader(marshaled))
	dec.UseNumber()
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("scan for unrepresentable integers: %w", err)
		}
		num, ok := tok.(json.Number)
		if !ok {
			continue
		}
		lit := num.String()
		f, err := strconv.ParseFloat(lit, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return fmt.Errorf("scan for unrepresentable integers: invalid JSON number literal %q: %w", lit, err)
		}
		if math.IsInf(f, 0) || math.Abs(f) > maxExactJSONInteger {
			return fmt.Errorf("%w: %s", ErrIntegerNotRepresentable, lit)
		}
	}
}

// CheckIssueVersionable reports whether issue could be minted into a version's
// durable_state. It is canonicalDurableState's own refusal -- the admission gate,
// then RFC 8785 canonicalization, over the whole marshaled issue -- so it refuses
// a number outside the I-JSON exact-integer range (ErrIntegerNotRepresentable)
// wherever in the issue it sits, in metadata or in a gate's timeout, and it also
// refuses duplicate keys, which RFC 8785 cannot canonicalize. The refusal names
// the top-level field it found the value in.
//
// It exists so that the check made when versioned history is switched on and
// the check the mint makes cannot disagree: FindUnversionable runs this over
// every issue the mint would version, and a store that passes it cannot then
// refuse the first write to a row it cleared. It is the one such check. A check
// over only part of an issue, its metadata say, can pass what the mint refuses (a
// gate's timeout can be past the range while its metadata is fine), so none may
// be exported beside it.
//
// The issue it is handed need not be the one the mint sees. The scan reads a
// lite projection, with no heavy text columns, no labels and no dependencies,
// and that is sound only because none of what it leaves out can hold a number.
// The tests in version_history_preenable_scan_test.go pin that.
func CheckIssueVersionable(issue *types.Issue) error {
	_, err := canonicalDurableState(issue)
	return err
}

// RecordVersionInTx mints one issue_versions row for issueID and advances
// issues.current_revision to match, as of tx (read-your-writes within the
// same transaction). A no-op when versioned history is disabled for tx, or
// when issueID resolves to a wisp: wisps carry current_revision for shape
// parity only and are never versioned this phase (design FR-8). IsWisp is
// Ephemeral || NoHistory, so a promoted no-history bead -- a durable
// issues-plane row with NoHistory=true -- is also never versioned; that is
// what no-history means, not an FR-8 wisp rule, and the write-path doc's
// row 13 says so.
//
// The revision it mints is a local ordinal, not an address — see the
// package comment above on ordinals versus version_id, and on the
// single-writer constraint that holds until version_id lands.
//
// actor is the acting identity that performed the mutation, recorded as the
// version row's attribution — "" when the mutation path genuinely has none,
// matching RecordEventInTx's own convention. It also drives
// attribution_status via attributionStatusForActor.
//
// This is the UPDATE-shaped entry point: design §16.2b's write fence. A
// record whose participation_generation is NULL is legacy — never
// positively promoted — and an update-shaped mutation against it mints
// nothing, neither half of this seam's write (no issue_versions row, no
// current_revision bump). A record that already carries a non-NULL
// participation_generation proceeds normally. Use RecordVersionForCreateInTx
// for a create-shaped mutation instead: a brand-new row has no legacy state
// to preserve, so that entry point stamps the column rather than checking
// it.
//
// The skip is whole: nothing about a legacy record is read, canonicalized or
// admitted, so such a write is neither versioned nor refused (design §16.2b: a
// skip, not a refusal).
func RecordVersionInTx(ctx context.Context, tx DBTX, issueID, actor string) error {
	if !versionedHistoryEnabled(tx) {
		return nil
	}
	return recordVersionAtInTx(ctx, tx, issueID, actor, time.Now().UTC(), mintUpdate)
}

// RecordVersionForCreateInTx is RecordVersionInTx's create-shaped sibling
// (design §16.2b). The write fence does not apply to a brand-new row, so
// instead of checking participation_generation, this stamps it — sourced
// from store_epoch.epoch, the same source the fence reads — as part of the
// row's first mint, whenever versioned history is enabled. A create with the
// flag off never reaches the stamp (this whole seam no-ops while the flag is
// off, same as RecordVersionInTx), leaving the column NULL — indistinguishable
// from a true legacy row (FR-7). A wisp arriving on the issues plane, by
// promotion or a persistence move out of the wisps table, mints through here
// too: its issues row is just as new, and a wisp never declares
// participation (FR-8).
func RecordVersionForCreateInTx(ctx context.Context, tx DBTX, issueID, actor string) error {
	if !versionedHistoryEnabled(tx) {
		return nil
	}
	return recordVersionAtInTx(ctx, tx, issueID, actor, time.Now().UTC(), mintCreate)
}

// RecordVersionAtInTx is RecordVersionInTx's test-support twin: it mints a
// version row at a caller-chosen change_at instead of time.Now(), and --
// deliberately -- does not gate on versionedHistoryEnabled at all, since its
// whole purpose is controlled-timestamp minting for conformance fixtures
// (R7.1's AsOfReadFixture.MintAt, backend/conformance/versioned_read_contract.go)
// regardless of a store's activation state. Production code never calls
// this; only per-leg as-of-read fixtures do, so their bare issue-create step
// can stay history-off (no unwanted real-time row) while still minting the
// exact versions a test needs at the instants it needs them.
//
// It bypasses design §16.2b's write fence exactly as it bypasses the gate:
// it neither reads participation_generation nor stamps it, so it mints for
// a legacy row and a participating row alike and leaves the column as it
// found it. The fence and the stamp belong to the production entry points
// above, never to a fixture's controlled-timestamp mint.
func RecordVersionAtInTx(ctx context.Context, tx DBTX, issueID, actor string, at time.Time) error {
	return recordVersionAtInTx(ctx, tx, issueID, actor, at, mintUnfenced)
}

// versionMintKind says how recordVersionAtInTx treats design §16.2b's write
// fence, the one thing RecordVersionInTx, RecordVersionForCreateInTx and
// RecordVersionAtInTx do not share.
type versionMintKind int

const (
	// mintUpdate is an update-shaped mutation (RecordVersionInTx): the fence
	// applies, and a record whose participation_generation is NULL mints nothing.
	mintUpdate versionMintKind = iota
	// mintCreate is a create-shaped mutation (RecordVersionForCreateInTx): a
	// brand-new row has no legacy state to preserve, so the fence does not
	// apply and the mint stamps participation_generation instead.
	mintCreate
	// mintUnfenced is RecordVersionAtInTx's test-support mint: no fence and no
	// stamp, exactly as it behaved before the fence existed.
	mintUnfenced
)

// mintSkipsRow reports whether the mint writes nothing for issue, which the
// caller has just loaded as issueID. It is the one place that answers the
// question: recordVersionAtInTx asks it before it reads or canonicalizes
// anything else about the row, and the defer-wake sweep asks it before it wakes
// a row, so the sweep cannot come to wake, or to skip, a row the mint treats
// differently.
//
// It decides in the mint's order. A wisp or no-history row (IsWisp) is never
// versioned, so it never reaches the second check. Then design §16.2b's write
// fence, which only an update-shaped call (mintUpdate) runs:
// participation_generation is read straight off the row rather than through
// types.Issue, because it is dual-write bookkeeping, not part of the issue's
// own content model, and so it stays out of the durable_state snapshot
// canonicalDurableState marshals. A brand-new row (mintCreate) has no legacy
// state to preserve, and recordVersionAtInTx stamps the column itself; the
// test-support mint (mintUnfenced) never reads it at all. An update-shaped call
// against a NULL (legacy) value is skipped here, before either half of this
// seam's write runs. The fence runs before the admission gate
// (canonicalDurableState) on purpose, so a legacy record is skipped whole:
// nothing about it is canonicalized, admitted or refused. Ignoring schema skew
// (internal/storage/schema/schema.go) has no bearing on this check: that
// downgrades CheckForwardDrift's migration-cursor refusal, a different plane
// than this column's real per-row data.
//
// An error is a failed read, not a refusal of the row.
func mintSkipsRow(ctx context.Context, tx DBTX, issueID string, issue *types.Issue, kind versionMintKind) (bool, error) {
	if IsWisp(issue) {
		return true, nil
	}
	if kind == mintUpdate {
		var participationGeneration sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			"SELECT participation_generation FROM issues WHERE id = ?", issueID,
		).Scan(&participationGeneration); err != nil {
			return false, fmt.Errorf("versioned history: read participation_generation for %s: %w", issueID, err)
		}
		if !participationGeneration.Valid {
			return true, nil
		}
	}
	return false, nil
}

// recordVersionAtInTx is RecordVersionInTx, RecordVersionForCreateInTx and
// RecordVersionAtInTx's shared body, taking at where the callers differ:
// time.Now().UTC() for the production gate-checked paths, a caller-chosen
// instant for test minting; and kind, which says whether design §16.2b's
// write fence applies (mintUpdate), the mint stamps participation_generation
// (mintCreate) or neither (mintUnfenced). The no-op rules documented on
// RecordVersionInTx above (wisps) apply here too, since this is that
// function's entire mechanism minus the gate.
func recordVersionAtInTx(ctx context.Context, tx DBTX, issueID, actor string, at time.Time, kind versionMintKind) error {
	// Fresh graph databases use DATETIME(6). Keep subsecond precision;
	// local ordinals and opaque graph tokens distinguish successive versions.

	issue, err := GetIssueInTx(ctx, tx, issueID)
	if err != nil {
		return fmt.Errorf("versioned history: snapshot %s: %w", issueID, err)
	}
	if skips, err := mintSkipsRow(ctx, tx, issueID, issue, kind); err != nil || skips {
		return err
	}

	// durable_state is the RFC 8785 (JCS) canonical form of the marshaled
	// issue, stored as bytes -- issue_versions.durable_state is a LONGBLOB
	// in fresh graph databases, not a JSON column. The invariant that buys,
	// the one donnabox asked for on #6358 item 4: sha256(stored bytes) is a
	// stable content token, because the bytes read back are exactly the
	// bytes the writer produced. A Dolt JSON column could not provide that.
	// It parses and renormalizes what it stores (measured on dolt 2.2.3:
	// 1.0 reads back as 1, 9007199254740993 as 9007199254740992, 1e300 as
	// 1e+300, while a LONGBLOB returns the same bytes) -- so the design's
	// verbatim-bytes promise (R5.1) and any content-derived token (#5898's
	// sha256-jcs) broke between the writer and the disk. JCS pins the bytes
	// on the way in (canonicalDurableState); LONGBLOB keeps them as written.
	//
	// The snapshot itself is the mutated issue (design §15.3, corrected by
	// §17.1): the write-path loader GetIssueInTx never hydrates Dependencies
	// (types.Issue.Dependencies is omitempty and unrelated to this snapshot's
	// own read), so it is populated here, once, for every caller of this
	// seam — in GetDependencyRecordsForIssuesInTx's own ordering (issue_id,
	// depends_on_id, type, id).
	deps, err := GetDependencyRecordsForIssuesInTx(ctx, tx, []string{issueID})
	if err != nil {
		return fmt.Errorf("versioned history: load dependencies for %s: %w", issueID, err)
	}
	issue.Dependencies = deps[issueID]

	// store_epoch is one shared row (id = 1) that every minting transaction
	// reads. Read first and seed only when the row is absent, so the seed
	// INSERT happens once per store rather than once per mint: an INSERT IGNORE
	// on every mint put that shared row into every write transaction's
	// footprint for nothing.
	var epoch int
	err = tx.QueryRowContext(ctx, "SELECT epoch FROM store_epoch WHERE id = 1").Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		if _, seedErr := tx.ExecContext(ctx, "INSERT IGNORE INTO store_epoch (id, epoch) VALUES (1, 1)"); seedErr != nil {
			return fmt.Errorf("versioned history: seed store epoch: %w", seedErr)
		}
		err = tx.QueryRowContext(ctx, "SELECT epoch FROM store_epoch WHERE id = 1").Scan(&epoch)
	}
	if err != nil {
		return fmt.Errorf("versioned history: read store epoch: %w", err)
	}

	var newRevision int64
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(revision), 0) + 1 FROM issue_versions WHERE issue_id = ?", issueID,
	).Scan(&newRevision); err != nil {
		return fmt.Errorf("versioned history: compute next revision for %s: %w", issueID, err)
	}

	durableState, err := canonicalDurableState(issue)
	if err != nil {
		return fmt.Errorf("versioned history: marshal durable state for %s: %w", issueID, err)
	}

	// durableState is bound as []byte, a LONGBLOB parameter -- never
	// string(durableState), which would ask the driver to treat it as text.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO issue_versions
			(issue_id, revision, epoch, durable_state, change_actor, change_agent, change_message, change_at, attribution_status)
		VALUES (?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		issueID, newRevision, epoch, durableState, actor, at, attributionStatusForActor(actor),
	); err != nil {
		return fmt.Errorf("versioned history: insert version row for %s: %w", issueID, err)
	}

	// Advancing recorder bookkeeping must not fire updated_at's ON UPDATE
	// clause after durableState has captured the accepted Issue state.
	if kind == mintCreate {
		// Stamps participation_generation in the same statement that advances
		// current_revision — design §16.2b's "positive declaration sourced
		// from store_epoch.epoch," minted here from the epoch already read
		// above for this same transaction's version row.
		if _, err := tx.ExecContext(ctx,
			"UPDATE issues SET current_revision = ?, participation_generation = ?, updated_at = updated_at WHERE id = ?",
			newRevision, epoch, issueID,
		); err != nil {
			return fmt.Errorf("versioned history: advance current_revision for %s: %w", issueID, err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE issues SET current_revision = ?, updated_at = updated_at WHERE id = ?", newRevision, issueID,
	); err != nil {
		return fmt.Errorf("versioned history: advance current_revision for %s: %w", issueID, err)
	}
	return nil
}
