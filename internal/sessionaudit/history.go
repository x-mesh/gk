package sessionaudit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// HistoryEntry is one recorded audit run, so turn-reduction adoption can be
// tracked over time (is collapsible-raw-turn count trending down?). Timestamp
// is set by the caller (time.Now at the CLI) to keep this package deterministic.
type HistoryEntry struct {
	Timestamp           string         `json:"ts"`
	Files               int            `json:"files"`
	GitTurns            int            `json:"git_turns"`
	EstimatedTurnsSaved int            `json:"estimated_turns_saved"`
	Rate                float64        `json:"rate"`
	AdoptionRate        float64        `json:"adoption_rate"`
	ByGroup             map[string]int `json:"by_group,omitempty"`
	// The git-kit lens, recorded alongside rather than folded in — the fields
	// above must keep meaning exactly what they meant in older entries, or the
	// trend readers comparing across runs get a third silent re-baseline. Absent
	// on every entry written before this existed, which is the honest signal
	// that the number was not measured then.
	GkTurns        int `json:"gk_turns,omitempty"`
	GkReprobeSaved int `json:"gk_reprobe_saved,omitempty"`
	// ByProject is what the aggregate rates above cannot say: whether a swing
	// came from behaviour changing or from the SCAN WINDOW changing. A recorded
	// run covers the newest N session files, so a project that goes quiet drops
	// out and a fresh one drops in — and a single un-onboarded project with
	// heavy raw-git use can move AdoptionRate several points without anyone's
	// habits changing. Without this the two are indistinguishable after the
	// fact, and the honest verdict on any swing is "cause unknown". Absent on
	// entries written before this existed — not measured then, not zero.
	ByProject []ProjectShare `json:"by_project,omitempty"`
}

// ProjectShare is one project's slice of a recorded run. Deliberately its own
// minimal type rather than a reuse of ProjectAdoption: this file is read ACROSS
// time, so its shape must not drift every time the report's project row grows a
// field. Rate is omitted because it is derivable (GitKit over the sum), and a
// stored derived value is one more thing that can disagree with itself later.
type ProjectShare struct {
	Project string `json:"project"`
	Files   int    `json:"files"`
	RawGit  int    `json:"raw_git"`
	GitKit  int    `json:"git_kit"`
	GKShort int    `json:"gk_short,omitempty"`
}

// HistoryPath is where recorded runs accumulate. The audit is global (it scans
// the home session roots, not a repo), so history lives under the home, not a
// repo's .gk.
func HistoryPath(home string) string {
	return filepath.Join(home, ".gk", "audit-history.jsonl")
}

// AppendHistory appends one entry as a JSON line, creating the directory and
// file as needed.
func AppendHistory(path string, e HistoryEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// ReadHistory reads all recorded entries in file order (oldest first). A missing
// file is not an error: it returns an empty slice.
func ReadHistory(path string) ([]HistoryEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []HistoryEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e HistoryEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Sparkline renders a numeric series as block characters, scaled between the
// series min and max. Empty series → empty string; a flat series renders as the
// lowest block.
func Sparkline(vals []float64) string {
	if len(vals) == 0 {
		return ""
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	min, max := vals[0], vals[0]
	for _, v := range vals {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	var b strings.Builder
	for _, v := range vals {
		idx := 0
		if max > min {
			idx = int((v-min)/(max-min)*float64(len(blocks)-1) + 0.5)
		}
		b.WriteRune(blocks[idx])
	}
	return b.String()
}
