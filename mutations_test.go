package jsonparse

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/caddyserver/caddy/v2"
)

func mustCompileActions(t *testing.T, acts []Action) []Action {
	t.Helper()
	var ctx caddy.Context
	for i := range acts {
		if err := acts[i].compile(ctx); err != nil {
			t.Fatalf("compile: %v", err)
		}
	}
	return acts
}

func TestApplyActionsTransformAndMerge(t *testing.T) {
	body := []byte(`{
		"method": "aria2.addUri",
		"params": [
			["https://pixeldrain.com/file1"],
			{"existing": "yes"}
		]
	}`)

	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	acts := mustCompileActions(t, []Action{
		{
			Type:         "transform_array",
			Path:         "params.0",
			Regex:        `^https://pixeldrain\.com/(.*)`,
			Replacements: []string{`$0`, `https://mirror.example.com/$1`},
		},
		{
			Type:  "merge",
			Path:  "params.1",
			Value: json.RawMessage(`{"max-connection-per-server":"2"}`),
		},
	})

	changed, err := applyActions(&v, acts, httptest.NewRequest("POST", "/", nil))
	if err != nil {
		t.Fatalf("applyActions error: %v", err)
	}
	if !changed {
		t.Fatalf("expected mutations to be applied")
	}

	params := fetchValue(v, "params").([]interface{})
	uris := params[0].([]interface{})
	if len(uris) != 2 {
		t.Fatalf("expected 2 uris, got %d", len(uris))
	}
	if uris[0] != "https://pixeldrain.com/file1" || uris[1] != "https://mirror.example.com/file1" {
		t.Fatalf("unexpected uris: %#v", uris)
	}

	opts := params[1].(map[string]interface{})
	if opts["existing"] != "yes" || opts["max-connection-per-server"] != "2" {
		t.Fatalf("unexpected options: %#v", opts)
	}
}

func TestApplyActionsSetAndDelete(t *testing.T) {
	body := []byte(`{"a":{"b":1,"c":2},"arr":[1,2,3]}`)
	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	acts := mustCompileActions(t, []Action{
		{Type: "set", Path: "a.b", Value: json.RawMessage(`10`)},
		{Type: "delete", Path: "a.c"},
	})

	changed, err := applyActions(&v, acts, httptest.NewRequest("POST", "/", nil))
	if err != nil {
		t.Fatalf("applyActions: %v", err)
	}
	if !changed {
		t.Fatalf("expected changes")
	}

	if got := fetchValue(v, "a.b"); got != float64(10) {
		t.Fatalf("set failed, got %v", got)
	}
	if got := fetchValue(v, "a.c"); got != nil {
		t.Fatalf("delete failed, got %v", got)
	}
}

func TestMergeIfMatchCreatesOptions(t *testing.T) {
	body := []byte(`{"params":[["https://pixeldrain.com/file1"]]}`)
	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	acts := mustCompileActions(t, []Action{
		{
			Type:   "merge_if_match",
			Path:   "params.0",
			Regex:  `pixeldrain\.com`,
			Target: "params.1",
			Value:  json.RawMessage(`{"max-connection-per-server":"1"}`),
		},
	})

	changed, err := applyActions(&v, acts, httptest.NewRequest("POST", "/", nil))
	if err != nil {
		t.Fatalf("applyActions: %v", err)
	}
	if !changed {
		t.Fatalf("expected change")
	}

	params := fetchValue(v, "params").([]interface{})
	if len(params) != 2 {
		t.Fatalf("expected params len 2, got %d", len(params))
	}
	opts, ok := params[1].(map[string]interface{})
	if !ok {
		t.Fatalf("options not a map: %#v", params[1])
	}
	if opts["max-connection-per-server"] != "1" {
		t.Fatalf("option missing: %#v", opts)
	}
}
