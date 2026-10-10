package codeindex

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// parseXML records element/attribute names only. The stdlib decoder neither
// loads DTDs nor resolves external entities, and receives no project host.
func parseXML(input parserInput) File {
	out := File{Path: input.Path, Blocked: map[string][]string{}}
	decoder := xml.NewDecoder(strings.NewReader(input.Content))
	stack := []int{}
	steps, roots := 0, 0
	symbolBytes := 0
	addSymbol := func(symbol Symbol) bool {
		encoded, err := json.Marshal(symbol)
		// Leave room for file provenance and serialization separators.
		if err != nil || len(encoded)+1 > maxJSONBytes-64*1024-symbolBytes {
			return false
		}
		symbolBytes += len(encoded) + 1
		out.Symbols = append(out.Symbols, symbol)
		return true
	}
	fail := func(kind string) File {
		return File{Path: input.Path, Blocked: map[string][]string{}, ParseError: kind}
	}
	// Namespace URIs are values and may contain credentials; retain local names.
	for {
		startLine, _ := decoder.InputPos()
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail("SyntaxError")
		}
		steps++
		if steps > 200000 || len(stack) > 256 {
			return fail("ASTLimit")
		}
		switch node := token.(type) {
		case xml.StartElement:
			if len(stack) == 0 {
				roots++
				if roots > 1 {
					return fail("SyntaxError")
				}
			}
			name, parent := node.Name.Local, moduleID(input.Path)
			if len(stack) > 0 {
				s := out.Symbols[stack[len(stack)-1]]
				parent = s.ID
				name = s.Name + "." + name
			}
			id := fmt.Sprintf("%s::%s@L%d_%d", input.Path, name, startLine, len(out.Symbols))
			position := len(out.Symbols)
			if !addSymbol(Symbol{ID: id, Name: name, Kind: "element", Line: startLine, EndLine: startLine, Parent: parent}) {
				return fail("ASTLimit")
			}
			stack = append(stack, position)
			for _, a := range node.Attr {
				label := name + ".@" + a.Name.Local
				if !addSymbol(Symbol{ID: fmt.Sprintf("%s::%s@L%d_%d", input.Path, label, startLine, len(out.Symbols)), Name: label, Kind: "attribute", Line: startLine, EndLine: startLine, Parent: id}) {
					return fail("ASTLimit")
				}
			}
		case xml.EndElement:
			if len(stack) == 0 {
				return fail("SyntaxError")
			}
			endLine, _ := decoder.InputPos()
			out.Symbols[stack[len(stack)-1]].EndLine = endLine
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 && strings.TrimSpace(string(node)) != "" {
				return fail("SyntaxError")
			}
		}
	}
	if len(stack) != 0 || roots != 1 {
		return fail("SyntaxError")
	}
	return out
}
