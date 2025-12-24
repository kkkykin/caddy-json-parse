# Match against the original URIs. Later matching rules override earlier options.
def uri_options:
  . as $uris
  | [
      {
        pattern: "^https?://[^/]+[.]baidu(?:pcs)?[.]com/file/",
        options: {"max-connection-per-server": "2", "user-agent": "pan.baidu.com"}
      },
      {
        pattern: "^https://[^/]+[.]mypikpak[.]com/",
        options: {"max-connection-per-server": "2"}
      },
      {
        pattern: "^https://pixeldrain[.]com/",
        options: {"max-connection-per-server": "1"}
      }
    ]
  | reduce .[] as $rule ({};
      if any($uris[]; test($rule.pattern))
      then . + $rule.options
      else . end
    );

def mirror_uris:
  [
    .[]
    | if startswith("https://pixeldrain.com/")
      then ., ("https://pixeldrain.proxy.org/" + .)
      else . end
  ];

def rewrite_add_uri:
  if type != "object" then .
  elif .method != "aria2.addUri" then .
  else
    (if (.params[0] | type) == "string" then 1 else 0 end) as $i
    | .params[$i] as $uris
    | ($uris | uri_options) as $options
    | .params[$i] = ($uris | mirror_uris)
    | if $options == {} then .
      else .params[$i + 1] = ((.params[$i + 1] // {}) + $options)
      end
  end;

# JSON-RPC batch entries are transformed independently.
if type == "array" then map(rewrite_add_uri) else rewrite_add_uri end
