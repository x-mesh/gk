package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// This file implements writeable config: `gk config set/unset`. The design
// goal is to mutate exactly one dot-key in a YAML file while leaving every
// other line — comments, ordering, blank lines — byte-for-byte intact. We do
// that with the yaml.v3 Node API rather than Marshal(struct), which would drop
// the rich comments the scaffolded template carries.

// ErrUnknownKey is returned when a dot-key does not correspond to any field in
// the Config schema (and is not under a dynamic map like ai.providers).
var ErrUnknownKey = errors.New("unknown config key")

// ErrNotScalar is returned when a key addresses a list/map node that `set`
// cannot write a single scalar into (e.g. ai.commit.deny_paths).
var ErrNotScalar = errors.New("key holds a list or map, not a scalar")

// dynamicMapPrefixes are dot-key prefixes under which arbitrary user-chosen
// keys are valid even though they don't appear in Defaults() — they decode
// into Go maps, not structs. Anything at or below these is accepted.
var dynamicMapPrefixes = []string{
	"ai.providers.",
	"clone.hosts.",
}

// UnknownKeys returns every dot-key explicitly set in the YAML file at path
// that is not part of the Config schema — i.e. typos or stale fields that Load
// silently ignores. Keys under a dynamic-map prefix (ai.providers.*) are
// skipped since arbitrary names are valid there. A missing file yields nil.
func UnknownKeys(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gk config: read %s: %w", path, err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("gk config: parse %s: %w", path, err)
	}

	var unknown []string
	var walk func(prefix string, node any)
	walk = func(prefix string, node any) {
		if sub, ok := node.(map[string]any); ok {
			for k, v := range sub {
				child := k
				if prefix != "" {
					child = prefix + "." + k
				}
				if underDynamicPrefix(child) {
					continue
				}
				walk(child, v)
			}
			return
		}
		// Leaf: flag it when the path doesn't exist in the schema at all. We
		// use schemaHasPath (not ValidKey) so a present-but-empty section like
		// `refresh:` (null in the template) isn't mistaken for a stray key.
		if prefix != "" && !schemaHasPath(prefix) {
			unknown = append(unknown, prefix)
		}
	}
	walk("", m)
	sort.Strings(unknown)
	return unknown, nil
}

// underDynamicPrefix reports whether dotKey sits under a dynamic-map prefix
// where arbitrary user keys are legitimate.
func underDynamicPrefix(dotKey string) bool {
	for _, p := range dynamicMapPrefixes {
		if strings.HasPrefix(dotKey, p) {
			return true
		}
	}
	return false
}

// ValueSource reports which file supplies the effective value for dotKey:
// "local" (repo .gk.yaml), "global" ($XDG_CONFIG_HOME/gk/config.yaml), or
// "default" (built-in, no file sets it). repoRoot may be "" outside a repo.
// Note: this inspects the two config files only — it does not account for
// GK_* env vars or git-config gk.* overrides, which also feed Load().
func ValueSource(dotKey, repoRoot string) string {
	if repoRoot != "" && keyInFile(LocalConfigPath(repoRoot), dotKey) {
		return "local"
	}
	if keyInFile(GlobalConfigPath(), dotKey) {
		return "global"
	}
	return "default"
}

// keyInFile reports whether the YAML file at path explicitly sets dotKey.
func keyInFile(path, dotKey string) bool {
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return false
	}
	cur := any(m)
	for _, seg := range strings.Split(dotKey, ".") {
		sub, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur, ok = sub[seg]
		if !ok {
			return false
		}
	}
	return true
}

// LocalConfigPath returns the repo-local .gk.yaml path for a working-tree root,
// or "" when root is empty.
func LocalConfigPath(repoRoot string) string {
	if repoRoot == "" {
		return ""
	}
	return filepath.Join(repoRoot, ".gk.yaml")
}

// LocalConfigHeader is written at the top of a freshly created .gk.yaml so the
// file explains itself. The global template (default_config.yaml) is unsuited
// to repo-local files — it documents every field as a starting point, whereas
// .gk.yaml should hold only the handful of repo overrides.
const LocalConfigHeader = "# gk repo-local config — overrides ~/.config/gk/config.yaml\n# See `gk config init` for the fully-documented global template.\n"

// ValidKey reports whether dotKey is a settable Config field. A key is valid if
// it resolves to a leaf in the Defaults() schema, or it sits under a known
// dynamic map prefix (ai.providers.<name>.*, clone.hosts.<name>.*).
func ValidKey(dotKey string) bool {
	for _, p := range dynamicMapPrefixes {
		if strings.HasPrefix(dotKey, p) {
			return true
		}
	}
	_, ok := schemaLeaf(dotKey)
	return ok
}

// schemaLeaf walks the Defaults() schema (as a nested map) along dotKey and
// returns the default value at that path. ok is false if the path doesn't
// exist or stops short of a leaf.
func schemaLeaf(dotKey string) (any, bool) {
	raw, err := yaml.Marshal(Defaults())
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, false
	}
	cur := any(m)
	for _, seg := range strings.Split(dotKey, ".") {
		sub, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = sub[seg]
		if !ok {
			return nil, false
		}
	}
	// A mapping is a section, not a settable leaf.
	if _, isMap := cur.(map[string]any); isMap {
		return cur, false
	}
	return cur, true
}

// schemaHasPath reports whether dotKey exists anywhere in the Defaults() schema
// — as a leaf OR a section. Unlike ValidKey (leaf-only, for `set`), this lets
// config doctor accept present-but-empty sections.
func schemaHasPath(dotKey string) bool {
	raw, err := yaml.Marshal(Defaults())
	if err != nil {
		return false
	}
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return false
	}
	cur := any(m)
	for _, seg := range strings.Split(dotKey, ".") {
		sub, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur, ok = sub[seg]
		if !ok {
			return false
		}
	}
	return true
}

// SetValue writes rawValue at dotKey in the YAML file at path, preserving all
// surrounding comments and ordering. The file is created (empty root) if
// missing — callers that want the documented template scaffolded first should
// do so before calling. It returns the typed value as serialized.
func SetValue(path, dotKey, rawValue string) (string, error) {
	if !ValidKey(dotKey) {
		return "", fmt.Errorf("%w: %s", ErrUnknownKey, dotKey)
	}
	// A list-valued field (e.g. ai.commit.deny_paths) can't take a single
	// scalar — reject up front, even when the file doesn't yet hold the key.
	if def, ok := schemaLeaf(dotKey); ok {
		if _, isSlice := def.([]any); isSlice {
			return "", fmt.Errorf("%w: %s", ErrNotScalar, dotKey)
		}
	}

	root, err := loadRootMapping(path)
	if err != nil {
		return "", err
	}

	valNode := scalarNode(dotKey, rawValue)

	segs := strings.Split(dotKey, ".")
	if err := setInMapping(root, segs, valNode); err != nil {
		return "", err
	}

	if err := writeDoc(path, root); err != nil {
		return "", err
	}
	return valNode.Value, nil
}

// UnsetValue removes dotKey from the YAML file at path, reverting it to its
// built-in default. Returns existed=false (no error) when the key was absent.
func UnsetValue(path, dotKey string) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	root, err := loadRootMapping(path)
	if err != nil {
		return false, err
	}
	segs := strings.Split(dotKey, ".")
	removed := removeFromMapping(root, segs)
	if !removed {
		return false, nil
	}
	if err := writeDoc(path, root); err != nil {
		return false, err
	}
	return true, nil
}

// loadRootMapping reads path and returns its root mapping node, creating an
// empty one when the file is missing or blank.
func loadRootMapping(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
		}
		return nil, fmt.Errorf("gk config: read %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("gk config: parse %s: %w", path, err)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("gk config: %s root is not a mapping", path)
	}
	return root, nil
}

// setInMapping descends m along segs, creating intermediate mappings as needed,
// and sets the final key's value to valNode. It refuses to overwrite a node
// that currently holds a list or non-mapping where a mapping is required.
func setInMapping(m *yaml.Node, segs []string, valNode *yaml.Node) error {
	key := segs[0]
	if len(segs) == 1 {
		if existing := mappingValue(m, key); existing != nil {
			if existing.Kind == yaml.MappingNode || existing.Kind == yaml.SequenceNode {
				return fmt.Errorf("%w: %s", ErrNotScalar, key)
			}
			// Preserve the inline comment trailing the old value.
			valNode.LineComment = existing.LineComment
			*existing = *valNode
			return nil
		}
		appendKV(m, key, valNode)
		return nil
	}

	child := mappingValue(m, key)
	if child == nil {
		child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		appendKV(m, key, child)
	}
	if child.Kind != yaml.MappingNode {
		return fmt.Errorf("gk config: %q is not a section", key)
	}
	return setInMapping(child, segs[1:], valNode)
}

// removeFromMapping descends along segs and removes the final key. Returns true
// when a key was actually removed.
func removeFromMapping(m *yaml.Node, segs []string) bool {
	key := segs[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			continue
		}
		if len(segs) == 1 {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
		val := m.Content[i+1]
		if val.Kind != yaml.MappingNode {
			return false
		}
		removed := removeFromMapping(val, segs[1:])
		// Drop a section that just became empty so unset doesn't leave a
		// dangling `status: {}` behind.
		if removed && len(val.Content) == 0 {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
		}
		return removed
	}
	return false
}

// mappingValue returns the value node for key in mapping m, or nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// appendKV appends a key/value pair to mapping m.
func appendKV(m *yaml.Node, key string, val *yaml.Node) {
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		val,
	)
}

// scalarNode builds a scalar value node for rawValue, choosing its YAML tag
// from the schema default's type when known, else inferring from the literal.
func scalarNode(dotKey, rawValue string) *yaml.Node {
	tag := "!!str"
	if def, ok := schemaLeaf(dotKey); ok {
		switch def.(type) {
		case bool:
			if _, err := strconv.ParseBool(rawValue); err == nil {
				tag = "!!bool"
			}
		case int, int64:
			if _, err := strconv.ParseInt(rawValue, 10, 64); err == nil {
				tag = "!!int"
			}
		case float32, float64:
			if _, err := strconv.ParseFloat(rawValue, 64); err == nil {
				tag = "!!float"
			}
		}
	} else {
		// No schema hint (dynamic-map key): infer from the literal.
		switch {
		case rawValue == "true" || rawValue == "false":
			tag = "!!bool"
		case isIntLiteral(rawValue):
			tag = "!!int"
		}
	}
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: rawValue}
	// Quote strings that YAML would otherwise misread (e.g. "true", "123",
	// "null", or anything with leading/trailing space).
	if tag == "!!str" && needsQuoting(rawValue) {
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
}

func isIntLiteral(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}

// needsQuoting reports whether a string scalar must be quoted to round-trip as
// a string rather than being reinterpreted as a bool/int/null by a YAML reader.
func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	if s != strings.TrimSpace(s) {
		return true
	}
	switch strings.ToLower(s) {
	case "true", "false", "null", "yes", "no", "on", "off", "~":
		return true
	}
	return isIntLiteral(s)
}

// writeDoc serializes root (wrapped in a document) back to path with a trailing
// newline, creating parent directories as needed.
func writeDoc(path string, root *yaml.Node) error {
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("gk config: marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("gk config: mkdir: %w", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("gk config: write %s: %w", path, err)
	}
	return nil
}
