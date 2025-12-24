package jsonparse

import (
	"errors"
	"net/http"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(JSONParse{})
	caddy.RegisterModule(JSONTransform{})
	httpcaddyfile.RegisterHandlerDirective("json_transform", parseTransformCaddyfile)
	httpcaddyfile.RegisterHandlerDirective("json_parse", parseCaddyfile)
	// Caddy only allows ordering relative to standard directives. Register both
	// here so the default order is transform, parse, reverse_proxy.
	httpcaddyfile.RegisterDirectiveOrder("json_transform", "before", "reverse_proxy")
	httpcaddyfile.RegisterDirectiveOrder("json_parse", "before", "reverse_proxy")
}

// JSONParse exposes values from a JSON request body as {json.*} placeholders.
// It preserves the original body bytes.
type JSONParse struct {
	Strict  bool  `json:"strict,omitempty"`
	MaxSize int64 `json:"max_size,omitempty"`

	log *zap.Logger
}

func (JSONParse) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.json_parse",
		New: func() caddy.Module { return new(JSONParse) },
	}
}

func (j *JSONParse) Provision(ctx caddy.Context) error {
	j.log = ctx.Logger(j)
	return provisionMaxSize(&j.MaxSize)
}

func (j JSONParse) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	doc, err := loadDocument(r, j.MaxSize)
	if err != nil {
		if j.Strict || !errors.Is(err, errInvalidJSON) {
			return caddyhttp.Error(http.StatusBadRequest, err)
		}
		j.log.Debug("json_parse: skipping invalid JSON", zap.Error(err))
		return next.ServeHTTP(w, r)
	}
	if !doc.placeholders {
		repl := r.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)
		repl.Map(newReplacerFunc(doc))
		doc.placeholders = true
	}
	return next.ServeHTTP(w, r)
}

// UnmarshalCaddyfile accepts:
//
//	json_parse [strict] {
//	    max_size <size>
//	}
func (j *JSONParse) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		args := d.RemainingArgs()
		if len(args) > 1 || (len(args) == 1 && args[0] != "strict") {
			return d.ArgErr()
		}
		j.Strict = len(args) == 1
		seen := false
		for d.NextBlock(0) {
			if d.Val() != "max_size" {
				return d.Errf("unrecognized subdirective: %s", d.Val())
			}
			var size string
			if seen || !d.AllArgs(&size) {
				return d.ArgErr()
			}
			seen = true
			var err error
			j.MaxSize, err = parseMaxSize(size)
			if err != nil {
				return d.Errf("max_size: %v", err)
			}
		}
	}
	return nil
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var j JSONParse
	err := j.UnmarshalCaddyfile(h.Dispenser)
	return &j, err
}

var (
	_ caddy.Provisioner           = (*JSONParse)(nil)
	_ caddyhttp.MiddlewareHandler = (*JSONParse)(nil)
	_ caddyfile.Unmarshaler       = (*JSONParse)(nil)
)
