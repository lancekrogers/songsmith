package writer

import (
	"strings"
	"testing"
)

func TestLyricCharsCountsOnlySungLines(t *testing.T) {
	if n := LyricChars("[Verse]\n  Windows down  \n\n[Chorus 2]\nInto the light\n"); n != len("Windows down")+len("Into the light") {
		t.Fatalf("chars = %d", n)
	}
	if n := LyricChars("[Verse]\nnaïve café"); n != 10 {
		t.Fatalf("runes = %d", n)
	}
}

func TestLongerThanClean(t *testing.T) {
	short := "[Verse]\n" + strings.Repeat("la la la la la la\n", 60) // 17 × 60 = 1020
	long := "[Verse]\n" + strings.Repeat("la la la la la la\n", 80)  // 1360
	cases := []struct {
		seconds float64
		lyrics  string
		want    bool
	}{
		{180, short, false}, {181, short, true}, {0, short, false}, {0, long, true}, {120, long, true},
	}
	for _, c := range cases {
		if got := LongerThanClean(c.seconds, c.lyrics); got != c.want {
			t.Errorf("%v s, %d chars: %v", c.seconds, LyricChars(c.lyrics), got)
		}
	}
}

func TestPromptsSizeLyricsToTheLength(t *testing.T) {
	llm := &fakeLLM{reply: goodReply}
	if _, err := New(llm, "m").Write(t.Context(), "idea", Options{Seconds: 120}); err != nil {
		t.Fatal(err)
	}
	if user := llm.got.Messages[1].Content; !strings.Contains(user, "about 780 characters") || !strings.Contains(user, "6.5 characters per second") {
		t.Fatalf("user prompt = %q", user)
	}
	if _, err := New(llm, "m").Write(t.Context(), "idea", Options{}); err != nil {
		t.Fatal(err)
	}
	if user := llm.got.Messages[1].Content; !strings.Contains(user, "3:00 or less") {
		t.Fatalf("no length guidance without a target: %q", user)
	}
	if sys := llm.got.Messages[0].Content; !strings.Contains(sys, "6.5 characters") || !strings.Contains(sys, "3:00 (180 seconds) or less") {
		t.Fatal("built-in instructions lack the length guidance")
	}
	custom := &fakeLLM{reply: goodReply}
	if _, err := New(custom, "m").WithInstructions("My own rules.").Write(t.Context(), "idea", Options{}); err != nil {
		t.Fatal(err)
	}
	if custom.got.Messages[0].Content != "My own rules." {
		t.Fatal("a user's own instructions were changed")
	}
	if !strings.Contains(ReviseRules, "6.5 characters") || !strings.Contains(ReviseRules, "3:00 (180 seconds) or less") {
		t.Fatal("revise rules lack the length guidance")
	}
}
