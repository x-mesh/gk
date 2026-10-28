// Command genrules converts a gitleaks default config (gitleaks.toml) into the
// JSON ruleset that internal/secrets/gitleaksrules embeds.
//
// It fails on any TOML key it does not model, so a gitleaks format change
// breaks generation instead of silently dropping a rule condition.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"

	"github.com/pelletier/go-toml/v2"
)

type tomlFile struct {
	Title      string     `toml:"title"`
	MinVersion string     `toml:"minVersion"`
	Allowlist  tomlAllow  `toml:"allowlist"`
	Rules      []tomlRule `toml:"rules"`
}

type tomlAllow struct {
	Description string   `toml:"description"`
	Paths       []string `toml:"paths"`
	Regexes     []string `toml:"regexes"`
	Stopwords   []string `toml:"stopwords"`
}

type tomlRule struct {
	ID          string          `toml:"id"`
	Description string          `toml:"description"`
	Regex       string          `toml:"regex"`
	Path        string          `toml:"path"`
	Keywords    []string        `toml:"keywords"`
	Entropy     float64         `toml:"entropy"`
	SecretGroup int             `toml:"secretGroup"`
	Allowlists  []tomlRuleAllow `toml:"allowlists"`
}

type tomlRuleAllow struct {
	Description string   `toml:"description"`
	Condition   string   `toml:"condition"`
	RegexTarget string   `toml:"regexTarget"`
	Regexes     []string `toml:"regexes"`
	Paths       []string `toml:"paths"`
	Stopwords   []string `toml:"stopwords"`
}

type outFile struct {
	Source    outSource `json:"source"`
	Allowlist outAllow  `json:"allowlist"`
	Rules     []outRule `json:"rules"`
}

type outSource struct {
	GitleaksVersion string `json:"gitleaksVersion"`
	ConfigSHA256    string `json:"configSha256"`
	MinVersion      string `json:"minVersion"`
}

type outAllow struct {
	Paths     []string `json:"paths,omitempty"`
	Regexes   []string `json:"regexes,omitempty"`
	Stopwords []string `json:"stopwords,omitempty"`
}

type outRule struct {
	ID          string         `json:"id"`
	Description string         `json:"description,omitempty"`
	Regex       string         `json:"regex,omitempty"`
	Path        string         `json:"path,omitempty"`
	Keywords    []string       `json:"keywords,omitempty"`
	Entropy     float64        `json:"entropy,omitempty"`
	SecretGroup int            `json:"secretGroup,omitempty"`
	Allowlists  []outRuleAllow `json:"allowlists,omitempty"`
}

type outRuleAllow struct {
	Condition   string   `json:"condition,omitempty"`
	RegexTarget string   `json:"regexTarget,omitempty"`
	Regexes     []string `json:"regexes,omitempty"`
	Paths       []string `json:"paths,omitempty"`
	Stopwords   []string `json:"stopwords,omitempty"`
}

func main() {
	in := flag.String("in", "", "path to gitleaks.toml")
	version := flag.String("version", "", "gitleaks release the config came from, e.g. v8.30.1")
	out := flag.String("out", "", "path to write rules.json")
	flag.Parse()
	if *in == "" || *version == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: genrules -in gitleaks.toml -version vX.Y.Z -out rules.json")
		os.Exit(2)
	}
	if err := run(*in, *version, *out); err != nil {
		fmt.Fprintln(os.Stderr, "genrules:", err)
		os.Exit(1)
	}
}

func run(in, version, out string) error {
	raw, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	var cfg tomlFile
	dec := toml.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return fmt.Errorf("decode %s: %w", in, err)
	}

	res := outFile{
		Source: outSource{
			GitleaksVersion: version,
			ConfigSHA256:    fmt.Sprintf("%x", sha256.Sum256(raw)),
			MinVersion:      cfg.MinVersion,
		},
		Allowlist: outAllow{Paths: cfg.Allowlist.Paths, Regexes: cfg.Allowlist.Regexes, Stopwords: cfg.Allowlist.Stopwords},
	}
	if err := compileAll("global allowlist", cfg.Allowlist.Paths, cfg.Allowlist.Regexes); err != nil {
		return err
	}

	seen := map[string]bool{}
	for _, r := range cfg.Rules {
		if r.ID == "" || seen[r.ID] {
			return fmt.Errorf("rule id %q is empty or duplicated", r.ID)
		}
		seen[r.ID] = true
		if r.Regex == "" && r.Path == "" {
			return fmt.Errorf("rule %s has neither regex nor path", r.ID)
		}
		if err := compileAll("rule "+r.ID, []string{r.Regex, r.Path}); err != nil {
			return err
		}
		or := outRule{
			ID: r.ID, Description: r.Description, Regex: r.Regex, Path: r.Path,
			Keywords: r.Keywords, Entropy: r.Entropy, SecretGroup: r.SecretGroup,
		}
		for _, a := range r.Allowlists {
			if err := validateRuleAllow(r.ID, a); err != nil {
				return err
			}
			or.Allowlists = append(or.Allowlists, outRuleAllow{
				Condition: a.Condition, RegexTarget: a.RegexTarget,
				Regexes: a.Regexes, Paths: a.Paths, Stopwords: a.Stopwords,
			})
		}
		res.Rules = append(res.Rules, or)
	}

	data, err := json.MarshalIndent(res, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("genrules: %d rules from gitleaks %s -> %s (%d bytes)\n", len(res.Rules), version, out, len(data))
	return nil
}

func validateRuleAllow(ruleID string, a tomlRuleAllow) error {
	switch a.RegexTarget {
	case "", "secret", "match", "line":
	default:
		return fmt.Errorf("rule %s: unsupported regexTarget %q", ruleID, a.RegexTarget)
	}
	switch a.Condition {
	case "", "OR", "AND":
	default:
		return fmt.Errorf("rule %s: unsupported condition %q", ruleID, a.Condition)
	}
	return compileAll("rule "+ruleID+" allowlist", a.Regexes, a.Paths)
}

// compileAll rejects patterns Go's regexp cannot compile, which is the engine's
// only regex dialect.
func compileAll(where string, groups ...[]string) error {
	for _, g := range groups {
		for _, p := range g {
			if p == "" {
				continue
			}
			if _, err := regexp.Compile(p); err != nil {
				return fmt.Errorf("%s: pattern %q: %w", where, p, err)
			}
		}
	}
	return nil
}
