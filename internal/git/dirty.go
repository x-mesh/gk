package git

// DirtyFlags summarizes uncommitted state of a worktree as classified
// from `git status --porcelain` output. Each flag is set when at least
// one path matches; the parser does not count occurrences.
type DirtyFlags struct {
	Modified bool // tracked file changed in working tree (Y position)
	Staged   bool // index has changes (X position)
	Conflict bool // unmerged path (U in either position) — overrides
}

// Clean reports whether all flags are unset.
func (d DirtyFlags) Clean() bool {
	return !d.Modified && !d.Staged && !d.Conflict
}

// ParsePorcelainV1 classifies `git status --porcelain[=v1] -z` output
// into DirtyFlags. Input is the raw stdout (entries are NUL-terminated
// when -z is used; the parser also handles newline-terminated output
// for callers that didn't pass -z). Untracked (`??`) and ignored (`!!`)
// entries are intentionally skipped — they're considered noise for the
// "branch has uncommitted work" signal.
//
// Format reminder (porcelain v1):
//
//	XY <path>           (X=index, Y=working tree)
//	`R  old -> new`     (rename — X='R', Y=' ')
//	`?? path`           (untracked — skipped)
//	`UU path`           (unmerged — flagged as conflict)
func ParsePorcelainV1(raw []byte) DirtyFlags {
	var d DirtyFlags
	for _, entry := range splitStatusEntries(raw) {
		if len(entry) < 2 {
			continue
		}
		x, y := entry[0], entry[1]
		// Untracked / ignored — skip.
		if x == '?' || x == '!' {
			continue
		}
		// Conflict — git-status(1) lists these XY codes for unmerged
		// paths: DD, AU, UD, UA, DU, AA, UU. Note `AA` and `DD` have
		// no U, so we can't shortcut on U-presence.
		if isConflictXY(x, y) {
			d.Conflict = true
			continue
		}
		// Staged: X is set (non-space, non-untracked, non-ignored).
		if x != ' ' && x != '.' {
			d.Staged = true
		}
		// Modified: Y is set similarly.
		if y != ' ' && y != '.' {
			d.Modified = true
		}
		// Early exit — once all flags are set there's nothing left to
		// learn from later entries.
		if d.Modified && d.Staged && d.Conflict {
			return d
		}
	}
	return d
}

// isConflictXY returns true for XY codes that git-status(1) defines
// as unmerged paths: DD, AU, UD, UA, DU, AA, UU.
func isConflictXY(x, y byte) bool {
	switch string([]byte{x, y}) {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	}
	return false
}

// splitStatusEntries splits porcelain output. With `-z` flag, entries
// are NUL-terminated and rename targets follow as a separate NUL-
// terminated record (we ignore the rename target). Without `-z`, each
// entry is a line; rename appears as `R  old -> new` on one line.
func splitStatusEntries(raw []byte) [][]byte {
	if len(raw) == 0 {
		return nil
	}
	// Heuristic: presence of NUL → -z mode.
	hasNUL := false
	for _, b := range raw {
		if b == 0 {
			hasNUL = true
			break
		}
	}
	var sep byte = '\n'
	if hasNUL {
		sep = 0
	}
	var out [][]byte
	skipNext := false
	start := 0
	for i := 0; i <= len(raw); i++ {
		if i == len(raw) || raw[i] == sep {
			if skipNext {
				skipNext = false
				start = i + 1
				continue
			}
			if i > start {
				entry := raw[start:i]
				out = append(out, entry)
				// In -z mode a rename entry is followed by an extra
				// NUL-terminated record holding the source path. Skip
				// it so we don't treat "old/path" as a status code.
				if hasNUL && len(entry) >= 2 && (entry[0] == 'R' || entry[0] == 'C') {
					skipNext = true
				}
			}
			start = i + 1
		}
	}
	return out
}
