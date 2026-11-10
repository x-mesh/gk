package gitleaksrules

// keywordMatcher finds which keywords occur in a text in one pass
// (Aho-Corasick). The prefilter used to call strings.Contains once per
// keyword per hunk, which was about a third of the scan time: ~290 keywords
// against every -U0 hunk of the history.
type keywordMatcher struct {
	// next is the goto function with failure transitions folded in, so a
	// scan is one table lookup per byte.
	next [][256]int32
	// out lists the keyword ids that end at a state, including those
	// reached through failure links.
	out   [][]int32
	words int
}

// newKeywordMatcher builds the automaton. Keywords are matched byte-wise,
// so callers pass them in the same case as the text they scan.
func newKeywordMatcher(words []string) *keywordMatcher {
	m := &keywordMatcher{next: make([][256]int32, 1), out: make([][]int32, 1), words: len(words)}
	for id, w := range words {
		s := int32(0)
		for i := 0; i < len(w); i++ {
			c := w[i]
			if m.next[s][c] == 0 {
				m.next = append(m.next, [256]int32{})
				m.out = append(m.out, nil)
				m.next[s][c] = int32(len(m.next) - 1)
			}
			s = m.next[s][c]
		}
		m.out[s] = append(m.out[s], int32(id))
	}

	fail := make([]int32, len(m.next))
	var queue []int32
	for c := range 256 {
		if s := m.next[0][c]; s != 0 {
			queue = append(queue, s)
		}
	}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		m.out[s] = append(m.out[s], m.out[fail[s]]...)
		for c := range 256 {
			t := m.next[s][c]
			if t == 0 {
				m.next[s][c] = m.next[fail[s]][c]
				continue
			}
			fail[t] = m.next[fail[s]][c]
			queue = append(queue, t)
		}
	}
	return m
}

// keywordSet is a bitset of keyword ids.
type keywordSet []uint64

func (m *keywordMatcher) newSet() keywordSet { return make(keywordSet, (m.words+63)/64) }

func (k keywordSet) has(id int32) bool { return k[id/64]&(1<<(id%64)) != 0 }

// find records in seen every keyword that occurs in text.
func (m *keywordMatcher) find(text string, seen keywordSet) {
	s := int32(0)
	for i := 0; i < len(text); i++ {
		s = m.next[s][text[i]]
		for _, id := range m.out[s] {
			seen[id/64] |= 1 << (id % 64)
		}
	}
}
