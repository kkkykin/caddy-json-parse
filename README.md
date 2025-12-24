[![Go](https://github.com/abiosoft/caddy-json-parse/workflows/Go/badge.svg)](https://github.com/abiosoft/caddy-json-parse/actions)

# caddy-json-parse
Caddy v2 module for parsing json request body.

## Installation

```
xcaddy build v2.0.0 \
    --with github.com/abiosoft/caddy-json-parse
```


## Usage

`json_parse` parses the request body as json for reference as [placeholders](https://caddyserver.com/docs/caddyfile/concepts#placeholders).

### Caddyfile

Simply use the directive anywhere in a route. If set, `strict` responds with bad request if the request body is an invalid json.
```
json_parse [<strict>] {
    set <path> <json_value>
    merge <path> <json_object>
    delete <path>
    transform_array <path> <regex> <replacement...>
    merge_if_match <source_array_path> <regex> <target_path> <json_object>
}
```

And reference variables via `{json.*}` placeholders. Where `*` can get as deep as possible. e.g. `{json.items.0.label}`


#### Example

Run a [command](https://github.com/abiosoft/caddy-exec) only if the github webhook is a push on master branch.
```
@webhook {
    expression {json.ref}.endsWith('/master')
}
route {
    json_parse # enable json parser
    exec @webhook git pull origin master
}
```

#### Mutating the body

`json_parse` can also modify the parsed JSON before passing it downstream:

- `set path value` – replace/create the value at `path`.
- `merge path {"k":"v"}` – merge the object into a map at `path` (creates the map if missing).
- `delete path` – remove a map key or array index.
- `transform_array path regex replacement...` – for every string in the array at `path`, apply the regex and replace using the provided templates (use `$0` to keep the original).
- `merge_if_match source regex target {"k":"v"}` – if any string in the array at `source` matches the regex, merge the object into `target`.
- Each action can be gated with `when <cel expression>` (same syntax as Caddy's `expression` matcher); it runs only when the expression is true.

Paths use dot-notation with numeric indices and `*` wildcard (e.g. `params.0`, `items.*.url`).

Example – mirror pixeldrain URIs and add per-host options for aria2:

```
handle_path /aria2rpc {
    json_parse strict {
        transform_array params.0 ^https://pixeldrain\.com/(.*) "$0" "https://pixeldrain.proxy.org/$0"
        merge_if_match params.0 ^https://pixeldrain\.com/ params.1 {"max-connection-per-server":"1"}
    }
    reverse_proxy http://localhost:6800/jsonrpc
}
```

### JSON

`json_parse` can be part of any route as an handler

```jsonc
{
  ...
  "routes": [
    {
      "handle": [
        {
          "handler": "json_parse",

          // if set to true, returns bad request for invalid json
          "strict": false,

          // ordered list of actions (optional)
          "actions": [
            { "type": "set", "path": "user.id", "value": 123 },
            { "type": "merge_if_match", "path": "params.0", "regex": "example.com", "target": "params.1", "value": {"ua":"custom"} }
          ]
        },
        ...
      ]
    },
  ...
  ]
}
```

### Full aria2 example

See `Caddyfile.aria2.example` for a complete configuration that mirrors pixeldrain links and sets server options for BaiduNetdisk, PikPak, and pixeldrain before proxying to aria2 RPC.

## License

Apache 2
