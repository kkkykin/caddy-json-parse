package jsonparse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// Action represents a single mutation applied to the parsed JSON body.
// Supported types:
//   - set: replace value at path with provided value.
//   - merge: merge provided object into map at path.
//   - delete: delete value at path (map key or array index).
//   - transform_array: apply regex replacements to each string element in the array at path.
type Action struct {
	Type         string          `json:"type"`
	Path         string          `json:"path"`
	Value        json.RawMessage `json:"value,omitempty"`
	Regex        string          `json:"regex,omitempty"`
	Replacements []string        `json:"replacements,omitempty"`
	Target       string          `json:"target,omitempty"`
	When         string          `json:"when,omitempty"`

	compiledRegex *regexp.Regexp
	compiledValue interface{}
	matcher       *caddyhttp.MatchExpression
}

// compile prepares regex, JSON values, and match expressions.
func (a *Action) compile(ctx caddy.Context) error {
	switch a.Type {
	case "set", "merge":
		if len(a.Value) == 0 {
			return fmt.Errorf("%s %s: empty value", a.Type, a.Path)
		}
		var v interface{}
		if err := json.Unmarshal(a.Value, &v); err != nil {
			return fmt.Errorf("%s %s: invalid JSON value: %w", a.Type, a.Path, err)
		}
		a.compiledValue = v
	case "delete":
		// nothing to prepare
	case "transform_array":
		if a.Regex == "" {
			return fmt.Errorf("transform_array %s: regex required", a.Path)
		}
		re, err := regexp.Compile(a.Regex)
		if err != nil {
			return fmt.Errorf("transform_array %s: invalid regex: %w", a.Path, err)
		}
		if len(a.Replacements) == 0 {
			return fmt.Errorf("transform_array %s: at least one replacement required", a.Path)
		}
		a.compiledRegex = re
	case "merge_if_match":
		if a.Regex == "" {
			return fmt.Errorf("merge_if_match %s: regex required", a.Path)
		}
		if a.Target == "" {
			return fmt.Errorf("merge_if_match %s: target path required", a.Path)
		}
		re, err := regexp.Compile(a.Regex)
		if err != nil {
			return fmt.Errorf("merge_if_match %s: invalid regex: %w", a.Path, err)
		}
		var v interface{}
		if err := json.Unmarshal(a.Value, &v); err != nil {
			return fmt.Errorf("merge_if_match %s: invalid JSON value: %w", a.Path, err)
		}
		if _, ok := v.(map[string]interface{}); !ok {
			return fmt.Errorf("merge_if_match %s: value must be an object", a.Path)
		}
		a.compiledRegex = re
		a.compiledValue = v
	default:
		return fmt.Errorf("unsupported action type: %s", a.Type)
	}

	if strings.TrimSpace(a.When) != "" {
		me := &caddyhttp.MatchExpression{Expr: a.When}
		if err := me.Provision(ctx); err != nil {
			return fmt.Errorf("when %s: %w", a.Path, err)
		}
		a.matcher = me
	}

	return nil
}

// applyActions mutates the provided JSON value in-place.
func applyActions(root *interface{}, actions []Action, r *http.Request) (bool, error) {
	mutated := false

	for _, act := range actions {
		if act.matcher != nil && !act.matcher.Match(r) {
			continue
		}
		switch act.Type {
		case "set":
			changed := applySet(root, act.Path, act.compiledValue)
			mutated = mutated || changed
		case "merge":
			changed, err := applyMerge(root, act.Path, act.compiledValue)
			if err != nil {
				return mutated, err
			}
			mutated = mutated || changed
		case "delete":
			changed := applyDelete(root, act.Path)
			mutated = mutated || changed
		case "transform_array":
			changed := applyTransformArray(root, act.Path, act.compiledRegex, act.Replacements)
			mutated = mutated || changed
		case "merge_if_match":
			changed, err := applyMergeIfMatch(root, act.Path, act.Target, act.compiledRegex, act.compiledValue)
			if err != nil {
				return mutated, err
			}
			mutated = mutated || changed
		default:
			return mutated, fmt.Errorf("unsupported action type %s", act.Type)
		}
	}

	return mutated, nil
}

// applySet replaces value at path. If the path is missing, it's a no-op.
func applySet(root *interface{}, path string, value interface{}) bool {
	targets := findTargets(root, path)
	if len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		t.set(clone(value))
	}
	return true
}

// applyMerge merges an object into map at path. Non-map targets are ignored.
func applyMerge(root *interface{}, path string, value interface{}) (bool, error) {
	targets := findTargets(root, path)
	if len(targets) == 0 {
		return false, nil
	}

	src, ok := value.(map[string]interface{})
	if !ok {
		return false, fmt.Errorf("merge %s: value must be an object", path)
	}

	changed := false
	for _, t := range targets {
		dstVal, ok := t.get()
		if !ok || dstVal == nil {
			dstVal = make(map[string]interface{})
			t.set(dstVal)
			ok = true
		}
		dst, ok := dstVal.(map[string]interface{})
		if !ok {
			continue
		}
		for k, v := range src {
			current, exists := dst[k]
			if !exists || !deepEqual(current, v) {
				dst[k] = clone(v)
				changed = true
			}
		}
	}

	return changed, nil
}

// applyDelete removes a key/index.
func applyDelete(root *interface{}, path string) bool {
	targets := findTargets(root, path)
	if len(targets) == 0 {
		return false
	}
	changed := false
	for _, t := range targets {
		if t.del != nil && t.del() {
			changed = true
		}
	}
	return changed
}

// applyTransformArray maps regex replacements over a string array.
func applyTransformArray(root *interface{}, path string, re *regexp.Regexp, replacements []string) bool {
	targets := findTargets(root, path)
	if len(targets) == 0 {
		return false
	}

	mutated := false
	for _, t := range targets {
		targetChanged := false
		val, ok := t.get()
		if !ok {
			continue
		}
		arr, ok := val.([]interface{})
		if !ok {
			continue
		}

		var out []interface{}
		for _, item := range arr {
			str, ok := item.(string)
			if !ok {
				out = append(out, item)
				continue
			}

			indices := re.FindStringSubmatchIndex(str)
			if indices == nil {
				out = append(out, str)
				continue
			}

			for _, tmpl := range replacements {
				expanded := re.ExpandString(nil, tmpl, str, indices)
				out = append(out, string(expanded))
			}
			targetChanged = true
			mutated = true
		}
		if targetChanged {
			t.set(out)
		}
	}

	return mutated
}

// applyMergeIfMatch merges value into targetPath when any string in the source array matches the regex.
func applyMergeIfMatch(root *interface{}, sourcePath, targetPath string, re *regexp.Regexp, value interface{}) (bool, error) {
	sources := findTargets(root, sourcePath)
	matched := false
	for _, s := range sources {
		val, ok := s.get()
		if !ok {
			continue
		}
		arr, ok := val.([]interface{})
		if !ok {
			continue
		}
		for _, item := range arr {
			str, ok := item.(string)
			if !ok {
				continue
			}
			if re.MatchString(str) {
				matched = true
				break
			}
		}
		if matched {
			break
		}
	}

	if !matched {
		return false, nil
	}

	return applyMerge(root, targetPath, value)
}

// target represents a reachable node with callbacks to get/set/delete it.
type target struct {
	get func() (interface{}, bool)
	set func(interface{})
	del func() bool
}

// findTargets returns all nodes matching the dotted path. Supports numeric indices and '*' wildcard.
// Missing map keys at the final segment are returned so setters can create them. Array indices are
// grown on demand when a setter is invoked.
func findTargets(root *interface{}, path string) []target {
	segments := strings.Split(path, ".")
	return walkTargets(*root, func(v interface{}) { *root = v }, segments)
}

func walkTargets(current interface{}, setter func(interface{}), segments []string) []target {
	if len(segments) == 0 {
		return nil
	}

	seg := segments[0]
	last := len(segments) == 1
	var out []target

	switch seg {
	case "*":
		switch v := current.(type) {
		case []interface{}:
			for i := range v {
				childSetter := func(idx int) func(interface{}) {
					return func(newVal interface{}) {
						v[idx] = newVal
						setter(v)
					}
				}(i)
				if last {
					out = append(out, target{
						get: func(idx int) func() (interface{}, bool) {
							return func() (interface{}, bool) {
								if idx < 0 || idx >= len(v) {
									return nil, false
								}
								return v[idx], true
							}
						}(i),
						set: childSetter,
						del: func(idx int) func() bool {
							return func() bool {
								if idx < 0 || idx >= len(v) {
									return false
								}
								v = append(v[:idx], v[idx+1:]...)
								setter(v)
								return true
							}
						}(i),
					})
				} else {
					out = append(out, walkTargets(v[i], childSetter, segments[1:])...)
				}
			}
		case map[string]interface{}:
			for k, val := range v {
				childSetter := func(key string) func(interface{}) {
					return func(newVal interface{}) {
						v[key] = newVal
						setter(v)
					}
				}(k)
				if last {
					out = append(out, target{
						get: func(key string) func() (interface{}, bool) {
							return func() (interface{}, bool) {
								val, ok := v[key]
								return val, ok
							}
						}(k),
						set: childSetter,
						del: func(key string) func() bool {
							return func() bool {
								if _, ok := v[key]; ok {
									delete(v, key)
									setter(v)
									return true
								}
								return false
							}
						}(k),
					})
				} else {
					out = append(out, walkTargets(val, childSetter, segments[1:])...)
				}
			}
		}
	default:
		if idx, err := strconv.Atoi(seg); err == nil {
			if arr, ok := current.([]interface{}); ok && idx >= 0 {
				// Prepare setter that can grow the array to fit idx.
				arrSetter := func(newArr []interface{}) {
					setter(newArr)
				}
				ensureIndex := func() []interface{} {
					if idx < len(arr) {
						return arr
					}
					// grow with nils
					grown := make([]interface{}, idx+1)
					copy(grown, arr)
					arrSetter(grown)
					return grown
				}

				if last {
					out = append(out, target{
						get: func() (interface{}, bool) {
							if idx < len(arr) {
								return arr[idx], true
							}
							return nil, false
						},
						set: func(newVal interface{}) {
							arr = ensureIndex()
							arr[idx] = newVal
							arrSetter(arr)
						},
						del: func() bool {
							if idx < 0 || idx >= len(arr) {
								return false
							}
							arr = append(arr[:idx], arr[idx+1:]...)
							arrSetter(arr)
							return true
						},
					})
				} else if idx < len(arr) {
					childSetter := func(newVal interface{}) {
						arr[idx] = newVal
						arrSetter(arr)
					}
					out = append(out, walkTargets(arr[idx], childSetter, segments[1:])...)
				}
			}
		} else {
			if obj, ok := current.(map[string]interface{}); ok {
				val, exists := obj[seg]
				childSetter := func(newVal interface{}) {
					obj[seg] = newVal
					setter(obj)
				}
				if last {
					out = append(out, target{
						get: func() (interface{}, bool) {
							val, ok := obj[seg]
							return val, ok
						},
						set: childSetter,
						del: func() bool {
							if _, ok := obj[seg]; ok {
								delete(obj, seg)
								setter(obj)
								return true
							}
							return false
						},
					})
				} else if exists {
					out = append(out, walkTargets(val, childSetter, segments[1:])...)
				}
			}
		}
	}

	return out
}

// clone ensures we don't share mutable maps/slices between targets.
func clone(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		cp := make(map[string]interface{}, len(val))
		for k, v2 := range val {
			cp[k] = clone(v2)
		}
		return cp
	case []interface{}:
		cp := make([]interface{}, len(val))
		for i, v2 := range val {
			cp[i] = clone(v2)
		}
		return cp
	default:
		return val
	}
}

// deepEqual handles interface equality for merge decisions.
func deepEqual(a, b interface{}) bool {
	switch av := a.(type) {
	case map[string]interface{}:
		bv, ok := b.(map[string]interface{})
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !deepEqual(v, bv[k]) {
				return false
			}
		}
		return true
	case []interface{}:
		bv, ok := b.([]interface{})
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !deepEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return av == b
	}
}
