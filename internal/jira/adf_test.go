package jira

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDescriptionToMarkdown(t *testing.T) {
	tests := []struct{ name, raw, want string }{
		{"bullet list", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Intro"}]},{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]}]},{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}]},{"type":"paragraph","content":[{"type":"text","text":"Outro"}]}]}`, "Intro\n\n- one\n- two\n\nOutro"},
		{"nested numbered list", `{"type":"doc","content":[{"type":"orderedList","attrs":{"order":3},"content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"parent"}]},{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"child"}]}]}]}]},{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"next"}]}]}]}]}`, "3. parent\n   \n   - child\n4. next"},
		{"heading and inline marks", `{"type":"doc","content":[{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Scope"}]},{"type":"paragraph","content":[{"type":"text","text":"bold","marks":[{"type":"strong"}]},{"type":"text","text":" and "},{"type":"text","text":"italic","marks":[{"type":"em"}]},{"type":"text","text":" old","marks":[{"type":"strike"}]},{"type":"hardBreak"},{"type":"text","text":"link","marks":[{"type":"link","attrs":{"href":"https://example.com/a(b)?q=x y"}}]}]}]}`, "## Scope\n\n**bold** and *italic* ~~old~~  \n[link](<https://example.com/a(b)?q=x%20y>)"},
		{"combined marks", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"marked","marks":[{"type":"em"},{"type":"strong"},{"type":"link","attrs":{"href":"https://example.com"}}]}]}]}`, "[***marked***](<https://example.com>)"},
		{"inline code delimiter", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"a\u0060b","marks":[{"type":"code"}]}]}]}`, "``a`b``"},
		{"inline code boundary", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"\u0060x\u0060","marks":[{"type":"code"}]}]}]}`, "`` `x` ``"},
		{"code block fence", `{"type":"doc","content":[{"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"fmt.Println(\"\u0060\u0060\u0060\")"}]}]}`, "````go\nfmt.Println(\"```\")\n````"},
		{"table", `{"type":"doc","content":[{"type":"table","content":[{"type":"tableRow","content":[{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"Name"}]}]},{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"Result"}]}]}]},{"type":"tableRow","content":[{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"a|b"}]}]},{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]},{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}]}]}]}`, "| Name | Result |\n| --- | --- |\n| a\\|b | one<br><br>two |"},
		{"panel and blockquote", `{"type":"doc","content":[{"type":"panel","content":[{"type":"paragraph","content":[{"type":"text","text":"Warning"}]}]},{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"Quoted"}]},{"type":"paragraph","content":[{"type":"text","text":"Again"}]}]}]}`, "> Warning\n\n> Quoted\n> \n> Again"},
		{"unknown container", `{"type":"doc","content":[{"type":"futureBlock","content":[{"type":"paragraph","content":[{"type":"text","text":"Keep descendants"}]}]}]}`, "Keep descendants"},
		{"literal Markdown and HTML", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"*literal* [x] <script>"}]}]}`, "\\*literal\\* \\[x\\] \\<script\\>"},
		{"unsafe link remains text", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"label","marks":[{"type":"link","attrs":{"href":"javascript:alert(1)"}}]}]}]}`, "label (javascript:alert(1))"},
		{"table without header", `{"type":"doc","content":[{"type":"table","content":[{"type":"tableRow","content":[{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"A"}]}]},{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"B"}]}]}]}]}]}`, "|  |  |\n| --- | --- |\n| A | B |"},
		{"literal list markers", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"1. list\n- item\n+ more"}]}]}`, "1\\. list\n\\- item\n\\+ more"},
		{"mention emoji and status", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"mention","attrs":{"text":"@Alice"}},{"type":"text","text":" "},{"type":"emoji","attrs":{"shortName":":smile:"}},{"type":"text","text":" "},{"type":"status","attrs":{"text":"Done"}}]}]}`, "@Alice :smile: Done"},
		{"empty document", `{"type":"doc","content":[]}`, ""},
		{"leading and trailing spaces", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":" leading "}]}]}`, " leading "},
		{"duplicate marks", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"once","marks":[{"type":"strong"},{"type":"strong"}]}]}]}`, "**once**"},
		{"v2 string", `"hello\nworld"`, "hello\nworld"},
		{"raw text", "not JSON", "not JSON"},
		{"non-document object", `{"type":"other","text":"retain raw"}`, `{"type":"other","text":"retain raw"}`},
		{"malformed JSON", `{"type":"doc",`, `{"type":"doc",`},
		{"whitespace null", " \nnull\t", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DescriptionToPlainText(json.RawMessage(tt.raw)); got != tt.want {
				t.Errorf("description = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestADFImportCallersPreserveList(t *testing.T) {
	ji := &Issue{Key: "PROJ-42", Fields: IssueFields{Description: json.RawMessage(`{"type":"doc","content":[{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Definition of Done"}]}]}]}]}`)}}
	ti := jiraToTrackerIssue(ji, nil)
	if ti.Description != "- Definition of Done" {
		t.Errorf("tracker description = %q", ti.Description)
	}
	conv := (&jiraFieldMapper{}).IssueToBeads(&ti)
	if conv == nil || conv.Issue.Description != "- Definition of Done" {
		t.Fatalf("field mapper did not preserve nested description: %+v", conv)
	}
}

func TestADFLimitsRetainOriginal(t *testing.T) {
	paragraph := `{"type":"paragraph","content":[{"type":"text","text":"retained"}]}`
	nested := paragraph
	for range 70 {
		nested = `{"type":"blockquote","content":[` + nested + `]}`
	}
	largeText, err := json.Marshal(strings.Repeat("x", 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	amplified, err := json.Marshal(strings.Repeat("x\n", 125000))
	if err != nil {
		t.Fatal(err)
	}
	amplification := `{"type":"paragraph","content":[{"type":"text","text":` + string(amplified) + `}]}`
	for range 10 {
		amplification = `{"type":"blockquote","content":[` + amplification + `]}`
	}
	tests := []struct{ name, content string }{
		{"depth", nested},
		{"input bytes", `{"type":"text","text":` + string(largeText) + `}`},
		{"node count", strings.TrimSuffix(strings.Repeat(`{"type":"text"},`, 50001), ",")},
		{"rendered bytes", amplification},
		{"mark count", `{"type":"paragraph","content":[{"type":"text","text":"retained","marks":[` + strings.TrimSuffix(strings.Repeat(`{"type":"em"},`, 50001), ",") + `]}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := json.RawMessage(`{"type":"doc","content":[` + tt.content + `]}`)
			if got := DescriptionToPlainText(raw); got != string(raw) {
				t.Errorf("limit must retain original JSON; got %d bytes, want %d", len(got), len(raw))
			}
		})
	}
}
