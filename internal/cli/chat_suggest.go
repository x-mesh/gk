package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/x-mesh/gk/internal/config"
)

// Bounds on one gk_suggest result. The tool feeds a single trailing
// suggestion line, not a browsable catalogue: a handful of candidates is
// enough for the model to pick the right one, and anything more just spends
// context on commands it will not name.
const (
	suggestMaxMatches   = 5
	suggestMaxFlags     = 8
	suggestMaxExampleLn = 3
	// suggestMinScore keeps a single incidental word match (a stray "file" in
	// some Long text) from surfacing an unrelated command. A real match nearly
	// always hits the command name or its summary, which score well above this.
	suggestMinScore = 3
	// suggestLongScanLimit caps how much of a Long description is scanned for
	// keywords. Long texts run to paragraphs of prose; matching all of it makes
	// verbose commands win on volume rather than relevance.
	suggestLongScanLimit = 400
	suggestMaxCandidates = 256
	suggestJevMinScore   = 1.5
	suggestJevBatchSize  = 64
)

// suggestFlag is one notable flag of a matched command.
type suggestFlag struct {
	Flag  string `json:"flag"`
	Usage string `json:"usage,omitempty"`
}

// suggestMatch is one command gk_suggest offers as a follow-up action.
type suggestMatch struct {
	Command string        `json:"command"`
	Summary string        `json:"summary,omitempty"`
	Flags   []suggestFlag `json:"flags,omitempty"`
	Example string        `json:"example,omitempty"`
}

// suggestResult is the tool's JSON payload. Note is present only when there
// is no match, so the model reads an explicit "gk has nothing for this"
// instead of inferring it from an empty array.
type suggestResult struct {
	Matches []suggestMatch `json:"matches"`
	Note    string         `json:"note,omitempty"`
}

// chatSuggestLookup returns the gk_suggest handler bound to the live cobra
// tree reachable from cmd. Walking the real tree — rather than a table
// maintained alongside it — is the whole point: a suggestion can only name a
// command that actually exists in this build, so the tool cannot invent one
// and cannot go stale when commands are added or renamed.
func chatSuggestLookup(cmd *cobra.Command) func(context.Context, string) (string, error) {
	root := cmd.Root()
	return func(_ context.Context, intent string) (string, error) {
		matches := suggestCommands(root, intent)
		res := suggestResult{Matches: matches}
		if len(matches) == 0 {
			res.Matches = []suggestMatch{}
			res.Note = "No gk command matches this intent. Do not invent one — either suggest nothing or say plainly that gk has no command for it."
		}
		out, err := json.Marshal(res)
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
}

func chatSuggestLookupWithJev(cmd *cobra.Command, jev config.JevConfig) func(context.Context, string) (string, error) {
	root := cmd.Root()
	return func(ctx context.Context, intent string) (string, error) {
		if !jev.Suggest {
			return chatSuggestLookup(cmd)(ctx, intent)
		}
		catalogue, err := suggestCatalogue(root)
		if err != nil {
			return "", err
		}
		if len(catalogue) > suggestMaxCandidates {
			return "", fmt.Errorf("gk_suggest: command catalogue exceeds %d candidates", suggestMaxCandidates)
		}
		stateCandidates := make([]map[string]any, 0, len(catalogue))
		jevCandidates := make([]jevCandidate, 0, len(catalogue))
		for i, match := range catalogue {
			id := fmt.Sprintf("command-%d", i)
			stateCandidates = append(stateCandidates, map[string]any{
				"id": id, "path": match.Command, "summary": truncateRunes(match.Summary, 512), "flags": match.Flags,
			})
			jevCandidates = append(jevCandidates, jevCandidate{ID: id, Text: match.Command + " " + match.Summary})
		}
		intent = truncateRunes(intent, 200)
		scores := make(map[string]float64, len(jevCandidates))
		batchCount := (len(jevCandidates) + suggestJevBatchSize - 1) / suggestJevBatchSize
		for start := 0; start < len(jevCandidates); start += suggestJevBatchSize {
			end := min(start+suggestJevBatchSize, len(jevCandidates))
			batchScores, _, err := scoreWithJev(ctx, jev, "suggest", map[string]any{
				"intent": intent, "candidates": stateCandidates[start:end],
			}, jevCandidates[start:end])
			if err != nil {
				batch := start/suggestJevBatchSize + 1
				return "", fmt.Errorf("gk_suggest: Jev batch %d/%d: %w", batch, batchCount, err)
			}
			for id, score := range batchScores {
				scores[id] = score
			}
		}
		type scored struct {
			match suggestMatch
			score float64
		}
		selected := make([]scored, 0, suggestMaxMatches)
		for i, match := range catalogue {
			if scores[fmt.Sprintf("command-%d", i)] < suggestJevMinScore {
				continue
			}
			selected = append(selected, scored{match: match, score: scores[fmt.Sprintf("command-%d", i)]})
		}
		sort.SliceStable(selected, func(i, j int) bool {
			if selected[i].score != selected[j].score {
				return selected[i].score > selected[j].score
			}
			return selected[i].match.Command < selected[j].match.Command
		})
		if len(selected) > suggestMaxMatches {
			selected = selected[:suggestMaxMatches]
		}
		matches := make([]suggestMatch, 0, len(selected))
		for _, item := range selected {
			matches = append(matches, item.match)
		}
		res := suggestResult{Matches: matches}
		if len(matches) == 0 {
			res.Matches = []suggestMatch{}
			res.Note = "No gk command matches this intent. Do not invent one — either suggest nothing or say plainly that gk has no command for it."
		}
		out, err := json.Marshal(res)
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
}

func suggestCatalogue(root *cobra.Command) ([]suggestMatch, error) {
	var matches []suggestMatch
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if suggestEligible(sub) {
				matches = append(matches, suggestMatch{
					Command: sub.CommandPath(),
					Summary: truncateRunes(sub.Short, 512),
					Flags:   notableFlags(sub),
				})
			}
			walk(sub)
		}
	}
	walk(root)
	sort.Slice(matches, func(i, j int) bool { return matches[i].Command < matches[j].Command })
	return matches, nil
}

// suggestCommands scores every runnable command in the tree against the
// intent keywords and returns the best few.
func suggestCommands(root *cobra.Command, intent string) []suggestMatch {
	terms := suggestTerms(intent)
	if len(terms) == 0 {
		return nil
	}

	type scored struct {
		cmd   *cobra.Command
		score int
		path  string
	}
	var candidates []scored

	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if !suggestEligible(sub) {
				walk(sub)
				continue
			}
			if s, ok := scoreCommand(sub, terms); ok && s >= suggestMinScore {
				candidates = append(candidates, scored{cmd: sub, score: s, path: sub.CommandPath()})
			}
			walk(sub)
		}
	}
	walk(root)

	// Ties break on path so the same intent always yields the same list —
	// a suggestion that reshuffles between identical questions reads as
	// nondeterminism in the answer itself.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].path < candidates[j].path
	})

	if len(candidates) > suggestMaxMatches {
		candidates = candidates[:suggestMaxMatches]
	}
	out := make([]suggestMatch, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, suggestMatch{
			Command: c.path,
			Summary: c.cmd.Short,
			Flags:   notableFlags(c.cmd),
			Example: firstLines(c.cmd.Example, suggestMaxExampleLn),
		})
	}
	return out
}

// suggestEligible reports whether a command can be offered as a follow-up
// action. Hidden and deprecated commands are excluded because suggesting
// them is worse than suggesting nothing; cobra's own help/completion
// scaffolding is excluded because it answers no user intent; and a command
// with no Run is a pure group ("gk branch"), whose children carry the real
// action.
func suggestEligible(c *cobra.Command) bool {
	if c.Hidden || c.Deprecated != "" {
		return false
	}
	switch c.Name() {
	case "help", "completion":
		return false
	}
	return c.Runnable()
}

// scoreCommand ranks one command against the intent terms. The weights
// encode where a term match is most likely to mean what it seems to mean:
// the command's own name and aliases are deliberate vocabulary, its Short is
// a curated one-liner, and Long/flags are supporting evidence that should
// never outweigh the name.
//
// The bool is the relevance gate, and it is what keeps an off-topic question
// from producing a suggestion at all. Help text is prose, so across ~90
// commands SOME command's summary shares a word with almost any sentence
// ("deploy to kubernetes and order pizza" reaches `gk batch` through "order"
// in "an ordered JSON plan"). Scoring alone cannot separate that from a real
// match, because both are one ordinary word. Requiring either a hit on the
// command's own name/alias or agreement across two distinct terms does:
// coincidence rarely clears either bar, and a genuine intent usually clears
// both. A silent tool beats a confident wrong suggestion here — the model is
// told an empty result means gk has no such command.
func scoreCommand(c *cobra.Command, terms []string) (int, bool) {
	nameTokens := suggestTerms(c.CommandPath())
	aliasTokens := suggestTerms(strings.Join(c.Aliases, " "))
	shortTokens := suggestTerms(c.Short)
	longTokens := suggestTerms(truncateRunes(c.Long, suggestLongScanLimit))
	flagTokens := suggestTerms(flagText(c))

	score, matched := 0, 0
	nameHit := false
	for _, t := range terms {
		switch {
		case containsToken(nameTokens, t):
			score += 10
			nameHit = true
		case containsToken(aliasTokens, t):
			score += 8
			nameHit = true
		case containsToken(shortTokens, t):
			score += 4
		case containsToken(flagTokens, t):
			score += 2
		case containsToken(longTokens, t):
			score += 1
		default:
			continue
		}
		matched++
	}
	return score, nameHit || matched >= 2
}

// flagText joins a command's local flag names and usage strings, so an
// intent phrased after a flag ("gone upstream", "squash merged") can still
// find the command that owns it.
func flagText(c *cobra.Command) string {
	var b strings.Builder
	c.LocalFlags().VisitAll(func(f *pflag.Flag) {
		b.WriteString(f.Name)
		b.WriteByte(' ')
		b.WriteString(f.Usage)
		b.WriteByte(' ')
	})
	return b.String()
}

// notableFlags lists a command's own flags, excluding inherited/global ones
// (--json, --repo, …) which apply everywhere and say nothing about this
// command in particular.
func notableFlags(c *cobra.Command) []suggestFlag {
	var out []suggestFlag
	c.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" || len(out) >= suggestMaxFlags {
			return
		}
		out = append(out, suggestFlag{Flag: "--" + f.Name, Usage: f.Usage})
	})
	return out
}

// suggestStopwords are function words that carry no intent. They are dropped
// from BOTH sides of the comparison, because gk's help text is written as
// prose: without this, "how do I clean up branches" matches any summary
// containing "up", and the relevance gate's two-distinct-terms bar would be
// cleared by grammar rather than meaning.
//
// Words that are gk command names (do, find, log, next, switch, watch, …) are
// deliberately absent even when they read as function words — dropping those
// would make a command unfindable by its own name.
var suggestStopwords = map[string]bool{
	"a": true, "an": true, "the": true, "to": true, "of": true, "for": true,
	"in": true, "on": true, "at": true, "as": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "and": true, "or": true,
	"but": true, "if": true, "my": true, "me": true, "it": true, "its": true,
	"this": true, "that": true, "these": true, "those": true, "with": true,
	"from": true, "into": true, "up": true, "so": true, "than": true,
	"then": true, "there": true, "here": true, "how": true, "what": true,
	"when": true, "where": true, "why": true, "which": true, "who": true,
	"i": true, "you": true, "we": true, "want": true, "need": true,
}

// suggestTerms lowercases and splits text into comparable tokens. Splitting
// on any non-letter/digit rune keeps this usable for the mixed-language help
// text gk actually ships (several commands have Korean Short lines), because
// CJK runes survive as tokens instead of being stripped as punctuation.
// One-character Latin tokens are dropped as noise; a lone CJK character is
// kept, since it can be a whole word.
func suggestTerms(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, f := range fields {
		if len([]rune(f)) < 2 && !isCJK(f) {
			continue
		}
		if suggestStopwords[f] {
			continue
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

func isCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hangul, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			return true
		}
	}
	return false
}

// containsToken matches a term against tokens by prefix in either direction,
// so "branches" finds "branch" and "clean" finds "cleanup" without pulling in
// a stemmer. The 4-rune floor keeps short terms exact — otherwise "log" would
// match "login", "logic", and every "logging" mention in a Long description.
func containsToken(tokens []string, term string) bool {
	tr := []rune(term)
	for _, tok := range tokens {
		if tok == term {
			return true
		}
		if len(tr) < 4 {
			continue
		}
		kr := []rune(tok)
		if len(kr) < 4 {
			continue
		}
		if strings.HasPrefix(tok, term) || strings.HasPrefix(term, tok) {
			return true
		}
	}
	return false
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// firstLines keeps an Example block short — the first lines carry the common
// invocation, the rest are variations the model does not need to see.
func firstLines(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, n)
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		out = append(out, ln)
		if len(out) >= n {
			break
		}
	}
	return strings.Join(out, "\n")
}
