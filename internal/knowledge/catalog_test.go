package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/steveyegge/beads/memoryops"
)

// Models only the observed TEXT limit and interrupted publication. Real Dolt
// fresh-process persistence is checked separately by the CLI fixture.
type catalogMemory struct {
	memoryops.Memories
	plane     map[string]string
	writes    int
	failAfter int
	failKey   string
}

func (m *catalogMemory) Remember(_ context.Context, r memoryops.RememberRequest) (memoryops.RememberResult, error) {
	if len(r.Content) > 65535 {
		return memoryops.RememberResult{}, errors.New("TEXT value too long")
	}
	if r.Key == m.failKey || (m.failAfter >= 0 && m.writes >= m.failAfter) {
		return memoryops.RememberResult{}, errors.New("interrupted write")
	}
	m.writes++
	m.plane[r.Key] = r.Content
	return memoryops.RememberResult{}, nil
}
func (m *catalogMemory) Recall(_ context.Context, r memoryops.RecallRequest) (memoryops.RecallResult, error) {
	v, ok := m.plane[r.Key]
	return memoryops.RecallResult{Value: v, Found: ok}, nil
}
func largeCatalog() Catalog {
	c := Catalog{Roots: []string{"docs"}, Sources: []Source{}}
	for i := 0; i < 150; i++ {
		s := Source{Path: fmt.Sprintf("docs/%03d.md", i), SHA256: strings.Repeat("a", 64), Contracts: []string{"DOX.md"}}
		for j := 0; j < 12; j++ {
			s.Headings = append(s.Headings, strings.Repeat("é界", 35)+fmt.Sprint(i, j))
		}
		c.Sources = append(c.Sources, s)
	}
	return c
}
func TestCatalogLargePersistenceAndInterruptedPublication(t *testing.T) {
	ctx := context.Background()
	old := Catalog{Roots: []string{"DOX.md"}, Sources: []Source{{Path: "DOX.md", SHA256: strings.Repeat("b", 64)}}}
	m := &catalogMemory{plane: map[string]string{"human": "established solution"}, failAfter: -1}
	if err := SaveCatalog(ctx, m, old); err != nil {
		t.Fatal(err)
	}
	large := largeCatalog()
	raw, _ := json.Marshal(large)
	if len(raw) <= 65535 {
		t.Fatal("fixture does not cross observed storage limit")
	}
	previous := m.plane[Prefix+"catalog"]
	m.failAfter = m.writes + 1
	if err := SaveCatalog(ctx, m, large); err == nil {
		t.Fatal("interrupted publication succeeded")
	}
	if m.plane[Prefix+"catalog"] != previous || !reflect.DeepEqual(Decode(m.plane).Catalog, old) {
		t.Fatal("interruption replaced previous selection")
	}
	m.failAfter = -1
	m.failKey = Prefix + "catalog"
	if err := SaveCatalog(ctx, m, large); err == nil || m.plane[Prefix+"catalog"] != previous {
		t.Fatal("failed manifest replaced selection")
	}
	m.failKey = ""
	if err := SaveCatalog(ctx, m, large); err != nil {
		t.Fatal(err)
	}
	if got := Decode(m.plane); !reflect.DeepEqual(got.Catalog, large) || len(got.Warnings) > 0 || len(got.Records) != 0 {
		t.Fatalf("fresh decode lost catalog: sources=%d warnings=%v", len(got.Catalog.Sources), got.Warnings)
	}
	writes := m.writes
	if err := SaveCatalog(ctx, m, large); err != nil || m.writes != writes {
		t.Fatal("unchanged catalog performed writes")
	}
	for k, v := range m.plane {
		if len(v) > 60*1024 || !utf8.ValidString(v) {
			t.Fatalf("unbounded or broken UTF-8 row %s", k)
		}
	}
	if m.plane["human"] != "established solution" {
		t.Fatal("human memory changed")
	}
	// Losing or altering any published part withholds the complete catalog.
	for k, v := range m.plane {
		if k == Prefix+"catalog" || k == "human" {
			continue
		}
		delete(m.plane, k)
		state := Decode(m.plane)
		if len(state.Catalog.Roots) != 0 || len(state.Warnings) == 0 {
			t.Fatal("missing part silently accepted")
		}
		m.plane[k] = v + "x"
		state = Decode(m.plane)
		if len(state.Catalog.Roots) != 0 || len(state.Warnings) == 0 {
			t.Fatal("corrupt part silently accepted")
		}
		m.plane[k] = v
		break
	}
}

func TestCatalogRefusesOversizedPreparationWithoutWrites(t *testing.T) {
	m := &catalogMemory{plane: map[string]string{"human": "keep"}, failAfter: -1}
	c := Catalog{Sources: []Source{{Path: "a.md", Headings: []string{strings.Repeat("x", 8*1024*1024)}}}}
	if err := SaveCatalog(context.Background(), m, c); err == nil || m.writes != 0 {
		t.Fatal("unbounded catalog was published")
	}
	c = largeCatalog()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SaveCatalog(ctx, m, c); err == nil || m.writes != 0 {
		t.Fatal("cancelled catalog was published")
	}
}
