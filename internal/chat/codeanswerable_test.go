package chat

import "testing"

func TestIsCodeAnswerable(t *testing.T) {
	cases := []struct {
		name string
		q    string
		want bool
	}{
		// Code-answerable: carry a code/repo signal → gate should fire.
		{"ko command noun", "원격지 repo가 변경되는걸 확인하는 커맨드가 뭐야?", true},
		{"ko terse command lookup", "원격 변경 확인 커맨드?", true},
		{"ko function deixis", "이 함수 언제 왜 바뀌었지?", true},
		{"ko file question", "이 파일은 어디서 쓰여?", true},
		{"ko implement", "그 기능 어디에 구현돼 있어?", true},
		{"en command", "which command checks the remote?", true},
		{"en function whole word", "what does this function do?", true},
		{"en repo deixis", "how does this repo handle config?", true},
		{"en plural commands", "which commands are available?", true},
		{"en plural functions", "where are these functions defined?", true},
		{"en plural tests", "where are the tests?", true},
		{"en plural options", "which options are available?", true},
		{"en plural errors", "where are these errors handled?", true},

		// Repo-independent: no code/repo signal → gate must stay off.
		{"weather", "오늘 날씨 어때?", false},
		{"math", "what is 2 + 2?", false},
		{"opinion", "리액트랑 뷰 중에 뭐가 더 좋아?", false},
		{"ko location deixis", "여기서 가까운 맛집이 어디야?", false},
		{"ko generic file", "Python에서 파일 여는 법 알려줘", false},
		{"ko generic file check", "Python에서 파일 존재 확인하는 법 알려줘", false},
		{"en generic file", "how do I open a file in Python?", false},
		{"en generic error", "what does HTTP 500 error mean?", false},
		{"greeting", "안녕, 잘 지내?", false},
		{"empty", "", false},
		{"whitespace", "   ", false},

		// Substring accidents must NOT trip English whole-word matching.
		{"latest not test", "what is the latest news today?", false},
		{"terror not error", "tell me about terror movies", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsCodeAnswerable(c.q); got != c.want {
				t.Errorf("IsCodeAnswerable(%q) = %v, want %v", c.q, got, c.want)
			}
		})
	}
}
