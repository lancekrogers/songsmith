package writer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancekrogers/songsmith/internal/ollama"
)

type fakeLLM struct {
	reply string
	err   error
	got   ollama.ChatRequest
}

func (f *fakeLLM) Chat(_ context.Context, req ollama.ChatRequest) (string, error) {
	f.got = req
	return f.reply, f.err
}

const goodReply = `{"title":"Coast Road","lyrics":"[Verse]\nWindows down\n[Chorus]\nInto the light","caption":{"global_metadata":"Indie pop, 104 BPM","vocal_details":"Warm male lead","arrangement":"Verse: acoustic guitar. Chorus: full drums."},"seconds":95}`

func TestWrite(t *testing.T) {
	t.Run("valid draft", func(t *testing.T) {
		llm := &fakeLLM{reply: goodReply}
		d, err := New(llm, "m").Write(t.Context(), "a summer drive", Options{})
		if err != nil {
			t.Fatal(err)
		}
		if d.Title != "Coast Road" || d.Seconds != 95 {
			t.Fatalf("draft = %+v", d)
		}
		if llm.got.Model != "m" || llm.got.Format == nil || *llm.got.Think {
			t.Fatalf("request = %+v", llm.got)
		}
	})

	t.Run("target length overrides the model", func(t *testing.T) {
		llm := &fakeLLM{reply: goodReply}
		d, err := New(llm, "m").Write(t.Context(), "idea", Options{Seconds: 60, Instrumental: true})
		if err != nil {
			t.Fatal(err)
		}
		if d.Seconds != 60 {
			t.Fatalf("seconds = %v", d.Seconds)
		}
		user := llm.got.Messages[1].Content
		if !strings.Contains(user, "about 60 seconds") || !strings.Contains(user, "instrumental") {
			t.Fatalf("user prompt = %q", user)
		}
	})

	t.Run("missing tags is reported", func(t *testing.T) {
		llm := &fakeLLM{reply: strings.Replace(goodReply, `[Verse]\nWindows down\n[Chorus]\n`, ``, 1)}
		_, err := New(llm, "m").Write(t.Context(), "idea", Options{})
		if err == nil || !strings.Contains(err.Error(), "section tag") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		_, err := New(&fakeLLM{reply: "sure! here is a song"}, "m").Write(t.Context(), "idea", Options{})
		if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("llm error is wrapped", func(t *testing.T) {
		boom := errors.New("connection refused")
		_, err := New(&fakeLLM{err: boom}, "m").Write(t.Context(), "idea", Options{})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("empty idea", func(t *testing.T) {
		if _, err := New(&fakeLLM{}, "m").Write(t.Context(), "  ", Options{}); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestWriteSettings(t *testing.T) {
	llm := &fakeLLM{reply: goodReply}
	if _, err := New(llm, "m").Write(t.Context(), "idea", Options{}); err != nil {
		t.Fatal(err)
	}
	if llm.got.Options["temperature"] != DefaultTemperature || llm.got.Messages[0].Content != BuiltinInstructions {
		t.Fatalf("defaults: %+v", llm.got)
	}
	if _, err := New(llm, "m").WithTemperature(1.2).WithInstructions("Write only haiku.").Write(t.Context(), "idea", Options{}); err != nil {
		t.Fatal(err)
	}
	if llm.got.Options["temperature"] != 1.2 || llm.got.Messages[0].Content != "Write only haiku." {
		t.Fatalf("configured: %+v", llm.got)
	}
	if New(llm, "m").WithInstructions("  \n").system != BuiltinInstructions {
		t.Fatal("blank instructions replaced the built-in prompt")
	}
}

func TestInstructions(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "prompt.md")
	empty := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(custom, []byte("Write sea shanties."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path    string
		want    string
		wantErr bool
	}{
		{"", BuiltinInstructions, false},
		{custom, "Write sea shanties.", false},
		{empty, BuiltinInstructions, true},
		{filepath.Join(dir, "missing.md"), BuiltinInstructions, true},
	}
	for _, c := range cases {
		got, err := Instructions(c.path)
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("Instructions(%q) = %.20q, %v", c.path, got, err)
		}
	}
}

func TestCaptionString(t *testing.T) {
	c := Caption{GlobalMetadata: " Pop ", VocalDetails: "Lead", Arrangement: "Verse: guitar"}
	want := "Global Metadata: Pop\n\nVocal Details: Lead\n\nArrangement: Verse: guitar"
	if got := c.String(); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestValidateClampsAndTags(t *testing.T) {
	d := Draft{Lyrics: "[Pre-Chorus]\nla", Caption: Caption{"a", "b", "c"}, Seconds: 400}
	if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("err = %v", err)
	}
	d.Seconds = 120
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestParseCaptionRoundTrip(t *testing.T) {
	c := Caption{GlobalMetadata: "Pop, 120 BPM", VocalDetails: "Female lead", Arrangement: "Intro: synth.\nChorus: drums."}
	if got := ParseCaption(c.String()); got != c {
		t.Fatalf("round trip = %+v", got)
	}
	if got := ParseCaption("just a vibe"); got.GlobalMetadata != "just a vibe" || got.String() != "Global Metadata: just a vibe" {
		t.Fatalf("headerless = %+v", got)
	}
	got := ParseCaption("lo-fi\nvocal details: hushed\nARRANGEMENT: rhodes")
	if got.GlobalMetadata != "lo-fi" || got.VocalDetails != "hushed" || got.Arrangement != "rhodes" {
		t.Fatalf("mixed case = %+v", got)
	}
}
