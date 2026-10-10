package graphread

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

var selectorJSONNumber = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?`)
var selectorIndex = regexp.MustCompile(`^(?:0|-?[1-9][0-9]*)$`)

func selectorTokenize(source string) ([]selectorToken, error) {
	var tokens []selectorToken
	for offset := 0; offset < len(source); {
		character := source[offset]
		if selectorSpace(character) {
			offset++
			continue
		}
		start := offset
		token := selectorToken{offset: start}
		var err error
		switch {
		case character == '"' || character == '\'':
			token.kind = "literal"
			token.literal, offset, err = selectorScanString(source, start)
		case character == '@':
			token.kind = "path"
			token.segments, offset, err = selectorScanPath(source, start)
		case character == '-' || selectorDigit(character):
			token.kind = "literal"
			token.literal, offset, err = selectorScanNumber(source, start)
		case selectorNameFirst(source, offset):
			var name string
			name, offset = selectorScanName(source, start)
			if offset < len(source) && source[offset] == '(' {
				return nil, selectorUnsupported("functions are not supported", start)
			}
			token.kind = "literal"
			switch name {
			case "true":
				token.literal = true
			case "false":
				token.literal = false
			case "null":
				token.literal = nil
			default:
				return nil, selectorSyntax("unexpected name "+strconv.Quote(name), start)
			}
		default:
			pair := ""
			if offset+1 < len(source) {
				pair = source[offset : offset+2]
			}
			switch pair {
			case "==", "!=", "<=", ">=", "&&", "||":
				token.kind = pair
				offset += 2
			case "=~":
				return nil, selectorUnsupported("regular expressions are not supported", offset)
			}
			if token.kind != "" {
				break
			}
			switch character {
			case '!', '<', '>', '$', '[', ']', '?', '(', ')':
				token.kind = string(character)
			case '*', ':':
				return nil, selectorUnsupported("wildcards and array selectors are not supported", offset)
			case '.':
				return nil, selectorUnsupported("recursive descent and nested-value selection are not supported", offset)
			case ',':
				return nil, selectorUnsupported("joins and unions are not supported", offset)
			case '/':
				return nil, selectorUnsupported("regular expressions are not supported", offset)
			case '{', '}':
				return nil, selectorUnsupported("object and array literals are not supported", offset)
			default:
				return nil, selectorSyntax("unexpected character", offset)
			}
			offset++
		}
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return append(tokens, selectorToken{kind: "end", offset: len(source)}), nil
}

func selectorScanPath(source string, start int) ([]selectorSegment, int, error) {
	var segments []selectorSegment
	offset := start + 1
	for {
		next := offset
		for next < len(source) && selectorSpace(source[next]) {
			next++
		}
		if next >= len(source) {
			break
		}
		switch source[next] {
		case '.':
			if next+1 < len(source) && (source[next+1] == '.' || source[next+1] == '*') {
				return nil, 0, selectorUnsupported("recursive descent and member wildcards are not supported", next)
			}
			if !selectorNameFirst(source, next+1) {
				return nil, 0, selectorSyntax("expected a dot-member name", next+1)
			}
			name, end := selectorScanName(source, next+1)
			segments = append(segments, selectorSegment{name: name})
			offset = end
		case '[':
			segment, end, err := selectorScanBracket(source, next)
			if err != nil {
				return nil, 0, err
			}
			segments = append(segments, segment)
			offset = end
		default:
			return segments, offset, nil
		}
	}
	return segments, offset, nil
}

func selectorScanBracket(source string, start int) (selectorSegment, int, error) {
	offset := start + 1
	if offset >= len(source) {
		return selectorSegment{}, 0, selectorSyntax("expected a quoted member name or canonical index selector", offset)
	}
	character := source[offset]
	switch {
	case character == '?' || character == '*' || character == ':' || character == ',':
		return selectorSegment{}, 0, selectorUnsupported("nested filters, wildcards, slices, and unions are not supported", start)
	case character == '"' || character == '\'':
		name, next, err := selectorScanString(source, offset)
		if err != nil {
			return selectorSegment{}, 0, err
		}
		if next < len(source) && source[next] == ',' {
			return selectorSegment{}, 0, selectorUnsupported("joins and unions are not supported", next)
		}
		if next >= len(source) || source[next] != ']' {
			return selectorSegment{}, 0, selectorSyntax("expected closing ']' after name selector", next)
		}
		return selectorSegment{name: name}, next + 1, nil
	case character == '-' || selectorDigit(character):
		next := offset
		if source[next] == '-' {
			next++
		}
		for next < len(source) && selectorDigit(source[next]) {
			next++
		}
		if next < len(source) && (source[next] == ':' || source[next] == ',') {
			return selectorSegment{}, 0, selectorUnsupported("array slices and unions are not supported", next)
		}
		raw := source[offset:next]
		if !selectorIndex.MatchString(raw) || next >= len(source) || source[next] != ']' {
			return selectorSegment{}, 0, selectorSyntax("invalid canonical index selector", offset)
		}
		index, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || index < -9007199254740991 || index > 9007199254740991 {
			return selectorSegment{}, 0, selectorSyntax("index selector is outside the I-JSON exact integer range", offset)
		}
		return selectorSegment{index: index, isIndex: true}, next + 1, nil
	}
	return selectorSegment{}, 0, selectorSyntax("expected a quoted member name or canonical index selector", offset)
}

func selectorScanName(source string, start int) (string, int) {
	offset := start
	for offset < len(source) && selectorNameCharacter(source, offset) {
		_, width := utf8.DecodeRuneInString(source[offset:])
		offset += width
	}
	return source[start:offset], offset
}
func selectorSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
func selectorDigit(c byte) bool { return c >= '0' && c <= '9' }
func selectorNameFirst(source string, offset int) bool {
	if offset >= len(source) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(source[offset:])
	return r >= 'A' && r <= 'Z' || r == '_' || r >= 'a' && r <= 'z' || r >= 0x80
}
func selectorNameCharacter(source string, offset int) bool {
	return offset < len(source) && (selectorDigit(source[offset]) || selectorNameFirst(source, offset))
}

func selectorScanNumber(source string, start int) (float64, int, error) {
	raw := selectorJSONNumber.FindString(source[start:])
	if raw == "" {
		return 0, 0, selectorSyntax("invalid JSON number literal", start)
	}
	next := start + len(raw)
	if selectorNameCharacter(source, next) || next < len(source) && source[next] == '.' {
		return 0, 0, selectorSyntax("invalid JSON number literal", start)
	}
	number, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
		return 0, 0, selectorSyntax("JSON number literal is outside the finite range", start)
	}
	return number, next, nil
}

func selectorScanString(source string, start int) (string, int, error) {
	quote := source[start]
	var value strings.Builder
	for offset := start + 1; offset < len(source); {
		character, width := utf8.DecodeRuneInString(source[offset:])
		if character == rune(quote) {
			return value.String(), offset + 1, nil
		}
		if character < 0x20 {
			return "", 0, selectorSyntax("invalid JSONPath string literal", offset)
		}
		if character != '\\' {
			value.WriteRune(character)
			offset += width
			continue
		}
		if offset+1 >= len(source) {
			return "", 0, selectorSyntax("invalid JSONPath string escape", offset)
		}
		escaped := source[offset+1]
		switch escaped {
		case 'b':
			value.WriteByte('\b')
		case 'f':
			value.WriteByte('\f')
		case 'n':
			value.WriteByte('\n')
		case 'r':
			value.WriteByte('\r')
		case 't':
			value.WriteByte('\t')
		case '/', '\\':
			value.WriteByte(escaped)
		default:
			if escaped == quote {
				value.WriteByte(quote)
				offset += 2
				continue
			}
			if escaped != 'u' {
				return "", 0, selectorSyntax("invalid JSONPath string escape", offset)
			}
			first, next, err := selectorScanUnicode(source, offset)
			if err != nil {
				return "", 0, err
			}
			if first >= 0xd800 && first <= 0xdbff {
				second, end, err := selectorScanUnicode(source, next)
				if err != nil || second < 0xdc00 || second > 0xdfff {
					return "", 0, selectorSyntax("unpaired high surrogate escape", offset)
				}
				value.WriteRune(utf16.DecodeRune(first, second))
				offset = end
				continue
			}
			if first >= 0xdc00 && first <= 0xdfff {
				return "", 0, selectorSyntax("unpaired low surrogate escape", offset)
			}
			value.WriteRune(first)
			offset = next
			continue
		}
		offset += 2
	}
	return "", 0, selectorSyntax("unterminated JSONPath string literal", start)
}
func selectorScanUnicode(source string, start int) (rune, int, error) {
	if start+6 > len(source) || source[start:start+2] != "\\u" {
		return 0, 0, selectorSyntax("invalid Unicode escape", start)
	}
	number, err := strconv.ParseUint(source[start+2:start+6], 16, 16)
	if err != nil {
		return 0, 0, selectorSyntax("invalid Unicode escape", start)
	}
	return rune(number), start + 6, nil
}
