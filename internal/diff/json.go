package diff

import (
	"encoding/json"
	"io"
)

// ToJSON은 DiffResult를 JSON 출력용 DiffJSON 구조체로 변환한다.
func ToJSON(result *DiffResult) *DiffJSON {
	if result == nil {
		return &DiffJSON{
			Files: []DiffFileJSON{},
			Stat:  DiffStatJSON{},
		}
	}

	totalAdded := 0
	totalDeleted := 0

	files := make([]DiffFileJSON, 0, len(result.Files))
	for _, f := range result.Files {
		totalAdded += f.AddedLines
		totalDeleted += f.DeletedLines
		files = append(files, toFileJSON(f))
	}

	return &DiffJSON{
		Files: files,
		Stat: DiffStatJSON{
			TotalFiles:   len(result.Files),
			TotalAdded:   totalAdded,
			TotalDeleted: totalDeleted,
		},
	}
}

// WriteJSON은 DiffResult를 JSON으로 변환하여 w에 출력한다.
// 가독성을 위해 들여쓰기된 JSON을 출력한다.
func WriteJSON(w io.Writer, result *DiffResult) error {
	dj := ToJSON(result)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(dj)
}

// toFileJSON은 DiffFile을 DiffFileJSON으로 변환한다.
func toFileJSON(f DiffFile) DiffFileJSON {
	hunks := make([]DiffHunkJSON, 0, len(f.Hunks))
	for _, h := range f.Hunks {
		hunks = append(hunks, toHunkJSON(h))
	}

	fj := DiffFileJSON{
		Path:         f.NewPath,
		Status:       f.Status.String(),
		IsBinary:     f.IsBinary,
		AddedLines:   f.AddedLines,
		DeletedLines: f.DeletedLines,
		Hunks:        hunks,
	}

	// rename인 경우 old_path 설정
	if f.Status == StatusRenamed && f.OldPath != f.NewPath {
		fj.OldPath = f.OldPath
	}

	return fj
}

// toHunkJSON은 Hunk를 DiffHunkJSON으로 변환한다.
func toHunkJSON(h Hunk) DiffHunkJSON {
	lines := make([]DiffLineJSON, 0, len(h.Lines))
	for _, l := range h.Lines {
		lines = append(lines, toLineJSON(l))
	}

	return DiffHunkJSON{
		Header: h.Header,
		Lines:  lines,
	}
}

// toLineJSON은 DiffLine을 DiffLineJSON으로 변환한다.
func toLineJSON(l DiffLine) DiffLineJSON {
	return DiffLineJSON{
		Kind:    l.Kind.String(),
		Content: l.Content,
		OldNum:  l.OldNum,
		NewNum:  l.NewNum,
	}
}
