// Package environ handles the environment variables that droot carries from a
// Docker image into a chrooted process.
package environ

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// EnvFilePath is the path, relative to the container root, of the file that
// `droot export` writes the image's environment variables into and that
// `droot run` reads them back from.
const EnvFilePath = "/.drootenv"

// maxLineSize bounds the length of a single line in an env file.
const maxLineSize = 1 << 20

// ReadEnvFile reads environment variables from the file at path.
// See ParseEnv for the file format.
func ReadEnvFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	env, err := ParseEnv(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return env, nil
}

// ParseEnv parses KEY=VALUE lines. Blank lines, lines starting with '#' and
// lines without '=' or with an empty key are ignored. Everything after the
// first '=' is the value, so values may themselves contain '='.
func ParseEnv(r io.Reader) ([]string, error) {
	var env []string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		env = append(env, key+"="+value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return env, nil
}

// Validate returns an error unless every item of env is in KEY=VALUE format
// with a non-empty KEY.
func Validate(env []string) error {
	for _, e := range env {
		if k, _, ok := strings.Cut(e, "="); !ok || k == "" {
			return fmt.Errorf("invalid environment variable %q: must be KEY=VALUE", e)
		}
	}
	return nil
}

// Merge returns a new slice containing the variables of base overridden by
// those of overrides. Variables keep the position of their first occurrence,
// new variables are appended in order, and duplicated keys are collapsed so
// that the last definition wins. Neither input slice is modified.
func Merge(base, overrides []string) ([]string, error) {
	if err := Validate(base); err != nil {
		return nil, err
	}
	if err := Validate(overrides); err != nil {
		return nil, err
	}

	merged := make([]string, 0, len(base)+len(overrides))
	index := make(map[string]int, len(base)+len(overrides))
	for _, list := range [][]string{base, overrides} {
		for _, e := range list {
			k, _, _ := strings.Cut(e, "=")
			if i, ok := index[k]; ok {
				merged[i] = e
				continue
			}
			index[k] = len(merged)
			merged = append(merged, e)
		}
	}
	return merged, nil
}

// Lookup returns the value of key in env.
func Lookup(env []string, key string) (string, bool) {
	value, found := "", false
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == key {
			value, found = v, true // the last definition wins
		}
	}
	return value, found
}
