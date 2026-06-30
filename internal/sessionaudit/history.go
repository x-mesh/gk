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
