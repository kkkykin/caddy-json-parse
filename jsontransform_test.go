package jsonparse

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func TestJSONTransform(t *testing.T) {
	tests := []struct{ name, query, input, want string }{
		{"create parents", `.user.name = "Ada"`, `{}`, `{"user":{"name":"Ada"}}`},
		{"unusual keys", `.["a.b"]["100"] = "new"`, `{"a.b":{"100":"old"}}`, `{"a.b":{"100":"new"}}`},
		{"delete all elements", `del(.items[])`, `{"items":[1,2,3,4]}`, `{"items":[]}`},
		{"empty object", `.options = {}`, `{}`, `{"options":{}}`},
		{"null keys", `.target += {"nested":{"new":null}}`, `{"target":{"nested":{"old":null}}}`, `{"target":{"nested":{"new":null}}}`},
		{"sequential condition", `.state = "new" | if .state == "new" then .flag = true else . end`, `{"state":"old"}`, `{"state":"new","flag":true}`},
		{"null is a result", `null`, `{}`, `null`},
		{"number precision", `.changed = true`,
			`{"id":9007199254740993,"huge":123456789012345678901234567890,"decimal":0.123456789012345678901,"exponent":1e1000}`,
			`{"id":9007199254740993,"huge":123456789012345678901234567890,"decimal":0.123456789012345678901,"exponent":1e1000,"changed":true}`},
		{"integer arithmetic", `.n += 1`, `{"n":123456789012345678901234567890}`, `{"n":123456789012345678901234567891}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := &JSONTransform{JQ: tt.query}
			provision(t, j)
			r, _ := newRequest(tt.input)
			r.ContentLength = -1
			r.TransferEncoding = []string{"chunked"}
			r.Header.Set("Transfer-Encoding", "chunked")
			called := false
			err := j.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) error {
				called = true
				body := readBody(t, r)
				assertJSON(t, body, tt.want)
				if r.ContentLength != int64(len(body)) || r.Header.Get("Content-Length") != strconv.Itoa(len(body)) {
					t.Error("incorrect content length")
				}
				if len(r.TransferEncoding) != 0 || r.Header.Get("Transfer-Encoding") != "" {
					t.Error("stale transfer encoding")
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Error("incorrect content type")
				}
				if r.GetBody == nil {
					t.Fatal("missing GetBody")
				}
				replay, err := r.GetBody()
				if err != nil {
					t.Fatal(err)
				}
				defer replay.Close()
				replayed, err := io.ReadAll(replay)
				if err != nil || string(replayed) != body {
					t.Errorf("GetBody returned %q, %v", replayed, err)
				}
				return nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Error("downstream not called")
			}
		})
	}
}

func TestJSONTransformFailureIsAtomic(t *testing.T) {
	tests := []struct {
		name, query string
		limit       int64
		timeout     caddy.Duration
		status      int
	}{
		{"empty", "empty", 0, 0, 500},
		{"multiple", `.state = "new", .`, 0, 0, 500},
		{"error after mutation", `.state = "new" | error("stop")`, 0, 0, 500},
		{"error after result", `.state = "new", error("stop")`, 0, 0, 500},
		{"nonfinite result", `.state = infinite`, 0, 0, 500},
		{"output too large", `.state = ("x" * 100)`, 40, 0, 413},
		{"timeout before result", `def loop: loop; loop`, 0, caddy.Duration(5 * time.Millisecond), 503},
		{"timeout after result", `def loop: loop; .state = "new", loop`, 0, caddy.Duration(5 * time.Millisecond), 503},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parse := &JSONParse{Strict: true}
			j := &JSONTransform{JQ: tt.query, MaxSize: tt.limit, Timeout: tt.timeout}
			provision(t, parse)
			provision(t, j)
			input := `{ "state": "old" }`
			r, repl := newRequest(input)
			next := caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error { return nil })
			if err := parse.ServeHTTP(httptest.NewRecorder(), r, next); err != nil {
				t.Fatal(err)
			}
			repl.Get("json.state")
			err := j.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
				t.Error("forwarded a failed transform")
				return nil
			}))
			assertStatus(t, err, tt.status)
			if body := readBody(t, r); body != input {
				t.Errorf("failed transform changed body: %s", body)
			}
			if state, _ := repl.Get("json.state"); state != "old" {
				t.Errorf("failed transform changed placeholder: %v", state)
			}
			if r.ContentLength != int64(len(input)) || r.Header.Get("Content-Length") != "" {
				t.Error("failed transform changed headers")
			}
		})
	}
}

func TestJSONTransformRejectsInvalidInput(t *testing.T) {
	for _, input := range []string{"", " ", "no JSON", "{} {}", "{}x"} {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			j := &JSONTransform{JQ: "."}
			provision(t, j)
			r, _ := newRequest(input)
			err := j.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
				t.Error("forwarded invalid JSON")
				return nil
			}))
			assertStatus(t, err, http.StatusBadRequest)
		})
	}
}

func TestJSONTransformInputLimit(t *testing.T) {
	for _, knownLength := range []bool{true, false} {
		t.Run(strconv.FormatBool(knownLength), func(t *testing.T) {
			j := &JSONTransform{JQ: ".", MaxSize: 4}
			provision(t, j)
			r, _ := newRequest("12345")
			if !knownLength {
				r.ContentLength = -1
			}
			err := j.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
				t.Error("forwarded oversized input")
				return nil
			}))
			assertStatus(t, err, http.StatusRequestEntityTooLarge)
		})
	}
}

func TestJSONTransformCancellation(t *testing.T) {
	j := &JSONTransform{JQ: `def loop: loop; loop`}
	provision(t, j)
	r, _ := newRequest("{}")
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	r = r.WithContext(ctx)
	err := j.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error {
		t.Error("forwarded a cancelled request")
		return nil
	}))
	assertStatus(t, err, http.StatusRequestTimeout)
}

func TestJSONTransformConcurrentRequests(t *testing.T) {
	j := &JSONTransform{JQ: `.items |= map(. + 1) | .seen = .id`}
	provision(t, j)
	for i := range 16 {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			r, _ := newRequest(fmt.Sprintf(`{"id":%d,"items":[1,2,3]}`, i))
			err := j.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) error {
				assertJSON(t, readBody(t, r), fmt.Sprintf(`{"id":%d,"seen":%d,"items":[2,3,4]}`, i, i))
				return nil
			}))
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestJSONTransformDoesNotEnableExternalInputs(t *testing.T) {
	t.Setenv("JSON_TRANSFORM_TEST_SECRET", "private")
	j := &JSONTransform{JQ: `[env, $ENV]`}
	provision(t, j)
	r, _ := newRequest("{}")
	err := j.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) error {
		body := readBody(t, r)
		if strings.Contains(body, "private") {
			t.Error("jq inherited the process environment")
		}
		assertJSON(t, body, `[{},{}]`)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
}
