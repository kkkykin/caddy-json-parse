package jsonparse

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
)

func TestTransformCaddyfile(t *testing.T) {
	input := `json_transform {
		jq <<JQ
			.name = "Ada Lovelace"
			| if .name == "Ada Lovelace" then .ok = true else . end
		JQ
		max_size 2MiB
		timeout 250ms
	}`
	var j JSONTransform
	if err := j.UnmarshalCaddyfile(caddyfile.NewTestDispenser(input)); err != nil {
		t.Fatal(err)
	}
	if j.MaxSize != 2<<20 || j.Timeout != caddy.Duration(250*time.Millisecond) {
		t.Fatalf("incorrect config: %+v", j)
	}
	provision(t, &j)
	result, err := runProgram(context.Background(), j.code, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, string(body), `{"name":"Ada Lovelace","ok":true}`)
}

func TestTransformCaddyfilePlaceholders(t *testing.T) {
	const envName = "CADDY_JSON_PARSE_TEST_VALUE"
	t.Setenv(envName, "from-env")
	input := `:8080 {
		json_transform {
			jq <<JQ
				.config = "{$CADDY_JSON_PARSE_TEST_VALUE}"
				| .remote = "{remote_host}"
				| .runtime_env = "{env.CADDY_JSON_PARSE_TEST_VALUE}"
			JQ
		}
	}`
	config, warnings, err := caddyconfig.GetAdapter("caddyfile").Adapt(caddyfile.Format([]byte(input)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("Caddyfile warnings: %v", warnings)
	}

	var adapted any
	if err := json.Unmarshal(config, &adapted); err != nil {
		t.Fatal(err)
	}
	source, ok := findStringField(adapted, "jq")
	if !ok {
		t.Fatalf("adapted config has no jq source: %s", config)
	}
	if !strings.Contains(source, `.config = "from-env"`) {
		t.Fatalf("Caddyfile environment variable was not expanded: %q", source)
	}
	if !strings.Contains(source, `{http.request.remote.host}`) {
		t.Fatalf("remote_host shorthand was not adapted: %q", source)
	}

	j := &JSONTransform{JQ: source}
	provision(t, j)
	r, _ := newRequest(`{}`)
	r.RemoteAddr = "203.0.113.7:54321"
	caddyhttp.NewTestReplacer(r)
	err = j.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) error {
		assertJSON(t, readBody(t, r), `{"config":"from-env","remote":"203.0.113.7","runtime_env":"from-env"}`)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
}

func findStringField(value any, key string) (string, bool) {
	switch value := value.(type) {
	case map[string]any:
		if found, ok := value[key].(string); ok {
			return found, true
		}
		for _, child := range value {
			if found, ok := findStringField(child, key); ok {
				return found, true
			}
		}
	case []any:
		for _, child := range value {
			if found, ok := findStringField(child, key); ok {
				return found, true
			}
		}
	}
	return "", false
}

func TestRejectInvalidTransformConfig(t *testing.T) {
	for _, config := range []string{
		"json_transform",
		"json_transform unexpected",
		"json_transform {\n jq \".\"\n jq_file rules.jq\n}",
		"json_transform {\n jq \".\"\n jq \".a\"\n}",
		"json_transform {\n jq \".\"\n timeout 0\n}",
		"json_transform {\n jq \".\"\n timeout -1s\n}",
		"json_transform {\n jq \".\"\n max_size 0\n}",
		"json_transform {\n jq \".\"\n max_size 999999999999999999999999\n}",
		"json_transform {\n jq \".\"\n set a 1\n}",
	} {
		t.Run(config, func(t *testing.T) {
			var j JSONTransform
			if err := j.UnmarshalCaddyfile(caddyfile.NewTestDispenser(config)); err == nil {
				t.Error("accepted invalid Caddyfile")
			}
		})
	}
	for _, config := range []JSONTransform{
		{},
		{JQ: ".", JQFile: "rules.jq"},
		{JQ: ".["},
		{JQ: "no_such_function"},
		{JQ: ".", Timeout: -1},
		{JQ: ".", MaxSize: -1},
		{JQFile: filepath.Join(t.TempDir(), "missing.jq")},
	} {
		if err := config.Provision(caddy.Context{}); err == nil {
			t.Errorf("accepted invalid JSON config: %+v", config)
		}
	}
}

func TestJQFileIsCompiledAtProvision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "query.jq")
	if err := os.WriteFile(path, []byte(`.version = 1`), 0600); err != nil {
		t.Fatal(err)
	}
	j := &JSONTransform{JQFile: path}
	provision(t, j)
	if err := os.WriteFile(path, []byte(`.version = 2`), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := runProgram(context.Background(), j.code, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["version"] != 1 {
		t.Errorf("running code unexpectedly changed: %v", result)
	}
	for _, source := range []string{"", "  ", ".[", "undefined_function"} {
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		config := JSONTransform{JQFile: path}
		if err := config.Provision(caddy.Context{}); err == nil {
			t.Errorf("accepted invalid jq_file: %q", source)
		}
	}
}

func TestJQFileKeepsPlaceholderStrings(t *testing.T) {
	t.Setenv("CADDY_JSON_PARSE_TEST_VALUE", "from-env")
	source := `{"config":"{$CADDY_JSON_PARSE_TEST_VALUE}","env":"{env.CADDY_JSON_PARSE_TEST_VALUE}","remote":"{http.request.remote.host}"}`
	path := filepath.Join(t.TempDir(), "query.jq")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	j := &JSONTransform{JQFile: path}
	provision(t, j)
	r, _ := newRequest(`{}`)
	r.RemoteAddr = "203.0.113.7:54321"
	caddyhttp.NewTestReplacer(r)
	err := j.ServeHTTP(httptest.NewRecorder(), r, caddyhttp.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) error {
		assertJSON(t, readBody(t, r), source)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
}

func TestJSONParseCaddyfile(t *testing.T) {
	var j JSONParse
	if err := j.UnmarshalCaddyfile(caddyfile.NewTestDispenser("json_parse strict {\n max_size 1MiB\n}")); err != nil {
		t.Fatal(err)
	}
	if !j.Strict || j.MaxSize != 1<<20 {
		t.Fatalf("incorrect config: %+v", j)
	}
	for _, input := range []string{
		"json_parse unexpected",
		"json_parse strict extra",
		"json_parse {\n set a 1\n}",
		"json_parse {\n max_size 0\n}",
	} {
		var j JSONParse
		if err := j.UnmarshalCaddyfile(caddyfile.NewTestDispenser(input)); err == nil {
			t.Errorf("accepted invalid parser config: %s", input)
		}
	}
}

func TestCaddyfileDefaultOrder(t *testing.T) {
	input := `:8080 {
		json_parse strict
		reverse_proxy localhost:6800
		json_transform {
			jq ".a = true"
		}
	}`
	config, _, err := caddyconfig.GetAdapter("caddyfile").Adapt([]byte(input), nil)
	if err != nil {
		t.Fatal(err)
	}
	transform := bytes.Index(config, []byte(`"handler":"json_transform"`))
	parse := bytes.Index(config, []byte(`"handler":"json_parse"`))
	proxy := bytes.Index(config, []byte(`"handler":"reverse_proxy"`))
	if transform < 0 || parse <= transform || proxy <= parse {
		t.Fatalf("unexpected handler order: %s", config)
	}
}

func TestAria2Caddyfile(t *testing.T) {
	input, err := os.ReadFile("Caddyfile.aria2.example")
	if err != nil {
		t.Fatal(err)
	}
	config, warnings, err := caddyconfig.GetAdapter("caddyfile").Adapt(input, map[string]any{"filename": "Caddyfile.aria2.example"})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("Caddyfile warnings: %v", warnings)
	}
	for _, want := range []string{`"handler":"json_transform"`, `"jq_file":"examples/aria2.jq"`, `"uri":"/jsonrpc"`} {
		if !strings.Contains(string(config), want) {
			t.Errorf("adapted config missing %s: %s", want, config)
		}
	}
}

func TestJSONPlaceholderExpression(t *testing.T) {
	j := &JSONParse{Strict: true}
	provision(t, j)
	r, _ := newRequest(`{"ref":"refs/heads/master","count":3}`)
	next := caddyhttp.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) error { return nil })
	if err := j.ServeHTTP(httptest.NewRecorder(), r, next); err != nil {
		t.Fatal(err)
	}
	matcher := &caddyhttp.MatchExpression{Expr: `{json.ref}.endsWith('/master') && int({json.count}) == 3`}
	provision(t, matcher)
	match, err := matcher.MatchWithError(r)
	if err != nil || !match {
		t.Fatalf("JSON expression match=%v, err=%v", match, err)
	}
}
