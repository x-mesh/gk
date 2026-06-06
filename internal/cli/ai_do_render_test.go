package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderDoPlanHeader_Korean_DryRun(t *testing.T) {
	var b bytes.Buffer
	renderDoPlanHeader(&b, "openai", "ko", true, 2)
	got := b.String()
	for _, want := range []string{"실행 계획", "미리보기", "provider: openai", "lang: ko", "명령 2개"} {
		if !strings.Contains(got, want) {
			t.Errorf("ko dry-run header missing %q\n got: %q", want, got)
		}
	}
}

func TestRenderDoPlanHeader_English_NoBadgeWhenLive(t *testing.T) {
	var b bytes.Buffer
	renderDoPlanHeader(&b, "kiro", "en", false, 1)
	got := b.String()
	for _, want := range []string{"Execution plan", "provider: kiro", "lang: en", "1 command(s)"} {
		if !strings.Contains(got, want) {
			t.Errorf("en live header missing %q\n got: %q", want, got)
		}
	}
	if strings.Contains(got, "preview") {
		t.Errorf("live (non-dry-run) header must not show a preview badge\n got: %q", got)
	}
}

func TestRenderDoDryRunFooter(t *testing.T) {
	var ko, en bytes.Buffer
	renderDoDryRunFooter(&ko, "ko")
	renderDoDryRunFooter(&en, "en")
	if !strings.Contains(ko.String(), "실행하려면") {
		t.Errorf("ko footer missing run hint: %q", ko.String())
	}
	if !strings.Contains(en.String(), "to run") {
		t.Errorf("en footer missing run hint: %q", en.String())
	}
}

func TestDoSpinnerMessage(t *testing.T) {
	if got := doSpinnerMessage("ko", "openai"); !strings.Contains(got, "계획 요청") || !strings.Contains(got, "openai") {
		t.Errorf("ko spinner message unexpected: %q", got)
	}
	if got := doSpinnerMessage("en", "kiro"); !strings.Contains(got, "asking") || !strings.Contains(got, "kiro") {
		t.Errorf("en spinner message unexpected: %q", got)
	}
}
