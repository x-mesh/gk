package config

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ReadGK returns git config entries under the gk.* namespace as a nested map
// suitable for viper.MergeConfigMap. Returns an empty map (nil error) when the
// working directory is not a git repository or there are no gk.* entries.
func ReadGK() (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "config", "--get-regexp", `^gk\.`)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0")

	out, err := cmd.Output()
	if err != nil {
		// exit code 1 means no matching keys — not an error for our purposes.
		// Any other failure (e.g. not a git repo) is also treated as "no config".
		return map[string]any{}, nil
	}

	return parseGKLines(string(out)), nil
}

// parseGKLines converts raw `git config --get-regexp` output into a nested map.
// Input format: one line per entry, "key<space>value", e.g.:
//
//	gk.base-branch main
//	gk.log.format %h %s
func parseGKLines(raw string) map[string]any {
	result := map[string]any{}

	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Split on first space only; value may contain spaces.
		idx := strings.IndexByte(line, ' ')
		if idx < 0 {
			continue
		}
		fullKey := line[:idx]
		value := line[idx+1:]

		// Strip leading "gk." prefix.
		if !strings.HasPrefix(fullKey, "gk.") {
			continue
		}
		parts := strings.Split(fullKey[3:], ".") // e.g. ["base-branch"] or ["log","format"]

		// Normalise dashes to underscores in each segment.
		for i, p := range parts {
			parts[i] = strings.ReplaceAll(p, "-", "_")
		}

		setNested(result, parts, value)
	}

	return result
}

// setNested inserts value into the nested map following the key path.
func setNested(m map[string]any, keys []string, value string) {
	if len(keys) == 1 {
		m[keys[0]] = value
		return
	}
	sub, ok := m[keys[0]]
	if !ok {
		sub = map[string]any{}
		m[keys[0]] = sub
	}
	if subMap, ok := sub.(map[string]any); ok {
		setNested(subMap, keys[1:], value)
	}
}

// cmdOutput runs a command and returns stdout bytes.
// ctx may be nil; in that case context.Background() is used.
func cmdOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}
