package cli

import (
	"context"
	"fmt"
	"sort"

	"github.com/x-mesh/gk/internal/config"
)

const findJevCandidateLimit = 50

type findRanking struct {
	Enabled        bool   `json:"enabled"`
	Model          string `json:"model,omitempty"`
	CandidateCount int    `json:"candidate_count"`
	Evaluated      int    `json:"evaluated"`
	Limited        bool   `json:"limited"`
	Skipped        string `json:"skipped,omitempty"`
}

func rerankFindResult(ctx context.Context, res *findResult, q findQuery, cfg config.JevConfig) error {
	all := res.allMatches
	if len(all) == 0 {
		all = res.Matches
	}
	if q.query == "" {
		res.Ranking = &findRanking{Skipped: "path-only search"}
		return nil
	}
	if len(all) <= 1 {
		res.Ranking = &findRanking{CandidateCount: len(all), Skipped: "fewer than two candidates"}
		return nil
	}
	count := len(all)
	if count > findJevCandidateLimit {
		count = findJevCandidateLimit
	}
	candidates := make([]jevCandidate, 0, count)
	stateCandidates := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		match := all[i]
		id := match.Hash
		candidates = append(candidates, jevCandidate{ID: id, Text: match.Subject})
		stateCandidates = append(stateCandidates, map[string]any{
			"id": id, "subject": truncateRunes(match.Subject, 512), "matched": match.Matched,
		})
	}
	scores, info, err := scoreWithJev(ctx, cfg, "find", map[string]any{
		"query": q.query, "candidates": stateCandidates,
	}, candidates)
	if err != nil {
		return err
	}
	segment := append([]findMatch(nil), all[:count]...)
	sort.SliceStable(segment, func(i, j int) bool {
		return scores[segment[i].Hash] > scores[segment[j].Hash]
	})
	ordered := append(segment, all[count:]...)
	res.allMatches = ordered
	if len(ordered) > q.limit {
		ordered = ordered[:q.limit]
	}
	res.Matches = ordered
	res.Count = len(ordered)
	res.Ranking = &findRanking{
		Enabled:        true,
		Model:          info.Model,
		CandidateCount: len(all),
		Evaluated:      count,
		Limited:        len(all) > count,
	}
	return nil
}

func renderFindRanking(res findResult, format func(string, ...any)) {
	if res.Ranking == nil {
		return
	}
	if res.Ranking.Skipped != "" {
		format("Jev ranking skipped: %s\n", res.Ranking.Skipped)
		return
	}
	format("Jev ranking: %d/%d candidates evaluated with %s", res.Ranking.Evaluated, res.Ranking.CandidateCount, res.Ranking.Model)
	if res.Ranking.Limited {
		format(" (capped at %d)", findJevCandidateLimit)
	}
	format("\n")
}

// applyFindRanking runs the optional Jev rerank. gk find read no config before
// ranking existed, so a config that fails to load must not fail a search that
// never asked for ranking: the rerank is skipped and the reason is recorded in
// res.Ranking, which --json callers see.
func applyFindRanking(ctx context.Context, res *findResult, q findQuery, cfg *config.Config, cfgErr error) error {
	if cfgErr != nil {
		res.Ranking = &findRanking{Skipped: "config not loaded: " + cfgErr.Error()}
		return nil
	}
	if cfg == nil || !cfg.AI.Jev.FindRerank {
		return nil
	}
	if err := rerankFindResult(ctx, res, q, cfg.AI.Jev); err != nil {
		return invalidFindJevConfig(err)
	}
	return nil
}

func invalidFindJevConfig(err error) error {
	return fmt.Errorf("gk find: %w", err)
}
