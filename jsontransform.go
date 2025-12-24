package jsonparse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/itchyny/gojq"
)

// JSONTransform rewrites a JSON request body with a jq program compiled at
// provision time. Exactly one of JQ and JQFile must be configured.
type JSONTransform struct {
	JQ      string         `json:"jq,omitempty"`
	JQFile  string         `json:"jq_file,omitempty"`
	MaxSize int64          `json:"max_size,omitempty"`
	Timeout caddy.Duration `json:"timeout,omitempty"`

	code *gojq.Code
}

func (JSONTransform) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.json_transform",
		New: func() caddy.Module { return new(JSONTransform) },
	}
}

func (j *JSONTransform) Provision(_ caddy.Context) error {
	if err := j.validateSource(); err != nil {
		return err
	}
	if err := provisionMaxSize(&j.MaxSize); err != nil {
		return err
	}
	if j.Timeout == 0 {
		j.Timeout = caddy.Duration(time.Second)
	}
	if j.Timeout < 0 {
		return errors.New("timeout must be positive")
	}

	source := j.JQ
	if j.JQFile != "" {
		body, err := os.ReadFile(j.JQFile)
		if err != nil {
			return fmt.Errorf("reading jq_file: %w", err)
		}
		source = string(body)
	}
	code, err := compileProgram(source)
	if err != nil {
		return err
	}
	j.code = code
	return nil
}

func (j JSONTransform) validateSource() error {
	if (strings.TrimSpace(j.JQ) == "") == (j.JQFile == "") {
		return errors.New("configure exactly one of jq or jq_file")
	}
	return nil
}

func (j JSONTransform) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	doc, err := loadDocument(r, j.MaxSize)
	if err != nil {
		return caddyhttp.Error(http.StatusBadRequest, err)
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(j.Timeout))
	defer cancel()
	result, err := runProgram(ctx, j.code, doc.value)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusServiceUnavailable
		} else if errors.Is(err, context.Canceled) {
			status = http.StatusRequestTimeout
		}
		return caddyhttp.Error(status, err)
	}
	body, err := json.Marshal(result)
	if err != nil {
		return caddyhttp.Error(http.StatusInternalServerError, fmt.Errorf("encoding jq result: %w", err))
	}
	if int64(len(body)) > j.MaxSize {
		return bodyTooLarge(j.MaxSize)
	}
	doc.commit(r, result, body)
	return next.ServeHTTP(w, r)
}

// UnmarshalCaddyfile accepts:
//
//	json_transform {
//	    jq <program>
//	    # or: jq_file <path>
//	    max_size <size>
//	    timeout <duration>
//	}
func (j *JSONTransform) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		if d.NextArg() {
			return d.ArgErr()
		}
		seen := make(map[string]bool)
		for d.NextBlock(0) {
			name := d.Val()
			if seen[name] {
				return d.Errf("duplicate subdirective: %s", name)
			}
			seen[name] = true
			var value string
			if !d.AllArgs(&value) {
				return d.ArgErr()
			}
			switch name {
			case "jq":
				j.JQ = value
			case "jq_file":
				j.JQFile = value
			case "max_size":
				size, err := parseMaxSize(value)
				if err != nil {
					return d.Errf("max_size: %v", err)
				}
				j.MaxSize = size
			case "timeout":
				duration, err := caddy.ParseDuration(value)
				if err != nil || duration <= 0 {
					return d.Err("timeout must be a positive duration")
				}
				j.Timeout = caddy.Duration(duration)
			default:
				return d.Errf("unrecognized subdirective: %s", name)
			}
		}
	}
	if err := j.validateSource(); err != nil {
		return d.Err(err.Error())
	}
	return nil
}

func parseTransformCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var j JSONTransform
	err := j.UnmarshalCaddyfile(h.Dispenser)
	return &j, err
}

var (
	_ caddy.Provisioner           = (*JSONTransform)(nil)
	_ caddyhttp.MiddlewareHandler = (*JSONTransform)(nil)
	_ caddyfile.Unmarshaler       = (*JSONTransform)(nil)
)
