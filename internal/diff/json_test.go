package diff

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestToJSON_NilResult(t *testing.T) {
	dj := ToJSON(nil)
	if dj == nil {
		t.Fatal("ToJSON(nil)이 nil을 반환함")
		return
	}
	if len(dj.Files) != 0 {
		t.Errorf("nil 입력에 대해 Files가 비어있지 않음: %d", len(dj.Files))
	}
	if dj.Stat.TotalFiles != 0 || dj.Stat.TotalAdded != 0 || dj.Stat.TotalDeleted != 0 {
		t.Errorf("nil 입력에 대해 Stat이 0이 아님: %+v", dj.Stat)
	}
}

func TestToJSON_EmptyResult(t *testing.T) {
	result := &DiffResult{}
	dj := ToJSON(result)
	if len(dj.Files) != 0 {
		t.Errorf("빈 결과에 대해 Files가 비어있지 않음: %d", len(dj.Files))
	}
	if dj.Stat.TotalFiles != 0 {
		t.Errorf("빈 결과에 대해 TotalFiles가 0이 아님: %d", dj.Stat.TotalFiles)
	}
}

func TestToJSON_SingleModifiedFile(t *testing.T) {
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath:      "main.go",
				NewPath:      "main.go",
				Status:       StatusModified,
				AddedLines:   3,
				DeletedLines: 1,
				Hunks: []Hunk{
					{
						OldStart: 10,
						OldCount: 5,
						NewStart: 10,
						NewCount: 7,
						Header:   "@@ -10,5 +10,7 @@",
						Lines: []DiffLine{
							{Kind: LineContext, Content: "func main() {", OldNum: 10, NewNum: 10},
							{Kind: LineDeleted, Content: "\told()", OldNum: 11, NewNum: 0},
							{Kind: LineAdded, Content: "\tnewA()", OldNum: 0, NewNum: 11},
							{Kind: LineAdded, Content: "\tnewB()", OldNum: 0, NewNum: 12},
							{Kind: LineAdded, Content: "\tnewC()", OldNum: 0, NewNum: 13},
							{Kind: LineContext, Content: "}", OldNum: 12, NewNum: 14},
						},
					},
				},
			},
		},
	}

	dj := ToJSON(result)

	// 파일 수 검증
	if len(dj.Files) != 1 {
		t.Fatalf("파일 수 불일치: 기대 1, 실제 %d", len(dj.Files))
	}

	f := dj.Files[0]
	if f.Path != "main.go" {
		t.Errorf("Path 불일치: %q", f.Path)
	}
	if f.OldPath != "" {
		t.Errorf("Modified 파일에 OldPath가 설정됨: %q", f.OldPath)
	}
	if f.Status != "modified" {
		t.Errorf("Status 불일치: %q", f.Status)
	}
	if f.IsBinary {
		t.Error("IsBinary가 true")
	}
	if f.AddedLines != 3 {
		t.Errorf("AddedLines 불일치: %d", f.AddedLines)
	}
	if f.DeletedLines != 1 {
		t.Errorf("DeletedLines 불일치: %d", f.DeletedLines)
	}

	// Hunk 검증
	if len(f.Hunks) != 1 {
		t.Fatalf("Hunk 수 불일치: 기대 1, 실제 %d", len(f.Hunks))
	}
	h := f.Hunks[0]
	if h.Header != "@@ -10,5 +10,7 @@" {
		t.Errorf("Header 불일치: %q", h.Header)
	}
	if len(h.Lines) != 6 {
		t.Fatalf("라인 수 불일치: 기대 6, 실제 %d", len(h.Lines))
	}

	// 라인 종류 검증
	expectedKinds := []string{"context", "deleted", "added", "added", "added", "context"}
	for i, l := range h.Lines {
		if l.Kind != expectedKinds[i] {
			t.Errorf("라인 %d Kind 불일치: 기대 %q, 실제 %q", i, expectedKinds[i], l.Kind)
		}
	}

	// Stat 검증
	if dj.Stat.TotalFiles != 1 {
		t.Errorf("TotalFiles 불일치: %d", dj.Stat.TotalFiles)
	}
	if dj.Stat.TotalAdded != 3 {
		t.Errorf("TotalAdded 불일치: %d", dj.Stat.TotalAdded)
	}
	if dj.Stat.TotalDeleted != 1 {
		t.Errorf("TotalDeleted 불일치: %d", dj.Stat.TotalDeleted)
	}
}

func TestToJSON_RenamedFile(t *testing.T) {
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath:      "old_name.go",
				NewPath:      "new_name.go",
				Status:       StatusRenamed,
				AddedLines:   0,
				DeletedLines: 0,
			},
		},
	}

	dj := ToJSON(result)
	f := dj.Files[0]

	if f.Path != "new_name.go" {
		t.Errorf("Path 불일치: %q", f.Path)
	}
	if f.OldPath != "old_name.go" {
		t.Errorf("OldPath 불일치: 기대 %q, 실제 %q", "old_name.go", f.OldPath)
	}
	if f.Status != "renamed" {
		t.Errorf("Status 불일치: %q", f.Status)
	}
}

func TestToJSON_BinaryFile(t *testing.T) {
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath:  "image.png",
				NewPath:  "image.png",
				Status:   StatusModified,
				IsBinary: true,
			},
		},
	}

	dj := ToJSON(result)
	f := dj.Files[0]

	if !f.IsBinary {
		t.Error("IsBinary가 false")
	}
	if len(f.Hunks) != 0 {
		t.Errorf("바이너리 파일에 Hunk가 존재: %d", len(f.Hunks))
	}
}

func TestToJSON_MultipleFiles_StatTotals(t *testing.T) {
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath:      "a.go",
				NewPath:      "a.go",
				Status:       StatusModified,
				AddedLines:   5,
				DeletedLines: 2,
			},
			{
				OldPath:      "b.go",
				NewPath:      "b.go",
				Status:       StatusAdded,
				AddedLines:   10,
				DeletedLines: 0,
			},
			{
				OldPath:      "c.go",
				NewPath:      "c.go",
				Status:       StatusDeleted,
				AddedLines:   0,
				DeletedLines: 8,
			},
		},
	}

	dj := ToJSON(result)

	if dj.Stat.TotalFiles != 3 {
		t.Errorf("TotalFiles 불일치: %d", dj.Stat.TotalFiles)
	}
	if dj.Stat.TotalAdded != 15 {
		t.Errorf("TotalAdded 불일치: 기대 15, 실제 %d", dj.Stat.TotalAdded)
	}
	if dj.Stat.TotalDeleted != 10 {
		t.Errorf("TotalDeleted 불일치: 기대 10, 실제 %d", dj.Stat.TotalDeleted)
	}
}

func TestWriteJSON_ValidJSON(t *testing.T) {
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath:      "test.go",
				NewPath:      "test.go",
				Status:       StatusModified,
				AddedLines:   2,
				DeletedLines: 1,
				Hunks: []Hunk{
					{
						OldStart: 1,
						OldCount: 3,
						NewStart: 1,
						NewCount: 4,
						Header:   "@@ -1,3 +1,4 @@",
						Lines: []DiffLine{
							{Kind: LineContext, Content: "package main", OldNum: 1, NewNum: 1},
							{Kind: LineDeleted, Content: "var x = 1", OldNum: 2, NewNum: 0},
							{Kind: LineAdded, Content: "var x = 2", OldNum: 0, NewNum: 2},
							{Kind: LineAdded, Content: "var y = 3", OldNum: 0, NewNum: 3},
						},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	err := WriteJSON(&buf, result)
	if err != nil {
		t.Fatalf("WriteJSON 실패: %v", err)
	}

	// 유효한 JSON인지 검증
	var parsed DiffJSON
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("JSON 파싱 실패: %v\n출력: %s", err, buf.String())
	}

	// 필수 필드 존재 검증
	if len(parsed.Files) != 1 {
		t.Fatalf("파일 수 불일치: %d", len(parsed.Files))
	}
	if parsed.Files[0].Path != "test.go" {
		t.Errorf("Path 불일치: %q", parsed.Files[0].Path)
	}
	if parsed.Stat.TotalAdded != 2 {
		t.Errorf("TotalAdded 불일치: %d", parsed.Stat.TotalAdded)
	}
}

func TestWriteJSON_NilResult(t *testing.T) {
	var buf bytes.Buffer
	err := WriteJSON(&buf, nil)
	if err != nil {
		t.Fatalf("WriteJSON(nil) 실패: %v", err)
	}

	var parsed DiffJSON
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("nil 결과의 JSON 파싱 실패: %v", err)
	}
	if len(parsed.Files) != 0 {
		t.Errorf("nil 결과에 파일이 존재: %d", len(parsed.Files))
	}
}

func TestWriteJSON_PrettyPrinted(t *testing.T) {
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath: "x.go",
				NewPath: "x.go",
				Status:  StatusModified,
			},
		},
	}

	var buf bytes.Buffer
	err := WriteJSON(&buf, result)
	if err != nil {
		t.Fatalf("WriteJSON 실패: %v", err)
	}

	output := buf.String()
	// 들여쓰기가 적용되었는지 확인 (2칸 스페이스)
	if !bytes.Contains(buf.Bytes(), []byte("  \"files\"")) {
		t.Errorf("JSON이 들여쓰기되지 않음:\n%s", output)
	}
}

func TestToJSON_LineNumbers_OmitEmpty(t *testing.T) {
	result := &DiffResult{
		Files: []DiffFile{
			{
				OldPath: "f.go",
				NewPath: "f.go",
				Status:  StatusModified,
				Hunks: []Hunk{
					{
						Header: "@@ -1,1 +1,1 @@",
						Lines: []DiffLine{
							{Kind: LineAdded, Content: "new line", OldNum: 0, NewNum: 1},
							{Kind: LineDeleted, Content: "old line", OldNum: 1, NewNum: 0},
						},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	err := WriteJSON(&buf, result)
	if err != nil {
		t.Fatalf("WriteJSON 실패: %v", err)
	}

	// JSON에서 old_num=0인 경우 omitempty로 생략되는지 확인
	var raw map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("JSON 파싱 실패: %v", err)
	}

	files := raw["files"].([]interface{})
	hunks := files[0].(map[string]interface{})["hunks"].([]interface{})
	lines := hunks[0].(map[string]interface{})["lines"].([]interface{})

	// Added 라인: old_num이 0이므로 omitempty에 의해 생략
	addedLine := lines[0].(map[string]interface{})
	if _, exists := addedLine["old_num"]; exists {
		t.Error("Added 라인에 old_num이 존재함 (omitempty로 생략되어야 함)")
	}

	// Deleted 라인: new_num이 0이므로 omitempty에 의해 생략
	deletedLine := lines[1].(map[string]interface{})
	if _, exists := deletedLine["new_num"]; exists {
		t.Error("Deleted 라인에 new_num이 존재함 (omitempty로 생략되어야 함)")
	}
}
