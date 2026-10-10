package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func graphHTTPTestResponse(method string, requestHeaders http.Header, status int, body []byte, contentType, etag string, responseHeaders http.Header) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://example.test/team/beads/a", nil)
	request.Header = requestHeaders.Clone()
	response := httptest.NewRecorder()
	graphReadResponse(response, request, status, body, contentType, etag, responseHeaders)
	return response
}

// Cases preserve the pinned BDP reference's negotiation and validator matrix.
func TestGraphReadHTTPAccept(t *testing.T) {
	cases := []struct {
		accept string
		status int
	}{
		{"*/*", 200}, {"application/*", 200}, {"APPLICATION/JSON", 200},
		{"text/html, application/json;q=0.001", 200},
		{"application/json;q=0, */*;q=1", 406}, {"application/*;q=0, */*;q=1", 406},
		{"application/json;q=1, application/*;q=0", 200}, {"image/png", 406}, {"", 406},
		{"application/json;q=0.000", 406}, {"application/json;q=1.000", 200},
		{"application/json;Q=0", 406}, {"application/json;q=0.0001", 406}, {"application/json;q=1.1", 406},
		{`application/json;q="1"`, 406}, {`application/json;profile="a,b;c"`, 406},
		{`application/json;profile="a,b;c", */*;q=0.5`, 200},
		{`text/plain;note="\",application/json", image/png`, 406},
		{", , application/json ; ; q=1. ,", 200}, {"application/json-seq", 406},
		{"application/json;charset=utf-8", 406}, {"application/json; charset=UTF-8", 406},
		{"application/json;q=0.9;charset=utf-8", 406}, {"application/json;charset=utf-8, */*;q=0.8", 200},
		{"application/json;q=0, application/json;q=0.1", 200},
		{"application/json;q=1;q=0", 406}, {"application/json;q=.5", 406},
		{`application/json;broken="never closed`, 406},
	}
	for _, tc := range cases {
		t.Run(tc.accept, func(t *testing.T) {
			response := graphHTTPTestResponse("GET", http.Header{"Accept": {tc.accept}}, 200, []byte(`{"revision":"rev-1"}`), "application/json", `"rev-1"`, nil)
			if response.Code != tc.status {
				t.Fatalf("want %d, got %d", tc.status, response.Code)
			}
			if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Vary") != "Accept" {
				t.Fatal(response.Header())
			}
			if tc.status == 406 {
				if response.Body.Len() != 0 || response.Header().Get("Content-Type") != "" || response.Header().Get("ETag") != "" || response.Header().Get("Content-Length") != "0" {
					t.Fatal("406 leaked representation", response.Header(), response.Body.String())
				}
			} else if response.Header().Get("ETag") != `"rev-1"` {
				t.Fatal("validator lost")
			}
		})
	}
	absent := graphHTTPTestResponse("GET", nil, 200, []byte(`{}`), "application/json", "", nil)
	if absent.Code != 200 {
		t.Fatal("absent Accept refused")
	}
	repeated := graphHTTPTestResponse("GET", http.Header{"Accept": {"application/json;q=0", "*/*;q=1"}}, 200, []byte(`{}`), "application/json", "", nil)
	if repeated.Code != 406 {
		t.Fatal("repeated field lines were not combined")
	}
}

func TestGraphReadHTTPConditionalTags(t *testing.T) {
	cases := []struct {
		headers http.Header
		status  int
	}{
		{http.Header{"If-Match": {`"rev-1"`}}, 200},
		{http.Header{"If-Match": {`W/"rev-1"`}}, 412},
		{http.Header{"If-Match": {`w/"rev-1"`}}, 412},
		{http.Header{"If-None-Match": {`w/"rev-1"`}}, 200},
		{http.Header{"If-None-Match": {`"rev1\", "rev-1"`}}, 304},
		{http.Header{"If-Match": {`"different", "rev-1"`}}, 200},
		{http.Header{"If-Match": {"*"}}, 200},
		{http.Header{"If-Match": {`"missing"`}, "If-None-Match": {"*"}}, 412},
		{http.Header{"If-Match": {`"rev-1"`}, "If-None-Match": {`W/"rev-1"`}}, 304},
		{http.Header{"If-None-Match": {`"rev-1"`}}, 304},
		{http.Header{"If-None-Match": {`W/"rev-1"`}}, 304},
		{http.Header{"If-None-Match": {`"other", W/"rev-1"`}}, 304},
		{http.Header{"If-None-Match": {"*"}}, 304},
		{http.Header{"If-None-Match": {`, , W/"rev-1", ,`}}, 304},
		{http.Header{"If-None-Match": {`"other"`}}, 200},
		{http.Header{"If-Match": {"rev-1"}}, 412},
		{http.Header{"If-None-Match": {"rev-1"}}, 200},
		{http.Header{"If-None-Match": {`*, "rev-1"`}}, 200},
		{http.Header{"If-Match": {`*, "rev-1"`}}, 412},
		{http.Header{"If-Match": {`"rev-1" trailing`}}, 412},
		{http.Header{"If-None-Match": {`"rev-1" trailing`}}, 200},
		{http.Header{"If-Match": {`"other"`, `"rev-1"`}}, 200},
		{http.Header{"If-None-Match": {`"other"`, `W/"rev-1"`}}, 304},
		{http.Header{"If-Match": {""}}, 412},
		{http.Header{"If-None-Match": {""}}, 200},
	}
	for index, tc := range cases {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			response := graphHTTPTestResponse("GET", tc.headers, 200, []byte(`{}`), "application/json", `"rev-1"`, nil)
			if response.Code != tc.status {
				t.Fatalf("%v: want %d, got %d", tc.headers, tc.status, response.Code)
			}
			if response.Header().Get("ETag") != `"rev-1"` {
				t.Fatal("validator lost")
			}
			if tc.status != 200 && (response.Body.Len() != 0 || response.Header().Get("Content-Type") != "") {
				t.Fatal("conditional response has payload")
			}
			if tc.status == 304 && response.Header().Get("Content-Length") != "" {
				t.Fatal("invented 304 length")
			}
		})
	}
}

func TestGraphReadHTTPTagOpaqueBytes(t *testing.T) {
	for _, opaque := range []string{"part,one", `part\one`, "", "r\xe9v", "rév"} {
		current := `"` + opaque + `"`
		response := graphHTTPTestResponse("GET", http.Header{"If-None-Match": {`"other", W/` + current}}, 200, []byte(`{}`), "application/json", current, nil)
		if response.Code != 304 {
			t.Fatalf("opaque %q did not match", opaque)
		}
	}
	for _, tc := range []struct {
		field  string
		status int
	}{{"If-Match", 412}, {"If-None-Match", 304}} {
		response := graphHTTPTestResponse("GET", http.Header{tc.field: {`"rev"`}}, 200, []byte(`{}`), "application/json", `W/"rev"`, nil)
		if response.Code != tc.status {
			t.Fatal("weak current validator treated as strong")
		}
	}
	for _, bad := range []string{"\"rev-1\"\x01", "\"rev\x7f-1\"", "\"unterminated", `W/`, `"rev-1" "other"`} {
		response := graphHTTPTestResponse("GET", http.Header{"If-None-Match": {bad}}, 200, []byte(`{}`), "application/json", `"rev-1"`, nil)
		if response.Code != 200 {
			t.Fatal("malformed tag matched", bad)
		}
	}
}

func TestGraphReadHTTPAbsentValidatorsAndScope(t *testing.T) {
	cases := []struct {
		headers http.Header
		status  int
	}{
		{nil, 200}, {http.Header{"If-Match": {"*"}}, 200},
		{http.Header{"If-Match": {`"specific"`}}, 412},
		{http.Header{"If-None-Match": {"*"}}, 304},
		{http.Header{"If-None-Match": {`"specific"`}}, 200},
		{http.Header{"If-Match": {`"specific"`}, "If-None-Match": {"*"}}, 412},
	}
	for _, scope := range []bool{false, true} {
		for _, method := range []string{"GET", "HEAD"} {
			for _, tc := range cases {
				status, want, contentType, body := 200, tc.status, "application/json", []byte(`{}`)
				headers := tc.headers.Clone()
				if headers == nil {
					headers = make(http.Header)
				}
				if scope {
					status = 204
					contentType = ""
					body = nil
					headers.Set("Accept", "image/png")
					if want == 200 {
						want = 204
					}
				}
				response := graphHTTPTestResponse(method, headers, status, body, contentType, "", http.Header{"Link": {`<https://example.test/team/bdp.json>; rel="service-desc"; type="application/json"`}})
				if response.Code != want {
					t.Fatalf("scope=%t headers=%v: want%d got%d", scope, headers, want, response.Code)
				}
				if scope && (response.Header().Get("Vary") != "" || response.Body.Len() != 0 || response.Header().Get("Link") == "") {
					t.Fatal("Scope probe metadata changed")
				}
				if (want == 204 || want == 304) && response.Header().Get("Content-Length") != "" {
					t.Fatal("invented zero-length representation")
				}
			}
		}
	}
}

func TestGraphReadHTTPOrdinaryFailuresBeforeNegotiation(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 405, 409, 410, 429, 503} {
		payload := []byte(`{"code":"ordinary-refusal"}`)
		headers := http.Header{"Accept": {"image/png"}, "If-None-Match": {"*"}, "If-Match": {`"absent"`}}
		for _, method := range []string{"GET", "HEAD"} {
			response := graphHTTPTestResponse(method, headers, status, payload, "application/problem+json", "", http.Header{"Allow": {"GET, HEAD"}, "Retry-After": {"1"}})
			if response.Code != status || response.Header().Get("Content-Type") != "application/problem+json" || response.Header().Get("Content-Length") != strconv.Itoa(len(payload)) {
				t.Fatal("ordinary refusal replaced", response.Code, response.Header())
			}
			if method == "HEAD" && response.Body.Len() != 0 {
				t.Fatal("HEAD refusal has body")
			}
			if method == "GET" && response.Body.String() != string(payload) {
				t.Fatal("ordinary body changed")
			}
			if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Allow") != "GET, HEAD" || response.Header().Get("Retry-After") != "1" {
				t.Fatal("context lost")
			}
		}
	}
	for _, status := range []int{406, 412, 500} {
		response := graphHTTPTestResponse("GET", nil, status, []byte(`{"internal":"must not escape"}`), "application/problem+json", "", nil)
		if response.Body.Len() != 0 || response.Header().Get("Content-Type") != "" || response.Header().Get("Content-Length") != "0" {
			t.Fatal("transport-only failure got BDP body")
		}
	}
}

func TestGraphReadHTTPHEADAndHeaderOwnership(t *testing.T) {
	payload := []byte(`{"text":"café — 雨"}`)
	extra := http.Header{"Vary": {"Origin"}, "Content-Length": {"999"}, "Cache-Control": {"public"}, "Bdp-Scope": {"scope-context"}}
	original := extra.Clone()
	conditions := []http.Header{nil, {"Accept": {"image/png"}}, {"If-None-Match": {"*"}}, {"If-Match": {`"missing"`}}}
	for _, condition := range conditions {
		get := graphHTTPTestResponse("GET", condition, 200, payload, "application/json", `"rev"`, extra)
		head := graphHTTPTestResponse("HEAD", condition, 200, payload, "application/json", `"rev"`, extra)
		if get.Code != head.Code || !reflect.DeepEqual(get.Header(), head.Header()) || head.Body.Len() != 0 {
			t.Fatal("HEAD differs from GET metadata", get.Header(), head.Header())
		}
		if get.Code == 200 && head.Header().Get("Content-Length") != strconv.Itoa(len(payload)) {
			t.Fatal("HEAD length is not serialized UTF-8 bytes")
		}
	}
	if !reflect.DeepEqual(extra, original) {
		t.Fatal("mutated caller headers")
	}
	for _, vary := range []string{"*", "Origin, accept", "Accept"} {
		response := graphHTTPTestResponse("GET", nil, 200, payload, "application/json", "", http.Header{"Vary": {vary}})
		if response.Header().Get("Vary") != vary {
			t.Fatal("duplicated Vary Accept", response.Header())
		}
	}
}

func TestGraphReadHTTPDatesDoNotInventModificationTime(t *testing.T) {
	response := graphHTTPTestResponse("GET", http.Header{"If-Modified-Since": {"Wed, 01 Jan 2100 00:00:00 GMT"}, "If-Unmodified-Since": {"Sat, 01 Jan 2000 00:00:00 GMT"}}, 200, []byte(`{}`), "application/json", "", nil)
	if response.Code != 200 || response.Header().Get("Last-Modified") != "" {
		t.Fatal("invented modification date")
	}
}

func TestGraphReadHTTPActualTransport(t *testing.T) {
	payload := []byte(`{"text":"雨"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		graphReadResponse(w, r, 200, payload, "application/json", `"revision"`, nil)
	}))
	defer server.Close()
	for _, method := range []string{"GET", "HEAD"} {
		for _, condition := range []http.Header{nil, {"If-None-Match": {"*"}}, {"Accept": {"image/png"}}} {
			request, err := http.NewRequest(method, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header = condition.Clone()
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
			want := 200
			if condition.Get("If-None-Match") != "" {
				want = 304
			}
			if condition.Get("Accept") != "" {
				want = 406
			}
			if response.StatusCode != want {
				t.Fatal(response.Status)
			}
			if method == "HEAD" || want != 200 {
				if len(body) != 0 {
					t.Fatal("transport leaked body")
				}
			} else if string(body) != string(payload) {
				t.Fatal("transport changed bytes")
			}
			if want == 200 && response.ContentLength != int64(len(payload)) {
				t.Fatal("wire length mismatch")
			}
			if want == 304 && response.Header.Get("Content-Length") != "" {
				t.Fatal("304 transport invented length")
			}
			if !strings.Contains(response.Header.Get("Vary"), "Accept") {
				t.Fatal("missing negotiation metadata")
			}
		}
	}
}
