package diff

// Digest is the semantic summary of a diff — what an agent (or human) needs
// to know what changed without reading the patch: per-file change kind,
// hunk count, line deltas, and the symbols (function contexts) the hunks
// touched. It is the data model behind `gk diff --digest`.
type Digest struct {
	Files []FileDigest
	Stat  DigestStat
}

// FileDigest summarizes one file's change.
type FileDigest struct {
	Path    string
	OldPath string // set on rename/copy
	Status  FileStatus
	Binary  bool
	Hunks   int
	Added   int
	Deleted int
	// Symbols are the deduplicated function contexts from this file's hunk
	// headers, in first-seen order — "which functions changed" at a glance.
	Symbols []string
}

// DigestStat aggregates the whole diff.
type DigestStat struct {
	Files   int
	Hunks   int
	Added   int
	Deleted int
}

// BuildDigest reduces a parsed diff to its digest.
func BuildDigest(res *DiffResult) Digest {
	d := Digest{Files: make([]FileDigest, 0, len(res.Files))}
	for _, f := range res.Files {
		fd := FileDigest{
			Path:    f.NewPath,
			Status:  f.Status,
			Binary:  f.IsBinary,
			Hunks:   len(f.Hunks),
			Added:   f.AddedLines,
			Deleted: f.DeletedLines,
		}
		if fd.Path == "" {
			fd.Path = f.OldPath
		}
		if f.Status == StatusRenamed || f.Status == StatusCopied {
			fd.OldPath = f.OldPath
		}
		seen := map[string]bool{}
		for _, h := range f.Hunks {
			if h.FuncName == "" || seen[h.FuncName] {
				continue
			}
			seen[h.FuncName] = true
			fd.Symbols = append(fd.Symbols, h.FuncName)
		}
		d.Files = append(d.Files, fd)
		d.Stat.Files++
		d.Stat.Hunks += fd.Hunks
		d.Stat.Added += fd.Added
		d.Stat.Deleted += fd.Deleted
	}
	return d
}
