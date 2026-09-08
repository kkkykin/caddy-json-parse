package jsonparse

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/itchyny/gojq"
)

// bindPlaceholderStrings turns jq string literals into variable references.
// Each request supplies the expanded strings as values, so placeholder contents
// cannot change the program's syntax and the compiled code can be reused.
func bindPlaceholderStrings(query *gojq.Query, source string) (variables, templates []string) {
	// Choose names that cannot be shadowed by the program's own bindings.
	prefix := "$__caddy_placeholder"
	for strings.Contains(source, prefix) {
		prefix += "_"
	}
	walkJQStrings(reflect.ValueOf(query), func(s *gojq.String) {
		if !strings.ContainsAny(s.Str, "{}") {
			return
		}
		name := prefix + strconv.Itoa(len(variables))
		variables = append(variables, name)
		templates = append(templates, s.Str)
		variable := &gojq.Query{Term: &gojq.Term{
			Type: gojq.TermTypeFunc,
			Func: &gojq.Func{Name: name},
		}}
		interpolation := &gojq.Query{Term: &gojq.Term{
			Type:  gojq.TermTypeQuery,
			Query: variable,
		}}
		// Keep the replacement inside a string term, so format strings such
		// as @uri "https://{env.HOST}/\(.path)" only format jq's own interpolations.
		literal := &gojq.Query{Term: &gojq.Term{
			Type: gojq.TermTypeString,
			Str:  &gojq.String{Queries: []*gojq.Query{interpolation}},
		}}
		*s = gojq.String{Queries: []*gojq.Query{literal}}
	})
	return
}

// gojq exposes its AST but no visitor. Its nodes consist of exported structs,
// pointers, and slices; traversing these covers literals in keys, indexes,
// patterns, function bodies, and nested string interpolations alike.
func walkJQStrings(node reflect.Value, visit func(*gojq.String)) {
	switch node.Kind() {
	case reflect.Pointer:
		if node.IsNil() {
			return
		}
		if s, ok := node.Interface().(*gojq.String); ok {
			if s.Queries == nil {
				visit(s)
			} else {
				for _, query := range s.Queries {
					walkJQStrings(reflect.ValueOf(query), visit)
				}
			}
			return
		}
		walkJQStrings(node.Elem(), visit)
	case reflect.Struct:
		for i := 0; i < node.NumField(); i++ {
			walkJQStrings(node.Field(i), visit)
		}
	case reflect.Slice:
		for i := 0; i < node.Len(); i++ {
			walkJQStrings(node.Index(i), visit)
		}
	}
}
