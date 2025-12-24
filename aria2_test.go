package jsonparse

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAria2Example(t *testing.T) {
	j := &JSONTransform{JQFile: "examples/aria2.jq"}
	provision(t, j)
	tests := []struct{ name, input, want string }{
		{
			"no token or options",
			`{"method":"aria2.addUri","params":[["https://pixeldrain.com/a"]]}`,
			`{"method":"aria2.addUri","params":[["https://pixeldrain.com/a","https://pixeldrain.proxy.org/https://pixeldrain.com/a"],{"max-connection-per-server":"1"}]}`,
		},
		{
			"token, existing options, and position",
			`{"id":9007199254740993,"method":"aria2.addUri","params":["token:example",["https://pixeldrain.com/a"],{"dir":"/downloads","max-connection-per-server":"8"},0]}`,
			`{"id":9007199254740993,"method":"aria2.addUri","params":["token:example",["https://pixeldrain.com/a","https://pixeldrain.proxy.org/https://pixeldrain.com/a"],{"dir":"/downloads","max-connection-per-server":"1"},0]}`,
		},
		{
			"baidu",
			`{"method":"aria2.addUri","params":[["https://d.baidupcs.com/file/a"]]}`,
			`{"method":"aria2.addUri","params":[["https://d.baidupcs.com/file/a"],{"max-connection-per-server":"2","user-agent":"pan.baidu.com"}]}`,
		},
		{
			"pikpak with token",
			`{"method":"aria2.addUri","params":["token:example",["https://dl.mypikpak.com/a"]]}`,
			`{"method":"aria2.addUri","params":["token:example",["https://dl.mypikpak.com/a"],{"max-connection-per-server":"2"}]}`,
		},
		{
			"ordered matching options",
			`{"method":"aria2.addUri","params":[["https://d.baidu.com/file/a","https://pixeldrain.com/b"]]}`,
			`{"method":"aria2.addUri","params":[["https://d.baidu.com/file/a","https://pixeldrain.com/b","https://pixeldrain.proxy.org/https://pixeldrain.com/b"],{"max-connection-per-server":"1","user-agent":"pan.baidu.com"}]}`,
		},
		{
			"unmatched URIs do not create options",
			`{"method":"aria2.addUri","params":[["https://example.org/a"]]}`,
			`{"method":"aria2.addUri","params":[["https://example.org/a"]]}`,
		},
		{
			"other method",
			`{"method":"aria2.other","params":[["https://pixeldrain.com/a"]]}`,
			`{"method":"aria2.other","params":[["https://pixeldrain.com/a"]]}`,
		},
		{
			"empty URI list",
			`{"method":"aria2.addUri","params":[[]]}`,
			`{"method":"aria2.addUri","params":[[]]}`,
		},
		{
			"independent batch entries",
			`[{"method":"aria2.addUri","params":[["https://dl.mypikpak.com/a"]]},{"method":"aria2.addUri","params":[["https://example.org/b"]]},{"method":"aria2.tellStatus","params":["token:example","gid"]}]`,
			`[{"method":"aria2.addUri","params":[["https://dl.mypikpak.com/a"],{"max-connection-per-server":"2"}]},{"method":"aria2.addUri","params":[["https://example.org/b"]]},{"method":"aria2.tellStatus","params":["token:example","gid"]}]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, err := decodeJSON([]byte(tt.input))
			if err != nil {
				t.Fatal(err)
			}
			result, err := runProgram(context.Background(), j.code, input)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			assertJSON(t, string(body), tt.want)
			// The input must remain usable until the HTTP handler commits.
			original, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			assertJSON(t, string(original), tt.input)
		})
	}
}
