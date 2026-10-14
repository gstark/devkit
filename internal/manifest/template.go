package manifest

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Vars holds the values a template can reference, such as "port",
// "ports.rails", "services.s3.port", "origin.spa", "bucket", "slug",
// "data_dir", "config", "xdg_data", and "root".
type Vars map[string]string

// With returns a copy of v with one more variable.
func (v Vars) With(key, value string) Vars {
	out := make(Vars, len(v)+1)
	for k, val := range v {
		out[k] = val
	}
	out[key] = value
	return out
}

var placeholder = regexp.MustCompile(`\{([a-z_][a-z0-9_]*(?:\.[a-z0-9_-]+)*)(\+\d+)?\}`)

// Expand replaces every {name} and {name+N} in s. A {name+N} adds N to a
// numeric variable. An unknown variable is an error, so a typo in a manifest
// fails loudly instead of leaking braces into a command line.
func (v Vars) Expand(s string) (string, error) {
	var firstErr error
	out := placeholder.ReplaceAllStringFunc(s, func(match string) string {
		parts := placeholder.FindStringSubmatch(match)
		name, offset := parts[1], parts[2]
		value, ok := v[name]
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("unknown template variable %s", match)
			}
			return match
		}
		if offset == "" {
			return value
		}
		base, err := strconv.Atoi(value)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %q is not a number", match, value)
			}
			return match
		}
		delta, _ := strconv.Atoi(strings.TrimPrefix(offset, "+"))
		return strconv.Itoa(base + delta)
	})
	return out, firstErr
}

// ExpandAll expands every string in a slice.
func (v Vars) ExpandAll(values []string) ([]string, error) {
	out := make([]string, len(values))
	for i, value := range values {
		expanded, err := v.Expand(value)
		if err != nil {
			return nil, err
		}
		out[i] = expanded
	}
	return out, nil
}

// ExpandMap expands every value in a map.
func (v Vars) ExpandMap(values map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(values))
	for key, value := range values {
		expanded, err := v.Expand(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		out[key] = expanded
	}
	return out, nil
}
