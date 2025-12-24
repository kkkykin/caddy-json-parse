package jsonparse

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func provision(t *testing.T, p caddy.Provisioner) {
	t.Helper()
	ctx, cancel := caddy.NewContext(caddy.Context{Context: context.Background()})
	t.Cleanup(cancel)
	if err := p.Provision(ctx); err != nil {
		t.Fatal(err)
	}
}

func newRequest(body string) (*http.Request, *caddy.Replacer) {
	r := httptest.NewRequest(http.MethodPost, "/jsonrpc", strings.NewReader(body))
	repl := caddy.NewReplacer()
	ctx := context.WithValue(r.Context(), caddy.ReplacerCtxKey, repl)
	ctx = context.WithValue(ctx, caddyhttp.VarsCtxKey, make(map[string]any))
	return r.WithContext(ctx), repl
}

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func assertJSON(t *testing.T, got, want string) {
	t.Helper()
	actual, err := decodeJSON([]byte(got))
	if err != nil {
		t.Fatalf("invalid result JSON: %v; body=%s", err, got)
	}
	expected, err := decodeJSON([]byte(want))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("got %s; want %s", got, want)
	}
}

func assertStatus(t *testing.T, err error, want int) {
	t.Helper()
	if want == 0 {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var handlerErr caddyhttp.HandlerError
	if !errors.As(err, &handlerErr) || handlerErr.StatusCode != want {
		t.Fatalf("got error %v; want HTTP %d", err, want)
	}
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) {
	return copy(p, "{}"), errors.New("read failed")
}

func TestJSONParseBodyHandling(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		strict bool
		limit  int64
		status int
	}{
		{"valid", " { \"id\": 9007199254740993 } \n", true, 0, 0},
		{"empty lenient", "", false, 0, 0},
		{"invalid lenient", "not JSON", false, 0, 0},
		{"trailing lenient", "{} {}", false, 0, 0},
		{"empty strict", "", true, 0, 400},
		{"invalid strict", "{", true, 0, 400},
		{"trailing strict", "{} {}", true, 0, 400},
		{"trailing garbage", "{} garbage", true, 0, 400},
		{"limit even when lenient", "12345", false, 4, 413},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := &JSONParse{Strict: tt.strict, MaxSize: tt.limit}
			provision(t, j)
			r, _ := newRequest(tt.body)
			original := &trackedBody{Reader: strings.NewReader(tt.body)}
			r.Body, r.ContentLength = original, -1
			called := false
			err := j.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) error {
				called = true
				if body := readBody(t, r); body != tt.body {
					t.Errorf("original bytes changed: %q", body)
				}
				if r.ContentLength != -1 {
					t.Error("parser changed request framing")
				}
				return nil
			}))
			assertStatus(t, err, tt.status)
			if called != (tt.status == 0) {
				t.Errorf("downstream called=%v", called)
			}
			if !original.closed {
				t.Error("original body was not closed")
			}
		})
	}
}

func TestJSONParseReadFailureIsNotForwarded(t *testing.T) {
	j := &JSONParse{}
	provision(t, j)
	r, _ := newRequest("")
	r.Body = &trackedBody{Reader: failingReader{}}
	err := j.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
		t.Error("forwarded a partial body")
		return nil
	}))
	assertStatus(t, err, http.StatusBadRequest)
}

func TestSharedDocumentAndPlaceholders(t *testing.T) {
	parse := &JSONParse{Strict: true}
	transform := &JSONTransform{JQ: `.state = "new" | .items = [10,20] | .id += 1`}
	replaceRoot := &JSONTransform{JQ: `[.state, .id]`}
	provision(t, parse)
	provision(t, transform)
	provision(t, replaceRoot)
	r, repl := newRequest(`{"state":"old","items":[1,2,3],"id":9007199254740993}`)
	w := httptest.NewRecorder()
	next := caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error { return nil })
	if err := parse.ServeHTTP(w, r, next); err != nil {
		t.Fatal(err)
	}
	if got, _ := repl.Get("json.state"); got != "old" {
		t.Fatalf("initial placeholder=%v", got)
	}
	// Read a soon-to-be-removed index before changing the array.
	repl.Get("json.items.2")
	doc := r.Context().Value(documentKey{}).(*document)
	if err := transform.ServeHTTP(w, r, next); err != nil {
		t.Fatal(err)
	}
	if got, _ := repl.Get("json.state"); got != "new" {
		t.Errorf("stale placeholder=%v", got)
	}
	if got, _ := repl.Get("json.items.2"); got != nil {
		t.Errorf("stale array index=%v", got)
	}
	if got := repl.ReplaceAll("{json.id}", ""); got != "9007199254740994" {
		t.Errorf("integer placeholder=%s", got)
	}
	body := r.Body
	if err := parse.ServeHTTP(w, r, next); err != nil {
		t.Fatal(err)
	}
	if r.Body != body || r.Context().Value(documentKey{}) != doc {
		t.Error("second parser did not reuse the document")
	}
	if err := replaceRoot.ServeHTTP(w, r, next); err != nil {
		t.Fatal(err)
	}
	if got, _ := repl.Get("json.0"); got != "new" {
		t.Errorf("root replacement not visible: %v", got)
	}
	assertJSON(t, readBody(t, r), `["new",9007199254740994]`)

	// A different middleware can replace the request body between JSON handlers.
	r.Body = io.NopCloser(strings.NewReader(`{"state":"external"}`))
	r.ContentLength = -1
	if err := parse.ServeHTTP(w, r, next); err != nil {
		t.Fatal(err)
	}
	if got, _ := repl.Get("json.state"); got != "external" {
		t.Errorf("replacement body was not parsed: %v", got)
	}
}

func TestCachedDocumentStillHonorsSizeLimit(t *testing.T) {
	parse := &JSONParse{}
	transform := &JSONTransform{JQ: ".", MaxSize: 4}
	provision(t, parse)
	provision(t, transform)
	r, _ := newRequest(`{"a":1}`)
	err := parse.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		return transform.ServeHTTP(w, r, caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
			t.Error("cached document bypassed max_size")
			return nil
		}))
	}))
	assertStatus(t, err, http.StatusRequestEntityTooLarge)
}
