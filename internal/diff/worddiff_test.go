package diff

import (
	"testing"
)

func TestComputeWordDiff_IdenticalStrings(t *testing.T) {
	oldSpans, newSpans := ComputeWordDiff("hello world", "hello world")

	if len(oldSpans) != 1 {
		t.Fatalf("동일 문자열: oldSpans 길이 %d, 기대값 1", len(oldSpans))
	}
	if oldSpans[0].Changed {
		t.Error("동일 문자열: oldSpans[0].Changed가 true")
	}
	if oldSpans[0].Start != 0 || oldSpans[0].End != 11 {
		t.Errorf("동일 문자열: oldSpans[0] = {%d, %d}, 기대값 {0, 11}", oldSpans[0].Start, oldSpans[0].End)
	}

	if len(newSpans) != 1 {
		t.Fatalf("동일 문자열: newSpans 길이 %d, 기대값 1", len(newSpans))
	}
	if newSpans[0].Changed {
		t.Error("동일 문자열: newSpans[0].Changed가 true")
	}
}

func TestComputeWordDiff_CompletelyDifferent(t *testing.T) {
	oldSpans, newSpans := ComputeWordDiff("foo bar", "baz qux")

	// 전체 커버리지 확인
	assertSpansCover(t, oldSpans, "foo bar", "old")
	assertSpansCover(t, newSpans, "baz qux", "new")

	// 단어 "foo", "bar"는 변경되어야 함
	oldLine := "foo bar"
	for _, s := range oldSpans {
		text := oldLine[s.Start:s.End]
		if text == "foo" && !s.Changed {
			t.Error("완전히 다른 문자열: 'foo'가 unchanged로 표시됨")
		}
		if text == "bar" && !s.Changed {
			t.Error("완전히 다른 문자열: 'bar'가 unchanged로 표시됨")
		}
	}

	newLine := "baz qux"
	for _, s := range newSpans {
		text := newLine[s.Start:s.End]
		if text == "baz" && !s.Changed {
			t.Error("완전히 다른 문자열: 'baz'가 unchanged로 표시됨")
		}
		if text == "qux" && !s.Changed {
			t.Error("완전히 다른 문자열: 'qux'가 unchanged로 표시됨")
		}
	}
}

func TestComputeWordDiff_SingleWordChange(t *testing.T) {
	oldSpans, newSpans := ComputeWordDiff("hello world", "hello earth")

	assertSpansCover(t, oldSpans, "hello world", "old")
	assertSpansCover(t, newSpans, "hello earth", "new")

	// "hello"는 변경되지 않아야 함
	foundUnchanged := false
	for _, s := range oldSpans {
		if !s.Changed && "hello world"[s.Start:s.End] == "hello" {
			foundUnchanged = true
			break
		}
		// "hello "도 허용 (공백 포함)
		if !s.Changed {
			text := "hello world"[s.Start:s.End]
			if text == "hello" || text == "hello " {
				foundUnchanged = true
				break
			}
		}
	}
	if !foundUnchanged {
		t.Error("단일 단어 변경: 'hello'가 unchanged로 표시되지 않음")
	}

	// "world"는 변경되어야 함
	foundChanged := false
	for _, s := range oldSpans {
		if s.Changed {
			text := "hello world"[s.Start:s.End]
			if text == "world" {
				foundChanged = true
				break
			}
		}
	}
	if !foundChanged {
		t.Error("단일 단어 변경: 'world'가 changed로 표시되지 않음")
	}
}

func TestComputeWordDiff_WhitespaceOnlyChanges(t *testing.T) {
	oldSpans, newSpans := ComputeWordDiff("hello  world", "hello world")

	assertSpansCover(t, oldSpans, "hello  world", "old")
	assertSpansCover(t, newSpans, "hello world", "new")

	// IsWhitespaceOnlyChange 확인
	if !IsWhitespaceOnlyChange("hello  world", "hello world") {
		t.Error("공백 전용 변경이 감지되지 않음")
	}
}

func TestComputeWordDiff_EmptyStrings(t *testing.T) {
	// 둘 다 빈 문자열
	oldSpans, newSpans := ComputeWordDiff("", "")
	if len(oldSpans) != 0 || len(newSpans) != 0 {
		t.Errorf("빈 문자열 쌍: oldSpans=%d, newSpans=%d", len(oldSpans), len(newSpans))
	}

	// old만 빈 문자열
	oldSpans, newSpans = ComputeWordDiff("", "hello")
	if len(oldSpans) != 0 {
		t.Errorf("old 빈 문자열: oldSpans 길이 %d, 기대값 0", len(oldSpans))
	}
	if len(newSpans) != 1 || !newSpans[0].Changed {
		t.Error("old 빈 문자열: newSpans가 전체 Changed가 아님")
	}
	assertSpansCover(t, newSpans, "hello", "new")

	// new만 빈 문자열
	oldSpans, newSpans = ComputeWordDiff("hello", "")
	if len(newSpans) != 0 {
		t.Errorf("new 빈 문자열: newSpans 길이 %d, 기대값 0", len(newSpans))
	}
	if len(oldSpans) != 1 || !oldSpans[0].Changed {
		t.Error("new 빈 문자열: oldSpans가 전체 Changed가 아님")
	}
	assertSpansCover(t, oldSpans, "hello", "old")
}

func TestComputeWordDiff_MultiWordChanges(t *testing.T) {
	oldSpans, newSpans := ComputeWordDiff(
		"the quick brown fox jumps",
		"the slow brown cat jumps",
	)

	assertSpansCover(t, oldSpans, "the quick brown fox jumps", "old")
	assertSpansCover(t, newSpans, "the slow brown cat jumps", "new")

	// "the", "brown", "jumps"는 변경되지 않아야 함
	oldLine := "the quick brown fox jumps"
	unchangedWords := map[string]bool{"the": false, "brown": false, "jumps": false}
	for _, s := range oldSpans {
		if !s.Changed {
			text := oldLine[s.Start:s.End]
			for w := range unchangedWords {
				if text == w || text == w+" " || text == " "+w || text == " "+w+" " {
					unchangedWords[w] = true
				}
			}
		}
	}
	for w, found := range unchangedWords {
		if !found {
			t.Errorf("다중 단어 변경: '%s'가 unchanged로 표시되지 않음", w)
		}
	}
}

func TestIsWhitespaceOnlyChange(t *testing.T) {
	tests := []struct {
		name     string
		oldLine  string
		newLine  string
		expected bool
	}{
		{"공백 추가", "hello world", "hello  world", true},
		{"탭 변경", "hello\tworld", "hello world", true},
		{"내용 변경", "hello world", "hello earth", false},
		{"동일", "hello", "hello", false},
		{"빈 문자열", "", "", false},
		{"앞뒤 공백", "  hello  ", "hello", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsWhitespaceOnlyChange(tt.oldLine, tt.newLine)
			if result != tt.expected {
				t.Errorf("IsWhitespaceOnlyChange(%q, %q) = %v, 기대값 %v",
					tt.oldLine, tt.newLine, result, tt.expected)
			}
		})
	}
}

// assertSpansCover는 span들이 문자열 전체를 빈틈 없이 커버하는지 검증한다.
func assertSpansCover(t *testing.T, spans []DiffSpan, line string, label string) {
	t.Helper()

	if len(line) == 0 {
		if len(spans) != 0 {
			t.Errorf("%s: 빈 문자열에 span이 있음: %d개", label, len(spans))
		}
		return
	}

	if len(spans) == 0 {
		t.Fatalf("%s: 비어있지 않은 문자열에 span이 없음", label)
	}

	// 첫 span은 0에서 시작
	if spans[0].Start != 0 {
		t.Errorf("%s: 첫 span 시작이 0이 아님: %d", label, spans[0].Start)
	}

	// 마지막 span은 문자열 끝까지
	if spans[len(spans)-1].End != len(line) {
		t.Errorf("%s: 마지막 span 끝이 문자열 길이(%d)와 다름: %d",
			label, len(line), spans[len(spans)-1].End)
	}

	// 연속성 검증 (겹침 없음, 빈틈 없음)
	for i := 1; i < len(spans); i++ {
		if spans[i].Start != spans[i-1].End {
			t.Errorf("%s: span[%d].End(%d) != span[%d].Start(%d) — 빈틈 또는 겹침",
				label, i-1, spans[i-1].End, i, spans[i].Start)
		}
	}
}

// TestComputeWordDiff_OversizedLineFallsBack verifies the LCS-cap
// safety net: when either line exceeds wordDiffMaxLineBytes the
// function must return whole-line Changed spans without allocating
// the O(m*n) DP table that would otherwise OOM the process.
func TestComputeWordDiff_OversizedLineFallsBack(t *testing.T) {
	// Build a single-line "minified" payload an order of magnitude
	// larger than the cap. Two distinct strings so they're considered
	// changed; without the cap this would allocate ~O(n²) ints and
	// run for seconds. With the cap, it returns instantly.
	long1 := makeRepeatString("a ", wordDiffMaxLineBytes)
	long2 := makeRepeatString("b ", wordDiffMaxLineBytes)

	oldSpans, newSpans := ComputeWordDiff(long1, long2)

	if len(oldSpans) != 1 || !oldSpans[0].Changed || oldSpans[0].Start != 0 || oldSpans[0].End != len(long1) {
		t.Errorf("oldSpans must be single whole-line Changed span, got %+v", oldSpans)
	}
	if len(newSpans) != 1 || !newSpans[0].Changed || newSpans[0].Start != 0 || newSpans[0].End != len(long2) {
		t.Errorf("newSpans must be single whole-line Changed span, got %+v", newSpans)
	}
}

// TestComputeWordDiff_ManyTokensCellCap verifies the secondary guard:
// even when each line is under the byte cap, a high token count that
// would explode (m+1)*(n+1) cells must trigger the same fallback.
func TestComputeWordDiff_ManyTokensCellCap(t *testing.T) {
	// Both lines are under wordDiffMaxLineBytes individually, but
	// produce enough tokens that the DP table exceeds wordDiffMaxCells.
	// 2000 tokens × 2000 tokens = 4M cells > 1M cap.
	a := makeRepeatString("x ", 4000) // ~2000 tokens, ~4KB
	b := makeRepeatString("y ", 4000)

	if len(a) > wordDiffMaxLineBytes {
		t.Skip("byte cap fires before cell cap; this test would not exercise the cell guard")
	}

	oldSpans, _ := ComputeWordDiff(a, b)
	if len(oldSpans) != 1 || !oldSpans[0].Changed {
		t.Errorf("expected whole-line Changed via cell-count guard, got %+v", oldSpans)
	}
}

// TestComputeWordDiff_MultiByteContent guards against rune-boundary
// regressions. Span offsets are byte-based; tokenize must split on
// ASCII whitespace only and never bisect a multibyte rune. We verify
// every span boundary lands on a valid UTF-8 start byte.
func TestComputeWordDiff_MultiByteContent(t *testing.T) {
	cases := []struct {
		oldLine, newLine string
	}{
		{"안녕 세계", "안녕 우주"},
		{"hello 🎉", "hello 🎊"},
		{"한글 영문 mixed", "한글 영문 changed"},
	}
	for _, c := range cases {
		oldSpans, newSpans := ComputeWordDiff(c.oldLine, c.newLine)
		assertRuneBoundaries(t, c.oldLine, oldSpans, "old")
		assertRuneBoundaries(t, c.newLine, newSpans, "new")
	}
}

func assertRuneBoundaries(t *testing.T, line string, spans []DiffSpan, label string) {
	t.Helper()
	for i, s := range spans {
		if s.Start > 0 && s.Start < len(line) {
			b := line[s.Start]
			// UTF-8 continuation bytes have top two bits "10".
			if b >= 0x80 && b < 0xc0 {
				t.Errorf("%s span[%d].Start=%d is a UTF-8 continuation byte (%q)",
					label, i, s.Start, line[s.Start:min(s.Start+3, len(line))])
			}
		}
		if s.End > 0 && s.End < len(line) {
			b := line[s.End]
			if b >= 0x80 && b < 0xc0 {
				t.Errorf("%s span[%d].End=%d is a UTF-8 continuation byte (%q)",
					label, i, s.End, line[s.End:min(s.End+3, len(line))])
			}
		}
	}
}

func makeRepeatString(s string, totalBytes int) string {
	if totalBytes <= 0 {
		return ""
	}
	n := totalBytes/len(s) + 1
	out := make([]byte, 0, n*len(s))
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out[:totalBytes])
}
