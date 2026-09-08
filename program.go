package jsonparse

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/itchyny/gojq"
)

func compileProgram(source string, placeholders bool) (*gojq.Code, []string, error) {
	if strings.TrimSpace(source) == "" {
		return nil, nil, errors.New("jq program is empty")
	}
	query, err := gojq.Parse(source)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing jq: %w", err)
	}
	var variables, templates []string
	if placeholders {
		variables, templates = bindPlaceholderStrings(query, source)
	}
	code, err := gojq.Compile(query, gojq.WithVariables(variables))
	if err != nil {
		return nil, nil, fmt.Errorf("compiling jq: %w", err)
	}
	return code, templates, nil
}

// runProgram accepts a single JSON result, and checks for errors even after the
// first result. gojq uses copy-on-write updates, so input remains unchanged.
func runProgram(ctx context.Context, code *gojq.Code, input any, values ...any) (any, error) {
	iter := code.RunWithContext(ctx, input, values...)
	result, ok := iter.Next()
	if !ok {
		return nil, errors.New("jq must produce exactly one JSON value; got none")
	}
	if err, ok := result.(error); ok {
		return nil, fmt.Errorf("executing jq: %w", err)
	}
	if extra, ok := iter.Next(); ok {
		if err, ok := extra.(error); ok {
			return nil, fmt.Errorf("executing jq: %w", err)
		}
		return nil, errors.New("jq must produce exactly one JSON value; got multiple")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
