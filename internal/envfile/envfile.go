// Package envfile reads and writes KEY=value lines in a dotenv-style file,
// such as .env.development.local. Other lines are left alone.
package envfile

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Read returns the value of the last line that sets key, or "" when the
// file or the key is missing.
func Read(path, key string) (string, error) {
	values, err := ReadAll(path)
	if err != nil {
		return "", err
	}
	return values[key], nil
}

// ReadAll returns every KEY=value pair in the file. A later line wins.
func ReadAll(path string) (map[string]string, error) {
	values := map[string]string{}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := parseLine(scanner.Text())
		if ok {
			values[key] = value
		}
	}
	return values, scanner.Err()
}

// Write sets key to value, replacing any earlier line for it.
func Write(path, key, value string) error {
	return Update(path, map[string]string{key: value}, nil)
}

// Remove deletes every line that sets one of keys.
func Remove(path string, keys ...string) error {
	return Update(path, nil, keys)
}

// Update removes the lines for the keys in set and in remove, then appends
// one line per key in set. The file is created when it is missing.
func Update(path string, set map[string]string, remove []string) error {
	drop := map[string]bool{}
	for key := range set {
		drop[key] = true
	}
	for _, key := range remove {
		drop[key] = true
	}

	var kept []string
	content, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(strings.TrimRight(string(content), "\n"), "\n") {
		if len(content) == 0 {
			break
		}
		key, _, ok := parseLine(line)
		if ok && drop[key] {
			continue
		}
		kept = append(kept, line)
	}
	for _, key := range sortedKeys(set) {
		kept = append(kept, fmt.Sprintf("%s=%s", key, set[key]))
	}
	if len(kept) == 0 {
		if len(content) == 0 {
			return nil
		}
		return os.WriteFile(path, nil, 0o644)
	}
	return os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o644)
}

func parseLine(line string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	trimmed = strings.TrimPrefix(trimmed, "export ")
	key, value, found := strings.Cut(trimmed, "=")
	if !found || key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	return key, strings.Trim(value, `"'`), true
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
