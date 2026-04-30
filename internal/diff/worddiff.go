package diff

import "strings"

// wordDiffMaxLineBytes caps the per-line byte length we'll word-diff.
// Beyond this threshold the line is almost certainly machine-generated
// (minified bundle, vendored lockfile) and the user gains nothing from
// intra-line highlights — meanwhile the LCS DP table is O(m*n) ints.
//
// 4 KB is large enough for normal source-code lines including verbose
// shell pipelines, while keeping the worst-case allocation bounded.
const wordDiffMaxLineBytes = 4 * 1024

// wordDiffMaxCells caps the LCS DP table size as a second safety net
// — even short-but-pathological inputs (lots of tokens) shouldn't be
// able to allocate gigabytes. 1M cells = 8 MB at int64 / 4 MB at int.
const wordDiffMaxCells = 1_000_000

// ComputeWordDiff는 삭제 라인(oldLine)과 추가 라인(newLine) 쌍에서
// 단어 단위 차이를 계산한다. 반환값은 각 라인의 변경된 구간(spans)이다.
// oldSpans는 oldLine 전체를, newSpans는 newLine 전체를 빈틈 없이 커버한다.
//
// 알고리즘: 공백 기준 토큰화 → LCS(최장 공통 부분 수열) → 바이트 오프셋 매핑
//
// 한 줄이 wordDiffMaxLineBytes를 넘거나 LCS DP 테이블 크기가
// wordDiffMaxCells를 초과하면 word-diff를 건너뛰고 라인 전체를
// 변경(Changed: true)으로 표시한다 — 인스턴스 메모리가 폭발하면
// `gk diff`가 OOM으로 중단되므로, 거대한 한 줄(minified JS, 생성
// 코드)에는 intra-line 하이라이트를 포기하는 게 옳다.
func ComputeWordDiff(oldLine, newLine string) (oldSpans, newSpans []DiffSpan) {
	// 빈 문자열 처리
	if oldLine == "" && newLine == "" {
		return nil, nil
	}
	if oldLine == "" {
		return nil, []DiffSpan{{Start: 0, End: len(newLine), Changed: true}}
	}
	if newLine == "" {
		return []DiffSpan{{Start: 0, End: len(oldLine), Changed: true}}, nil
	}

	// 동일한 문자열
	if oldLine == newLine {
		return []DiffSpan{{Start: 0, End: len(oldLine), Changed: false}},
			[]DiffSpan{{Start: 0, End: len(newLine), Changed: false}}
	}

	// Bail-out for pathological inputs — fall back to whole-line change.
	if len(oldLine) > wordDiffMaxLineBytes || len(newLine) > wordDiffMaxLineBytes {
		return []DiffSpan{{Start: 0, End: len(oldLine), Changed: true}},
			[]DiffSpan{{Start: 0, End: len(newLine), Changed: true}}
	}

	// 토큰화 (공백 기준, 공백도 토큰으로 보존)
	oldTokens := tokenize(oldLine)
	newTokens := tokenize(newLine)

	// Second guard: token-count product caps the DP table.
	if (len(oldTokens)+1)*(len(newTokens)+1) > wordDiffMaxCells {
		return []DiffSpan{{Start: 0, End: len(oldLine), Changed: true}},
			[]DiffSpan{{Start: 0, End: len(newLine), Changed: true}}
	}

	// LCS 계산
	lcs := computeLCS(oldTokens, newTokens)

	// LCS 결과를 바이트 오프셋 span으로 변환
	oldSpans = buildSpans(oldLine, oldTokens, lcs, true)
	newSpans = buildSpans(newLine, newTokens, lcs, false)

	return oldSpans, newSpans
}

// token은 토큰화된 단어 또는 공백 구간을 나타낸다.
type token struct {
	text  string
	start int // 원본 문자열에서의 바이트 오프셋
	end   int // 바이트 오프셋 (exclusive)
	isWS  bool
}

// tokenize는 문자열을 공백과 비공백 토큰으로 분리한다.
// 공백도 별도 토큰으로 보존하여 공백 변경을 감지할 수 있다.
func tokenize(s string) []token {
	if s == "" {
		return nil
	}

	var tokens []token
	i := 0
	for i < len(s) {
		if isSpace(s[i]) {
			// 공백 토큰
			j := i + 1
			for j < len(s) && isSpace(s[j]) {
				j++
			}
			tokens = append(tokens, token{text: s[i:j], start: i, end: j, isWS: true})
			i = j
		} else {
			// 단어 토큰
			j := i + 1
			for j < len(s) && !isSpace(s[j]) {
				j++
			}
			tokens = append(tokens, token{text: s[i:j], start: i, end: j, isWS: false})
			i = j
		}
	}
	return tokens
}

// isSpace는 바이트가 공백 문자인지 확인한다.
func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

// lcsMatch는 LCS에서 매칭된 토큰 쌍의 인덱스를 나타낸다.
type lcsMatch struct {
	oldIdx int
	newIdx int
}

// computeLCS는 두 토큰 배열의 최장 공통 부분 수열(LCS)을 계산한다.
// 반환값은 매칭된 (oldIdx, newIdx) 쌍의 배열이다.
func computeLCS(oldTokens, newTokens []token) []lcsMatch {
	m := len(oldTokens)
	n := len(newTokens)

	if m == 0 || n == 0 {
		return nil
	}

	// DP 테이블 구축
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if oldTokens[i-1].text == newTokens[j-1].text {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				if dp[i-1][j] >= dp[i][j-1] {
					dp[i][j] = dp[i-1][j]
				} else {
					dp[i][j] = dp[i][j-1]
				}
			}
		}
	}

	// 역추적으로 LCS 추출
	var matches []lcsMatch
	i, j := m, n
	for i > 0 && j > 0 {
		if oldTokens[i-1].text == newTokens[j-1].text {
			matches = append(matches, lcsMatch{oldIdx: i - 1, newIdx: j - 1})
			i--
			j--
		} else if dp[i-1][j] >= dp[i][j-1] {
			i--
		} else {
			j--
		}
	}

	// 역순으로 추출했으므로 뒤집기
	for left, right := 0, len(matches)-1; left < right; left, right = left+1, right-1 {
		matches[left], matches[right] = matches[right], matches[left]
	}

	return matches
}

// buildSpans는 LCS 매칭 결과를 바이트 오프셋 기반 DiffSpan 배열로 변환한다.
// isOld가 true이면 oldTokens 기준, false이면 newTokens 기준으로 span을 생성한다.
//
// LCS의 매칭 인덱스는 양쪽 모두 단조 증가(역추적 후 reverse 했으므로)
// 이므로, 별도 set 자료구조 없이 두 포인터(token cursor i, lcs cursor
// matchIdx)만으로 O(len(tokens)+len(lcs))에 매칭 여부를 판정한다.
// 이전 구현은 매 호출마다 map[int]bool을 새로 할당했고, 라인 쌍 수에
// 비례해 GC 압박을 만들었다.
func buildSpans(line string, tokens []token, lcs []lcsMatch, isOld bool) []DiffSpan {
	if len(tokens) == 0 {
		if len(line) > 0 {
			return []DiffSpan{{Start: 0, End: len(line), Changed: true}}
		}
		return nil
	}

	spans := make([]DiffSpan, 0, len(tokens))
	matchIdx := 0
	for i, tok := range tokens {
		matched := false
		if isOld {
			for matchIdx < len(lcs) && lcs[matchIdx].oldIdx < i {
				matchIdx++
			}
			if matchIdx < len(lcs) && lcs[matchIdx].oldIdx == i {
				matched = true
			}
		} else {
			for matchIdx < len(lcs) && lcs[matchIdx].newIdx < i {
				matchIdx++
			}
			if matchIdx < len(lcs) && lcs[matchIdx].newIdx == i {
				matched = true
			}
		}
		changed := !matched
		if len(spans) > 0 && spans[len(spans)-1].Changed == changed && spans[len(spans)-1].End == tok.start {
			// 이전 span과 병합
			spans[len(spans)-1].End = tok.end
		} else {
			spans = append(spans, DiffSpan{
				Start:   tok.start,
				End:     tok.end,
				Changed: changed,
			})
		}
	}

	return spans
}

// IsWhitespaceOnlyChange는 두 라인의 차이가 공백 변경만인지 확인한다.
// 중간 공백, 앞뒤 공백 등 모든 공백 차이를 감지한다.
func IsWhitespaceOnlyChange(oldLine, newLine string) bool {
	if oldLine == newLine {
		return false
	}
	return normalizeWhitespace(oldLine) == normalizeWhitespace(newLine)
}

// normalizeWhitespace는 문자열의 모든 연속 공백을 단일 공백으로 정규화하고
// 앞뒤 공백을 제거한다.
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
