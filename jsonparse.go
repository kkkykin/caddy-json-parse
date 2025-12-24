package jsonparse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

// Interface guards
var (
	_ caddy.Provisioner           = (*JSONParse)(nil)
	_ caddyhttp.MiddlewareHandler = (*JSONParse)(nil)
	_ caddyfile.Unmarshaler       = (*JSONParse)(nil)
)

func init() {
	caddy.RegisterModule(JSONParse{})
	httpcaddyfile.RegisterHandlerDirective("json_parse", parseCaddyfile)
	httpcaddyfile.RegisterDirectiveOrder("json_parse", "before", "reverse_proxy")
}

// JSONParse implements an HTTP handler that parses
// json body as placeholders.
type JSONParse struct {
	Strict  bool     `json:"strict,omitempty"`
	Actions []Action `json:"actions,omitempty"`
	log     *zap.Logger
}

// CaddyModule returns the Caddy module information.
func (JSONParse) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.json_parse",
		New: func() caddy.Module { return new(JSONParse) },
	}
}

// Provision implements caddy.Provisioner.
func (j *JSONParse) Provision(ctx caddy.Context) error {
	j.log = ctx.Logger(j)

	for i := range j.Actions {
		if err := j.Actions[i].compile(ctx); err != nil {
			return err
		}
	}

	return nil
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (j JSONParse) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	repl := r.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)

	origBody, err := io.ReadAll(r.Body)
	if err != nil {
		if j.Strict {
			return caddyhttp.Error(http.StatusBadRequest, err)
		}
		j.log.Debug("json_parse: failed to read body", zap.Error(err))
		r.Body = io.NopCloser(bytes.NewReader(origBody))
		return next.ServeHTTP(w, r)
	}

	// always restore body so downstream handlers can read it
	restoreBody := func(b []byte) {
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.ContentLength = int64(len(b))
		r.Header.Set("Content-Length", fmt.Sprintf("%d", len(b)))
	}
	restoreBody(origBody)

	if len(origBody) == 0 {
		if j.Strict {
			return caddyhttp.Error(http.StatusBadRequest, fmt.Errorf("json_parse: empty body"))
		}
		return next.ServeHTTP(w, r)
	}

	var v interface{}
	if err := json.Unmarshal(origBody, &v); err != nil {
		if j.Strict {
			return caddyhttp.Error(http.StatusBadRequest, err)
		}
		j.log.Debug("json_parse: invalid json", zap.Error(err))
		return next.ServeHTTP(w, r)
	}

	// Map placeholders before evaluating conditional actions
	repl.Map(newReplacerFunc(v))

	mutated, err := applyActions(&v, j.Actions, r)
	if err != nil {
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}

	if mutated {
		newBody, err := json.Marshal(v)
		if err != nil {
			return caddyhttp.Error(http.StatusInternalServerError, err)
		}
		restoreBody(newBody)
		// refresh placeholders to reflect mutations
		repl.Map(newReplacerFunc(v))
	}

	return next.ServeHTTP(w, r)
}

// UnmarshalCaddyfile implements caddyfile.Unmarshaler.
func (j *JSONParse) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		args := d.RemainingArgs()
		switch len(args) {
		case 0:
		case 1:
			if args[0] != "strict" {
				return d.Errf("unexpected token '%s'", args[0])
			}
			j.Strict = true
		default:
			return d.ArgErr()
		}

		for d.NextBlock(0) {
			switch d.Val() {
			case "set":
				if !d.NextArg() {
					return d.ArgErr()
				}
				path := d.Val()
				valueStr, whenStr := splitValueAndWhen(d.RemainingArgs())
				if strings.TrimSpace(valueStr) == "" {
					return d.Errf("set %s: missing value", path)
				}
				var raw json.RawMessage
				if err := json.Unmarshal([]byte(valueStr), &raw); err != nil {
					return d.Errf("set %s: value must be valid JSON: %v", path, err)
				}
				j.Actions = append(j.Actions, Action{
					Type:  "set",
					Path:  path,
					Value: raw,
					When:  whenStr,
				})

			case "merge":
				if !d.NextArg() {
					return d.ArgErr()
				}
				path := d.Val()
				valueStr, whenStr := splitValueAndWhen(d.RemainingArgs())
				if strings.TrimSpace(valueStr) == "" {
					return d.Errf("merge %s: missing object", path)
				}
				var raw json.RawMessage
				if err := json.Unmarshal([]byte(valueStr), &raw); err != nil {
					return d.Errf("merge %s: value must be valid JSON: %v", path, err)
				}
				j.Actions = append(j.Actions, Action{
					Type:  "merge",
					Path:  path,
					Value: raw,
					When:  whenStr,
				})

			case "delete":
				if !d.NextArg() {
					return d.ArgErr()
				}
				path := d.Val()
				whenStr := ""
				if d.NextArg() {
					if d.Val() != "when" {
						return d.ArgErr()
					}
					whenStr = strings.Join(d.RemainingArgs(), " ")
					if strings.TrimSpace(whenStr) == "" {
						return d.Errf("delete %s: when expression missing", path)
					}
				}
				j.Actions = append(j.Actions, Action{
					Type: "delete",
					Path: path,
					When: whenStr,
				})

			case "transform_array":
				if !d.NextArg() {
					return d.ArgErr()
				}
				path := d.Val()
				if !d.NextArg() {
					return d.ArgErr()
				}
				regex := d.Val()
				repls, whenStr := splitReplsAndWhen(d.RemainingArgs())
				if len(repls) == 0 {
					return d.Errf("transform_array %s: needs at least one replacement", path)
				}
				j.Actions = append(j.Actions, Action{
					Type:         "transform_array",
					Path:         path,
					Regex:        regex,
					Replacements: repls,
					When:         whenStr,
				})

			case "merge_if_match":
				// merge_if_match <source_path> <regex> <target_path> <json_object>
				if !d.NextArg() {
					return d.ArgErr()
				}
				source := d.Val()
				if !d.NextArg() {
					return d.ArgErr()
				}
				regex := d.Val()
				if !d.NextArg() {
					return d.ArgErr()
				}
				target := d.Val()
				valueStr, whenStr := splitValueAndWhen(d.RemainingArgs())
				if strings.TrimSpace(valueStr) == "" {
					return d.Errf("merge_if_match %s: missing object", source)
				}
				var raw json.RawMessage
				if err := json.Unmarshal([]byte(valueStr), &raw); err != nil {
					return d.Errf("merge_if_match %s: value must be valid JSON: %v", source, err)
				}

				j.Actions = append(j.Actions, Action{
					Type:   "merge_if_match",
					Path:   source,
					Regex:  regex,
					Target: target,
					Value:  raw,
					When:   whenStr,
				})

			default:
				return d.Errf("unrecognized subdirective: %s", d.Val())
			}
		}
	}
	return nil
}

// splitValueAndWhen separates JSON value tokens from optional "when" expression.
func splitValueAndWhen(tokens []string) (value string, when string) {
	valueTokens := tokens
	for i, tok := range tokens {
		if tok == "when" {
			valueTokens = tokens[:i]
			when = strings.Join(tokens[i+1:], " ")
			break
		}
	}
	value = strings.Join(valueTokens, " ")
	return
}

// splitReplsAndWhen handles replacements list with optional trailing "when".
func splitReplsAndWhen(tokens []string) (repls []string, when string) {
	for i, tok := range tokens {
		if tok == "when" {
			repls = tokens[:i]
			when = strings.Join(tokens[i+1:], " ")
			return
		}
	}
	repls = tokens
	return
}

// parseCaddyfile unmarshals tokens from h into a new Middleware.
func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m JSONParse
	err := m.UnmarshalCaddyfile(h.Dispenser)
	return m, err
}
