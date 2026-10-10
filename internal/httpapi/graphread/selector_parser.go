package graphread

type selectorToken struct {
	kind       string
	offset     int
	segments   []selectorSegment
	literal    any
	valueCount int
}

type selectorParser struct {
	limits    SelectorLimits
	nodes     []selectorNode
	values    []int
	operators []selectorToken
}

func selectorParse(tokens []selectorToken, limits SelectorLimits) (*Selector, error) {
	for i, kind := range []string{"$", "[", "?"} {
		if i >= len(tokens) {
			return nil, selectorSyntax("incomplete Selector wrapper", tokens[len(tokens)-1].offset)
		}
		if tokens[i].kind != kind {
			return nil, selectorSyntax("expected '"+kind+"'", tokens[i].offset)
		}
	}
	p := selectorParser{limits: limits}
	expectingOperand := true
	for cursor := 3; cursor < len(tokens); cursor++ {
		token := tokens[cursor]
		switch token.kind {
		case "end":
			return nil, selectorSyntax("expected closing ']'", token.offset)
		case "]":
			if expectingOperand {
				return nil, selectorSyntax("expected a filter expression", token.offset)
			}
			for _, op := range p.operators {
				if op.kind == "(" {
					return nil, selectorSyntax("expected closing ')'", token.offset)
				}
			}
			for len(p.operators) > 0 {
				if err := p.reduce(); err != nil {
					return nil, err
				}
			}
			if len(p.values) != 1 || p.nodes[p.values[0]].kind == "literal" {
				return nil, selectorSyntax("incomplete filter expression", token.offset)
			}
			trailing := tokens[cursor+1]
			if trailing.kind != "end" {
				switch trailing.kind {
				case "path", "$", "[":
					return nil, selectorUnsupported("selection of nested values is not supported", trailing.offset)
				}
				return nil, selectorSyntax("unexpected content after closing ']'", trailing.offset)
			}
			return &Selector{nodes: p.nodes}, nil
		}
		if expectingOperand {
			switch token.kind {
			case "!":
				if len(p.operators) > 0 && p.operators[len(p.operators)-1].kind == "!" {
					return nil, selectorSyntax("repeated logical NOT requires parentheses", token.offset)
				}
				p.operators = append(p.operators, token)
			case "(":
				token.valueCount = len(p.values)
				p.operators = append(p.operators, token)
			case "path":
				size := len(token.segments) + 1
				if err := p.add(selectorNode{kind: "path", segments: token.segments, depth: size, count: size}, token.offset); err != nil {
					return nil, err
				}
				expectingOperand = false
			case "literal":
				if err := p.add(selectorNode{kind: "literal", literal: token.literal, depth: 1, count: 1}, token.offset); err != nil {
					return nil, err
				}
				expectingOperand = false
			case "[":
				return nil, selectorUnsupported("array literals and nested filters are not supported", token.offset)
			case "$":
				return nil, selectorUnsupported("absolute paths and joins are not supported", token.offset)
			default:
				return nil, selectorSyntax("expected a path, literal, '!', or '('", token.offset)
			}
			continue
		}
		if selectorPrecedence(token.kind) > 0 && token.kind != "!" {
			for len(p.operators) > 0 && selectorPrecedence(p.operators[len(p.operators)-1].kind) >= selectorPrecedence(token.kind) {
				if err := p.reduce(); err != nil {
					return nil, err
				}
			}
			p.operators = append(p.operators, token)
			expectingOperand = true
			continue
		}
		switch token.kind {
		case ")":
			if err := p.closeGroup(token.offset); err != nil {
				return nil, err
			}
		case "[":
			return nil, selectorUnsupported("array and bracket selectors are not supported", token.offset)
		case "$":
			return nil, selectorUnsupported("absolute paths and joins are not supported", token.offset)
		default:
			return nil, selectorSyntax("expected an operator, ')', or closing ']'", token.offset)
		}
	}
	return nil, selectorSyntax("expected closing ']'", tokens[len(tokens)-1].offset)
}

func selectorPrecedence(operator string) int {
	switch operator {
	case "!":
		return 4
	case "==", "!=", "<", "<=", ">", ">=":
		return 3
	case "&&":
		return 2
	case "||":
		return 1
	}
	return 0
}
func (p *selectorParser) add(n selectorNode, offset int) error {
	if err := selectorCheckSize(n.depth, n.count, p.limits, offset); err != nil {
		return err
	}
	p.values = append(p.values, len(p.nodes))
	p.nodes = append(p.nodes, n)
	return nil
}
func (p *selectorParser) pop(offset int) (int, error) {
	if len(p.values) == 0 {
		return 0, selectorSyntax("operator is missing an operand", offset)
	}
	i := p.values[len(p.values)-1]
	p.values = p.values[:len(p.values)-1]
	return i, nil
}
func (p *selectorParser) reduce() error {
	token := p.operators[len(p.operators)-1]
	p.operators = p.operators[:len(p.operators)-1]
	right, err := p.pop(token.offset)
	if err != nil {
		return err
	}
	r := p.nodes[right]
	if token.kind == "!" {
		if r.kind == "literal" {
			return selectorSyntax("a JSON literal is not a filter expression", token.offset)
		}
		return p.add(selectorNode{kind: "!", left: right, depth: r.depth + 1, count: r.count + 1}, token.offset)
	}
	left, err := p.pop(token.offset)
	if err != nil {
		return err
	}
	l := p.nodes[left]
	if token.kind == "&&" || token.kind == "||" {
		if l.kind == "literal" || r.kind == "literal" {
			return selectorSyntax("a JSON literal is not a filter expression", token.offset)
		}
	} else {
		if (l.kind != "path" && l.kind != "literal") || (r.kind != "path" && r.kind != "literal") {
			return selectorSyntax("comparison operands must be singular paths or JSON literals", token.offset)
		}
		if l.kind == "path" && r.kind == "path" {
			return selectorUnsupported("path-to-path comparisons are not supported", token.offset)
		}
	}
	return p.add(selectorNode{kind: token.kind, left: left, right: right, depth: max(l.depth, r.depth) + 1, count: l.count + r.count + 1}, token.offset)
}
func (p *selectorParser) closeGroup(offset int) error {
	for len(p.operators) > 0 {
		token := p.operators[len(p.operators)-1]
		if token.kind != "(" {
			if err := p.reduce(); err != nil {
				return err
			}
			continue
		}
		p.operators = p.operators[:len(p.operators)-1]
		if len(p.values) != token.valueCount+1 {
			return selectorSyntax("parenthesized expression is incomplete", offset)
		}
		index, err := p.pop(offset)
		if err != nil {
			return err
		}
		operand := p.nodes[index]
		if operand.kind == "literal" {
			return selectorSyntax("a JSON literal is not a filter expression", offset)
		}
		return p.add(selectorNode{kind: "group", left: index, depth: operand.depth + 1, count: operand.count + 1}, token.offset)
	}
	return selectorSyntax("unmatched ')'", offset)
}
