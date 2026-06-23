package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/x-mesh/gk/internal/config"
)

// versionFilePaths projects the resolved version-file specs onto their paths
// for the human render and the --json contract, which both report paths only.
func versionFilePaths(vfs []config.VersionFile) []string {
	out := make([]string, len(vfs))
	for i, vf := range vfs {
		out[i] = vf.Path
	}
	return out
}

// bumpShipVersionFile rewrites the version string in one version file and
// reports whether the bytes changed. The replacement strategy is, in order:
//
//   - spec.Pattern set → a literal {version} template (any text file);
//   - spec.Key set     → a dotted key path into a YAML file;
//   - otherwise        → a native handler chosen by the filename.
//
// An unrecognized filename with no Pattern/Key is an error, not a silent
// skip: the user listed the file expecting a bump, so failing loudly beats
// shipping a release whose version never moved.
func bumpShipVersionFile(spec config.VersionFile, version string) (bool, error) {
	b, err := os.ReadFile(spec.Path)
	if err != nil {
		return false, fmt.Errorf("ship: read version file: %w", err)
	}
	before := string(b)
	var after string
	switch {
	case spec.Pattern != "":
		after, err = bumpVersionByPattern(before, spec.Pattern, version, spec.Path)
	case spec.Key != "":
		after, err = bumpVersionByYAMLKey(before, spec.Key, version, spec.Path)
	default:
		after, err = bumpVersionByFormat(before, filepath.Base(spec.Path), version)
	}
	if err != nil {
		return false, err
	}
	if after == before {
		return false, nil
	}
	if err := os.WriteFile(spec.Path, []byte(after), 0o644); err != nil {
		return false, fmt.Errorf("ship: write version file: %w", err)
	}
	return true, nil
}

var versionJSONRE = regexp.MustCompile(`(?m)("version"\s*:\s*")([^"]+)(")`)

// bumpVersionByFormat dispatches on the filename to a format-aware rewrite.
// The TOML handlers are table-scoped on purpose — a naive `version = "..."`
// replace would also rewrite dependency pins, so each only touches the
// version key under the package's own table.
func bumpVersionByFormat(before, base, version string) (string, error) {
	switch base {
	case "VERSION":
		return version + "\n", nil
	case "package.json", "marketplace.json":
		if !versionJSONRE.MatchString(before) {
			return "", fmt.Errorf("ship: %s has no version field", base)
		}
		return versionJSONRE.ReplaceAllString(before, `${1}`+version+`${3}`), nil
	case "pyproject.toml":
		// PEP 621 puts the version under [project]; Poetry under [tool.poetry].
		return bumpTOMLTableVersion(before, []string{"project", "tool.poetry"}, version, base)
	case "Cargo.toml":
		return bumpTOMLTableVersion(before, []string{"package"}, version, base)
	case "pubspec.yaml", "Chart.yaml":
		return bumpYAMLTopLevelVersion(before, version, base)
	default:
		// Python source modules conventionally carry __version__ = "x.y.z".
		if strings.HasSuffix(base, ".py") {
			return bumpPyDunderVersion(before, version, base)
		}
		return "", fmt.Errorf("ship: don't know how to bump %q — set a `pattern` or `key` for it in ship.version_files", base)
	}
}

var (
	tomlHeaderRE      = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*$`)
	tomlVersionLineRE = regexp.MustCompile(`^(\s*version\s*=\s*")[^"]*(".*)$`)
	pyDunderVersionRE = regexp.MustCompile(`(?m)(__version__\s*=\s*["'])[^"']*(["'])`)
	yamlTopVersionRE  = regexp.MustCompile(`(?m)^(version\s*:\s*).*$`)
)

// bumpTOMLTableVersion rewrites the version key that sits under one of the
// named tables ([project], [tool.poetry], [package]) and nowhere else. It
// tracks the current table header line by line, so a `version = "..."` under
// [tool.poetry.dependencies] or any other table is left untouched.
func bumpTOMLTableVersion(before string, tables []string, version, base string) (string, error) {
	want := make(map[string]bool, len(tables))
	for _, t := range tables {
		want[t] = true
	}
	lines := strings.Split(before, "\n")
	current := "" // the root table, before any [header]
	for i, line := range lines {
		if m := tomlHeaderRE.FindStringSubmatch(line); m != nil {
			current = strings.TrimSpace(m[1])
			continue
		}
		if !want[current] {
			continue
		}
		if m := tomlVersionLineRE.FindStringSubmatch(line); m != nil {
			lines[i] = m[1] + version + m[2]
			return strings.Join(lines, "\n"), nil
		}
	}
	return "", fmt.Errorf("ship: %s has no version key under [%s]", base, strings.Join(tables, "] / ["))
}

// bumpYAMLTopLevelVersion rewrites a column-0 `version:` scalar — the shape
// pubspec.yaml and Helm Chart.yaml use. Nested version keys are ignored.
func bumpYAMLTopLevelVersion(before, version, base string) (string, error) {
	if !yamlTopVersionRE.MatchString(before) {
		return "", fmt.Errorf("ship: %s has no top-level version key", base)
	}
	return yamlTopVersionRE.ReplaceAllString(before, `${1}`+version), nil
}

func bumpPyDunderVersion(before, version, base string) (string, error) {
	if !pyDunderVersionRE.MatchString(before) {
		return "", fmt.Errorf("ship: %s has no __version__ assignment", base)
	}
	return pyDunderVersionRE.ReplaceAllString(before, `${1}`+version+`${2}`), nil
}

// bumpVersionByPattern replaces the text where the single {version} token sits
// in the literal template. The template is matched anchored on its literal
// prefix/suffix, so `__version__ = "{version}"` rewrites only the quoted value
// and nothing else on the line.
func bumpVersionByPattern(before, pattern, version, path string) (string, error) {
	const token = "{version}"
	prefix, suffix, ok := strings.Cut(pattern, token)
	if !ok {
		return "", fmt.Errorf("ship: pattern for %s must contain the %s placeholder", filepath.Base(path), token)
	}
	if strings.Contains(suffix, token) {
		return "", fmt.Errorf("ship: pattern for %s must contain exactly one %s placeholder", filepath.Base(path), token)
	}
	reStr := regexp.QuoteMeta(prefix) + `(.+?)` + regexp.QuoteMeta(suffix)
	re, err := regexp.Compile(reStr)
	if err != nil {
		return "", fmt.Errorf("ship: bad pattern for %s: %w", filepath.Base(path), err)
	}
	loc := re.FindStringSubmatchIndex(before)
	if loc == nil {
		return "", fmt.Errorf("ship: pattern %q not found in %s", pattern, filepath.Base(path))
	}
	// loc[2]:loc[3] spans capture group 1 — the old version text.
	return before[:loc[2]] + version + before[loc[3]:], nil
}

// bumpVersionByYAMLKey rewrites the scalar at a dotted key path (e.g.
// "version" or "tool.poetry.version") while preserving the file's comments
// and structure via a yaml.Node round-trip.
func bumpVersionByYAMLKey(before, key, version, path string) (string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(before), &root); err != nil {
		return "", fmt.Errorf("ship: parse %s: %w", filepath.Base(path), err)
	}
	if len(root.Content) == 0 {
		return "", fmt.Errorf("ship: %s is empty", filepath.Base(path))
	}
	node := root.Content[0]
	parts := strings.Split(key, ".")
	for i, part := range parts {
		if node.Kind != yaml.MappingNode {
			return "", fmt.Errorf("ship: %s: %q is not a mapping in %s", strings.Join(parts[:i], "."), part, filepath.Base(path))
		}
		found := false
		for j := 0; j+1 < len(node.Content); j += 2 {
			if node.Content[j].Value != part {
				continue
			}
			if i == len(parts)-1 {
				val := node.Content[j+1]
				val.Tag = "!!str" // keep 1.2 a string, not a float, on the round-trip
				val.Value = version
				val.Style = 0
			} else {
				node = node.Content[j+1]
			}
			found = true
			break
		}
		if !found {
			return "", fmt.Errorf("ship: key %q not found in %s", key, filepath.Base(path))
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return "", fmt.Errorf("ship: re-encode %s: %w", filepath.Base(path), err)
	}
	_ = enc.Close()
	return buf.String(), nil
}
