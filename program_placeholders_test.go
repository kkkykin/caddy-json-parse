package jsonparse

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func TestJSONTransformPlaceholderStrings(t *testing.T) {
	t.Setenv("CADDY_JSON_PARSE_TEST_EMPTY", "")
	tests := []struct{ name, query, input, want string }{
		{"embedded and repeated", `.out = "prefix {test.value}/{test.value}"`, `{}`, `{"out":"prefix value/value"}`},
		{"keys and indexes", `{"{test.key}":"{test.value}"} | ."{test.key}" += "!" | .["{test.key}"]`, `{}`, `"value!"`},
		{"key shorthand", `{"{test.key}"}`, `{"field":"from-body"}`, `{"field":"from-body"}`},
		{"destructuring", `. as {"{test.key}": $v} | ["{test.value}", $v]`, `{"field":7}`, `["value",7]`},
		{"jq interpolation", `"before-{test.value}-\(.id)-\("{test.value}" | ascii_upcase)-after"`, `{"id":7}`, `"before-value-7-VALUE-after"`},
		{"regex and literal braces", `.ok = (.name | test("^[a-z]{2,4}$")) | .literal = "{} {unknown} {2,4}" | .out = "{test.value}"`,
			`{"name":"abc"}`, `{"name":"abc","ok":true,"literal":"{} {unknown} {2,4}","out":"value"}`},
		{"escaped braces", `["\\{test.value\\}", "{test.value}", "\\{unclosed", "closing\\}"]`, `{}`, `["{test.value}","value","{unclosed","closing}"]`},
		{"formatted string prefix", `@uri "{test.url}/\(.part)"`, `{"part":"a b"}`, `"https://example.com/a/b/a%20b"`},
		{"formatted literal", `@uri "{test.url}"`, `{}`, `"https://example.com/a/b"`},
		{"formatted jq interpolation", `@uri "\("{test.url}")"`, `{}`, `"https%3A%2F%2Fexample.com%2Fa%2Fb"`},
		{"function and reduce", `def add_value: . + "{test.value}"; if .enabled then reduce .items[] as $x (""; . + ($x | add_value)) else "{test.value}" end`,
			`{"enabled":true,"items":["a","b"]}`, `"avaluebvalue"`},
		{"foreach", `[foreach .[] as $x (""; . + "{test.value}"; .)]`, `[1,2]`, `["value","valuevalue"]`},
		{"try catch", `try error("fail") catch "{test.value}"`, `{}`, `"value"`},
		{"variable collision", `"local" as $__caddy_placeholder0 | [$__caddy_placeholder0, "{test.value}"]`, `{}`, `["local","value"]`},
		{"number and empty env", `{n:("{test.number}" | tonumber), missing:"{env.CADDY_JSON_PARSE_TEST_EMPTY}"}`, `{}`, `{"n":42,"missing":""}`},
		{"request body stays literal", `.echo = .input | .out = "{test.value}"`, `{"input":"{test.value}"}`, `{"input":"{test.value}","echo":"{test.value}","out":"value"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := &JSONTransform{JQ: tt.query}
			provision(t, j)
			r, repl := newRequest(tt.input)
			repl.Set("test.value", "value")
			repl.Set("test.key", "field")
			repl.Set("test.url", "https://example.com/a/b")
			repl.Set("test.number", 42)
			err := j.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) error {
				assertJSON(t, readBody(t, r), tt.want)
				return nil
			}))
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestJSONTransformPlaceholdersPerRequest(t *testing.T) {
	const envName = "CADDY_JSON_PARSE_TEST_RUNTIME"
	t.Setenv(envName, "at-provision")
	j := &JSONTransform{JQ: `.remote = "{http.request.remote.host}" | .env = "{env.CADDY_JSON_PARSE_TEST_RUNTIME}" | .header = "{http.request.header.X-Value}"`}
	provision(t, j)
	for _, tt := range []struct{ remote, env, header string }{
		{"203.0.113.7", "first-request", "first-header"},
		{"203.0.113.8", "quotes: \" backslash: \\ newline:\n tab:\t unicode: 中文\u2028", `" | error("injected") # \(.value) {env.CADDY_JSON_PARSE_TEST_RUNTIME}`},
	} {
		t.Setenv(envName, tt.env)
		r, _ := newRequest(`{"original":true}`)
		r.RemoteAddr = tt.remote + ":54321"
		r.Header.Set("X-Value", tt.header)
		caddyhttp.NewTestReplacer(r)
		want, err := json.Marshal(map[string]any{"original": true, "remote": tt.remote, "env": tt.env, "header": tt.header})
		if err != nil {
			t.Fatal(err)
		}
		err = j.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) error {
			assertJSON(t, readBody(t, r), string(want))
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestJSONTransformUsesJSONPlaceholders(t *testing.T) {
	parse := &JSONParse{Strict: true}
	j := &JSONTransform{JQ: `.original = "{json.state}" | .state = "new"`}
	provision(t, parse)
	provision(t, j)
	r, repl := newRequest(`{"state":"old"}`)
	err := parse.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		return j.ServeHTTP(w, r, caddyhttp.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) error {
			assertJSON(t, readBody(t, r), `{"original":"old","state":"new"}`)
			if state, _ := repl.Get("json.state"); state != "new" {
				t.Errorf("stale placeholder: %v", state)
			}
			return nil
		}))
	}))
	if err != nil {
		t.Fatal(err)
	}
}
