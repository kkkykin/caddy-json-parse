[![Go](https://github.com/abiosoft/caddy-json-parse/workflows/Go/badge.svg)](https://github.com/abiosoft/caddy-json-parse/actions)

# caddy-json-parse

Caddy v2 handlers for reading and transforming JSON request bodies:

- `json_parse` exposes JSON values as `{json.*}` placeholders.
- `json_transform` rewrites JSON using an embedded [gojq](https://github.com/itchyny/gojq) program. No external jq executable is required.

## Installation

Build with Go 1.25 or later and [xcaddy](https://github.com/caddyserver/xcaddy):

```sh
xcaddy build v2.10.2 \
    --with github.com/abiosoft/caddy-json-parse
```

For a local checkout, append `=./` to the module path.

## Placeholders

```caddyfile
json_parse [strict] {
    max_size <size>
}
```

The block is optional. Paths use dot notation with array indices, such as
`{json.user.name}`, `{json.items.0.label}`, or `{json.labels.100}`.
Missing values produce an empty placeholder. Negative or out-of-range array
indices are treated as missing.

`json_parse` preserves the request body bytes. With `strict`, empty or invalid
JSON returns HTTP 400. Without `strict`, it passes that body downstream without
adding placeholders. Read failures and size limits always return an error.

For example, with the [exec module](https://github.com/abiosoft/caddy-exec),
run a command only for a GitHub webhook on the master branch:

```caddyfile
@webhook expression {json.ref}.endsWith('/master')
route {
    json_parse strict
    exec @webhook git pull origin master
}
```

For numeric CEL comparisons, convert explicitly, for example
`int({json.count}) > 1` or `double({json.price}) > 10.5`.

## Transforming JSON

```caddyfile
json_transform {
    jq <program>
    # Or: jq_file <path>
    max_size <size>
    timeout <duration>
}
```

Configure exactly one of `jq` and `jq_file`. Inline programs must be a single
Caddyfile argument: use backticks or a heredoc to preserve jq's quotes and spaces.
Programs are compiled when Caddy loads the configuration. File paths are relative
to Caddy's working directory; reload the configuration after editing a jq file.

```caddyfile
route {
    json_transform {
        jq <<JQ
            .user.role = "member"
            | del(.password)
        JQ
    }
    json_parse strict
    reverse_proxy localhost:8081 {
        header_up X-User-Role {json.user.role}
    }
}
```

Use jq's conditionals, object updates, array operations, and variable bindings
to express the whole transformation. For example:

```jq
if .method == "example.update" then
    .params.options += {"enabled": true}
    | del(.params.items[] | select(.obsolete))
else
    .
end
```

Each request must contain exactly one JSON value, and the program must produce
exactly one JSON value. Objects, arrays, scalars, and `null` are valid results.
Collect multiple results into an array with `[...]`; programs such as `empty`
or `., .` fail the result-count check.

A successful transform updates the body, content length, replay body, and content
type (`application/json`) together. A failed transform never sends a partial
result downstream. Output is re-encoded, so whitespace and object-key order may
change. Unchanged numbers retain their JSON precision, including large integers
and decimals. Integer arithmetic supports arbitrary precision; fractional
arithmetic and mathematical functions follow gojq's floating-point semantics.
Non-finite results cannot be encoded as JSON and are rejected.

Both handlers share the parsed document within a request. Add `json_parse` when
placeholders are needed; they reflect the latest successful transform, even if
`json_parse` ran first. By default, Caddy orders `json_transform`, then
`json_parse`, then `reverse_proxy`. Use a `route` block for explicit ordering.

### Limits and errors

`max_size` defaults to **10 MiB** for each handler. It limits the input body and,
for `json_transform`, the encoded result. Caddyfile values accept sizes such as
`1MiB` or `1048576`; JSON configuration uses bytes. Each handler enforces its own
limit, including when it reuses an already parsed document.

`timeout` defaults to **1s** and bounds jq evaluation. Request cancellation also
stops evaluation. Programs receive the request JSON; module imports, extra input
streams, and access to the process environment are not enabled.

| Failure | Result |
| --- | --- |
| Missing, invalid, or unreadable program | Configuration load fails |
| Invalid JSON or body read failure | HTTP 400 |
| Input or output exceeds `max_size` | HTTP 413 |
| jq error, zero/multiple results, or unencodable output | HTTP 500 |
| jq evaluation timeout | HTTP 503 |
| Cancelled request | HTTP 408 |

The lenient behavior described above applies only to `json_parse`;
`json_transform` always rejects invalid JSON.

### aria2 example

[Caddyfile.aria2.example](Caddyfile.aria2.example) proxies `/aria2rpc` to aria2's
`/jsonrpc` endpoint using [examples/aria2.jq](examples/aria2.jq). Run Caddy from
the repository root, or adjust the jq file path.

The program:

- Handles `aria2.addUri` with or without a secret token.
- Retains original pixeldrain URIs and adds a proxy URI containing the original
  URL. Replace `pixeldrain.proxy.org` with your proxy's hostname and URL format.
- Merges per-server options for BaiduNetdisk, PikPak, and pixeldrain, preserving
  unrelated options and trailing parameters. Later matching rules override
  earlier option values.
- Processes each JSON-RPC batch entry independently and leaves other methods
  unchanged.

## JSON configuration

Example HTTP route:

```json
{
  "handle": [
    {
      "handler": "json_transform",
      "jq": ".user.role = \"member\" | del(.password)",
      "max_size": 10485760,
      "timeout": "1s"
    },
    {
      "handler": "json_parse",
      "strict": true
    },
    {
      "handler": "reverse_proxy",
      "upstreams": [{"dial": "localhost:8081"}]
    }
  ]
}
```

Use `"jq_file": "/etc/caddy/aria2.jq"` in place of `jq` to load a file.

## Development

```sh
go test ./...
go test -race ./...
```

A Nix development shell is available with `nix develop`. The Makefile runs its
build and test commands in that shell; `make build` produces Caddy with this module.

## License

Apache 2
