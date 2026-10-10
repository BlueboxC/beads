package httpapi

// HTTP media and entity-tag rules are adapted from gastownhall/bdp
// packages/server/src/read-http.ts at 5b4eb46c7f21f2272f4da5a230f042ce753cb304.
// The caller must finish routing, authorization, storage access and wire
// validation before invoking this finalizer. This Read surface has no actual
// representation modification date; Resource timestamps are not HTTP dates.

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// graphReadResponse applies success-only HTTP negotiation and preconditions,
// then writes GET metadata with no payload for HEAD. Ordinary refusals retain
// precedence. The caller supplies complete serialized bytes, never a streaming
// body, and must not advertise Last-Modified without extending this contract.
func graphReadResponse(w http.ResponseWriter, r *http.Request, status int, body []byte, contentType, etag string, headers http.Header) {
	out := w.Header()
	for key, values := range headers {
		out[key] = append([]string(nil), values...)
	}
	out.Set("Cache-Control", "private, no-store")
	out.Del("Content-Length")
	out.Del("Content-Type")
	out.Del("ETag")
	if contentType != "" {
		out.Set("Content-Type", contentType)
	}
	if etag != "" {
		out.Set("ETag", etag)
	}
	success := status >= 200 && status < 300
	jsonRepresentation := success && status != http.StatusNoContent && contentType == "application/json"
	if jsonRepresentation {
		graphReadVaryAccept(out)
	}
	if success && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		accept, present := graphReadField(r.Header, "Accept")
		if jsonRepresentation && present && !graphReadAcceptsJSON(accept) {
			status = http.StatusNotAcceptable
			out.Del("ETag")
		} else if conditional := graphReadConditionalStatus(r.Header, etag); conditional != http.StatusOK {
			status = conditional
		}
	}
	switch status {
	case http.StatusNoContent, http.StatusNotModified:
		body = nil
		out.Del("Content-Type")
		out.Del("Content-Length")
	case http.StatusNotAcceptable, http.StatusPreconditionFailed, http.StatusInternalServerError:
		body = nil
		out.Del("Content-Type")
		out.Set("Content-Length", "0")
	default:
		if body != nil {
			out.Set("Content-Length", strconv.Itoa(len(body)))
		}
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead && len(body) > 0 {
		//nolint:gosec // G705: callers serialize JSON with fixed application/json or application/problem+json; no HTML sink.
		_, _ = w.Write(body)
	}
}

func graphReadField(headers http.Header, name string) (string, bool) {
	values := headers.Values(name)
	return strings.Join(values, ","), len(values) > 0
}

func graphReadVaryAccept(headers http.Header) {
	value, _ := graphReadField(headers, "Vary")
	for _, field := range strings.Split(value, ",") {
		if strings.TrimSpace(field) == "*" || strings.EqualFold(strings.TrimSpace(field), "Accept") {
			return
		}
	}
	if value != "" {
		value += ", "
	}
	headers.Set("Vary", value+"Accept")
}

func graphReadConditionalStatus(headers http.Header, etag string) int {
	if field, present := graphReadField(headers, "If-Match"); present && !graphReadTagsMatch(field, etag, true) {
		return http.StatusPreconditionFailed
	}
	if field, present := graphReadField(headers, "If-None-Match"); present && graphReadTagsMatch(field, etag, false) {
		return http.StatusNotModified
	}
	return http.StatusOK
}

// Entity-tag backslashes are opaque bytes, not quoted-pair escapes. Validate
// the entire list before accepting any match; malformed trailing syntax and
// mixed wildcard lists cannot accidentally select a not-modified response.
func graphReadTagsMatch(value, current string, strong bool) bool {
	if strings.TrimSpace(value) == "*" {
		return true
	}
	matched := false
	for offset := 0; offset < len(value); {
		for offset < len(value) && (value[offset] == ' ' || value[offset] == '\t' || value[offset] == ',') {
			offset++
		}
		if offset == len(value) {
			break
		}
		start := offset
		if strings.HasPrefix(value[offset:], "W/") {
			offset += 2
		}
		if offset >= len(value) || value[offset] != '"' {
			return false
		}
		offset++
		for offset < len(value) && value[offset] != '"' {
			c := value[offset]
			if c != 0x21 && !(c >= 0x23 && c <= 0x7e) && c < 0x80 {
				return false
			}
			offset++
		}
		if offset == len(value) {
			return false
		}
		offset++
		candidate := value[start:offset]
		if current != "" {
			if strong {
				matched = matched || (!strings.HasPrefix(candidate, "W/") && !strings.HasPrefix(current, "W/") && candidate == current)
			} else {
				matched = matched || strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(current, "W/")
			}
		}
		for offset < len(value) && (value[offset] == ' ' || value[offset] == '\t') {
			offset++
		}
		if offset < len(value) && value[offset] != ',' {
			return false
		}
	}
	return matched
}

var graphReadQuality = regexp.MustCompile(`^(?:0(?:\.[0-9]{0,3})?|1(?:\.0{0,3})?)$`)

func graphReadAcceptsJSON(value string) bool {
	ranges, ok := graphReadSplitQuoted(value, ',')
	if !ok {
		return false
	}
	specificity, quality := -1, float64(0)
	for _, entry := range ranges {
		specific, q, ok := graphReadJSONRange(entry)
		if !ok {
			continue
		}
		if specific > specificity {
			specificity, quality = specific, q
		} else if specific == specificity && q > quality {
			quality = q
		}
	}
	return quality > 0
}

// Parameter-free JSON cannot match a range requiring charset/profile/media
// parameters. Malformed ranges are ignored; no BDP Problem is invented.
func graphReadJSONRange(value string) (int, float64, bool) {
	parts, ok := graphReadSplitQuoted(value, ';')
	if !ok {
		return 0, 0, false
	}
	specificity := -1
	switch strings.ToLower(strings.TrimSpace(parts[0])) {
	case "application/json":
		specificity = 2
	case "application/*":
		specificity = 1
	case "*/*":
		specificity = 0
	}
	if specificity < 0 {
		return 0, 0, false
	}
	quality := float64(1)
	hasWeight, hasMedia := false, false
	for _, part := range parts[1:] {
		parameter := strings.TrimSpace(part)
		if parameter == "" {
			continue
		}
		name, raw, found := strings.Cut(parameter, "=")
		if !found || !graphReadToken(name) || !(graphReadToken(raw) || graphReadQuoted(raw)) {
			return 0, 0, false
		}
		if strings.EqualFold(name, "q") {
			if hasWeight || !graphReadQuality.MatchString(raw) {
				return 0, 0, false
			}
			quality, _ = strconv.ParseFloat(raw, 64)
			hasWeight = true
		} else {
			hasMedia = true
		}
	}
	return specificity, quality, !hasMedia
}

func graphReadToken(value string) bool {
	if value == "" {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("!#$%&'*+.^_`|~-", rune(c)) {
			continue
		}
		return false
	}
	return true
}

func graphReadQuoted(value string) bool {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for i := 1; i < len(value)-1; i++ {
		c := value[i]
		if c == '\\' {
			i++
			if i >= len(value)-1 {
				return false
			}
			c = value[i]
			if c != '\t' && !(c >= 0x20 && c <= 0x7e) && c < 0x80 {
				return false
			}
		} else if c != '\t' && c != 0x20 && c != 0x21 && !(c >= 0x23 && c <= 0x5b) && !(c >= 0x5d && c <= 0x7e) && c < 0x80 {
			return false
		}
	}
	return true
}

// Media quoted-pairs differ from entity tags: commas and semicolons within a
// quoted parameter do not divide ranges or parameters.
func graphReadSplitQuoted(value string, delimiter byte) ([]string, bool) {
	parts := []string{}
	quoted := false
	start := 0
	for i := 0; i < len(value); i++ {
		c := value[i]
		if quoted && c == '\\' {
			i++
		} else if c == '"' {
			quoted = !quoted
		} else if !quoted && c == delimiter {
			parts = append(parts, value[start:i])
			start = i + 1
		}
	}
	if quoted {
		return nil, false
	}
	return append(parts, value[start:]), true
}
