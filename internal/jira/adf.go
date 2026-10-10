package jira

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

const (
	maxADFInput  = 1 << 20
	maxADFOutput = 2 << 20
	maxADFDepth  = 64
	maxADFNodes  = 50000
)

type adfAttrs struct {
	Level     int    `json:"level"`
	Order     *int   `json:"order"`
	Language  string `json:"language"`
	Href      string `json:"href"`
	Text      string `json:"text"`
	ShortName string `json:"shortName"`
	URL       string `json:"url"`
}

type adfMark struct {
	Type  string   `json:"type"`
	Attrs adfAttrs `json:"attrs"`
}

type adfNode struct {
	Type    string    `json:"type"`
	Text    string    `json:"text"`
	Attrs   adfAttrs  `json:"attrs"`
	Marks   []adfMark `json:"marks"`
	Content []adfNode `json:"content"`
}

// DescriptionToPlainText retains its API name; ADF imports now produce Markdown.
// Invalid or over-budget documents retain the original JSON rather than partial text.
func DescriptionToPlainText(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return plain
	}
	if len(raw) > maxADFInput {
		return string(raw)
	}
	var doc adfNode
	if json.Unmarshal(raw, &doc) != nil || doc.Type != "doc" {
		return string(raw)
	}
	r := adfRenderer{}
	text := r.render(doc, 0, false)
	if r.failed {
		return string(raw)
	}
	return text
}

type adfRenderer struct {
	nodes  int
	failed bool
}

func (r *adfRenderer) visit(n adfNode, depth int) bool {
	r.nodes += 1 + len(n.Marks)
	if depth > maxADFDepth || r.nodes > maxADFNodes {
		r.failed = true
	}
	return !r.failed
}

func (r *adfRenderer) bound(text string) string {
	if len(text) > maxADFOutput {
		r.failed = true
		return ""
	}
	return text
}

func (r *adfRenderer) children(nodes []adfNode, depth int, sep string, literal bool) string {
	var out strings.Builder
	for _, n := range nodes {
		text := r.render(n, depth+1, literal)
		if r.failed {
			return ""
		}
		if text == "" {
			continue
		}
		if out.Len()+len(sep)+len(text) > maxADFOutput {
			r.failed = true
			return ""
		}
		if out.Len() > 0 {
			out.WriteString(sep)
		}
		out.WriteString(text)
	}
	return out.String()
}

func (r *adfRenderer) render(n adfNode, depth int, literal bool) string {
	if !r.visit(n, depth) {
		return ""
	}
	var text string
	switch n.Type {
	case "text":
		text = n.Text
		if !literal {
			text = adfMarkedText(n)
		}
	case "hardBreak":
		text = "\n"
		if !literal {
			text = "  \n"
		}
	case "paragraph":
		text = r.children(n.Content, depth, "", literal)
	case "heading":
		level := n.Attrs.Level
		if level < 1 || level > 6 {
			level = 1
		}
		text = strings.Repeat("#", level) + " " + r.children(n.Content, depth, "", literal)
	case "bulletList", "orderedList":
		start := 1
		if n.Attrs.Order != nil && *n.Attrs.Order >= 0 {
			start = *n.Attrs.Order
		}
		var items []string
		length := 0
		for i, item := range n.Content {
			marker := "- "
			if n.Type == "orderedList" {
				if start+i < start {
					r.failed = true
					break
				}
				marker = strconv.Itoa(start+i) + ". "
			}
			value := r.render(item, depth+1, literal)
			value = r.prefix(value, strings.Repeat(" ", len(marker)))
			if r.failed {
				break
			}
			value = marker + strings.TrimPrefix(value, strings.Repeat(" ", len(marker)))
			length += len(value) + 1
			if length > maxADFOutput {
				r.failed = true
				break
			}
			items = append(items, value)
		}
		text = strings.Join(items, "\n")
	case "blockquote", "panel":
		text = r.prefix(r.children(n.Content, depth, "\n\n", literal), "> ")
	case "codeBlock":
		code := r.children(n.Content, depth, "", true)
		fence := strings.Repeat("`", max(3, longestBackticks(code)+1))
		language := n.Attrs.Language
		if strings.ContainsFunc(language, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_+-", c))
		}) {
			language = ""
		}
		text = fence + language + "\n" + code + "\n" + fence
	case "table":
		text = r.table(n.Content, depth)
	case "rule":
		text = "---"
	case "mention", "status":
		text = adfEscape(n.Attrs.Text)
	case "emoji":
		text = n.Attrs.Text
		if text == "" {
			text = n.Attrs.ShortName
		}
		text = adfEscape(text)
	case "inlineCard":
		text = adfLink(adfEscape(n.Attrs.URL), n.Attrs.URL)
	default:
		text = r.children(n.Content, depth, "\n\n", literal)
		if n.Text != "" {
			text = adfEscape(n.Text) + text
		}
	}
	return r.bound(text)
}

func (r *adfRenderer) prefix(text, prefix string) string {
	if text == "" || r.failed {
		return ""
	}
	if len(text)+(strings.Count(text, "\n")+1)*len(prefix) > maxADFOutput {
		r.failed = true
		return ""
	}
	return prefix + strings.ReplaceAll(text, "\n", "\n"+prefix)
}

func (r *adfRenderer) table(nodes []adfNode, depth int) string {
	var rows [][]string
	width := 0
	header := false
	for i, row := range nodes {
		if !r.visit(row, depth+1) {
			return ""
		}
		if row.Type != "tableRow" {
			r.failed = true
			return ""
		}
		var cells []string
		for _, cell := range row.Content {
			value := r.render(cell, depth+2, false)
			if r.failed {
				return ""
			}
			if i == 0 && cell.Type == "tableHeader" {
				header = true
			}
			value = strings.ReplaceAll(value, "|", "\\|")
			value = strings.ReplaceAll(value, "\n", "<br>")
			cells = append(cells, value)
		}
		width = max(width, len(cells))
		rows = append(rows, cells)
	}
	if width == 0 {
		return ""
	}
	if !header {
		rows = append([][]string{{}}, rows...)
	}
	var out strings.Builder
	for i, cells := range rows {
		cells = append(cells, make([]string, width-len(cells))...)
		line := "| " + strings.Join(cells, " | ") + " |\n"
		if out.Len()+len(line) > maxADFOutput {
			r.failed = true
			return ""
		}
		out.WriteString(line)
		if i == 0 {
			out.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
		}
	}
	return r.bound(strings.TrimSuffix(out.String(), "\n"))
}

var adfEscaper = strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "<", "\\<", ">", "\\>", "`", "\\`", "~", "\\~", "!", "\\!", "#", "\\#", "-", "\\-", "+", "\\+", ".", "\\.", "=", "\\=")

func adfEscape(text string) string { return adfEscaper.Replace(text) }

func adfMarkedText(n adfNode) string {
	if len(n.Marks) == 0 {
		return adfEscape(n.Text)
	}
	marks := make(map[string]adfMark, len(n.Marks))
	for _, mark := range n.Marks {
		marks[mark.Type] = mark
	}
	text := adfEscape(n.Text)
	if _, ok := marks["code"]; ok {
		fence := strings.Repeat("`", longestBackticks(n.Text)+1)
		code := n.Text
		if strings.HasPrefix(code, "`") || strings.HasSuffix(code, "`") || strings.HasPrefix(code, " ") && strings.HasSuffix(code, " ") && strings.TrimSpace(code) != "" {
			code = " " + code + " "
		}
		text = fence + code + fence
	}
	for _, mark := range []struct{ kind, delimiter string }{{"strong", "**"}, {"em", "*"}, {"strike", "~~"}} {
		if _, ok := marks[mark.kind]; ok && strings.TrimSpace(text) != "" {
			core := strings.TrimSpace(text)
			start := strings.Index(text, core)
			text = text[:start] + mark.delimiter + core + mark.delimiter + text[start+len(core):]
		}
	}
	if link, ok := marks["link"]; ok {
		text = adfLink(text, link.Attrs.Href)
	}
	return text
}

func adfLink(text, href string) string {
	u, err := url.Parse(href)
	if href == "" {
		return text
	}
	if err != nil || strings.ContainsAny(href, "\r\n\t") || u.Scheme != "" && !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "mailto") {
		return text + " (" + adfEscape(href) + ")"
	}
	href = strings.NewReplacer(" ", "%20", "<", "%3C", ">", "%3E", "\\", "%5C").Replace(href)
	return "[" + text + "](<" + href + ">)"
}

func longestBackticks(text string) int {
	longest, run := 0, 0
	for _, c := range text {
		if c == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return longest
}
