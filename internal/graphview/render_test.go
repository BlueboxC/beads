package graphview

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/steveyegge/beads/internal/activity"
	"io"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestNativeViewerPreservesEvidenceOffline(t *testing.T) {
	t.Parallel()
	const hostile = "</script><img src=x onerror=alert(1)>"
	for _, knowledge := range []bool{false, true} {
		page := Page{Activity: &activity.View{Enabled: true, Total: 1, Events: []activity.Event{{ID: "origin", Kind: "turn_end", Summary: hostile}}}, Title: hostile, Knowledge: knowledge, Nodes: []Node{{ID: "source:a", Title: hostile, Type: "source", Detail: map[string]any{"evidence": hostile}}, {ID: "record:fix", Title: "Fix", Type: "solution", Validity: "needs_review"}}, Links: []Edge{{Source: "record:fix", Target: "source:a", Type: "supported-by"}}}
		var out bytes.Buffer
		if err := WriteHTML(&out, page); err != nil {
			t.Fatal(err)
		}
		content := out.String()
		if strings.Contains(content, hostile) || strings.Contains(content, "<script src=") {
			t.Fatal("stored strings or network dependency escaped the offline boundary")
		}
		if !strings.Contains(content, "Permission to use, copy, modify") {
			t.Fatal("bundled D3 license missing")
		}
		match := regexp.MustCompile(`(?m)^const data = (.*);$`).FindStringSubmatch(content)
		if len(match) != 2 {
			t.Fatal("graph data missing")
		}
		var got Page
		if err := json.Unmarshal([]byte(match[1]), &got); err != nil {
			t.Fatal(err)
		}
		for _, field := range []struct {
			name   string
			target any
		}{{"nodes", &got.Nodes}, {"links", &got.Links}} {
			match := regexp.MustCompile(`(?m)^const ` + field.name + ` = (.*);$`).FindStringSubmatch(content)
			if len(match) != 2 {
				t.Fatal("graph arrays missing")
			}
			if err := json.Unmarshal([]byte(match[1]), field.target); err != nil {
				t.Fatal(err)
			}
		}
		if !reflect.DeepEqual(got, page) {
			t.Fatalf("graph evidence changed: %#v", got)
		}
	}
}

type brokenWriter struct{ err error }

func (w brokenWriter) Write([]byte) (int, error) { return 0, w.err }

func TestNativeViewerReportsWriteFailure(t *testing.T) {
	t.Parallel()
	err := WriteHTML(brokenWriter{io.ErrClosedPipe}, Page{})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("writer failure lost: %v", err)
	}
}
