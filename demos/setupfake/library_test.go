package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lancekrogers/songsmith/internal/ollama"
	"github.com/lancekrogers/songsmith/internal/writer"
)

type promptOnly struct{ got string }

func (p *promptOnly) Chat(_ context.Context, req ollama.ChatRequest) (string, error) {
	p.got = req.Messages[1].Content
	return "{}", nil
}

func TestTheCannedRevisionKeepsEveryLineAndValidates(t *testing.T) {
	current := writer.Draft{Title: cannedDraft["title"].(string), Lyrics: cannedDraft["lyrics"].(string) + "\n\n[Verse]\nKeep the light on",
		Caption: writer.Caption{GlobalMetadata: "Folk ballad", VocalDetails: "Weathered male lead", Arrangement: "Verse: guitar."}, Seconds: 150}
	p := &promptOnly{}
	_, _ = writer.New(p, "m").Revise(t.Context(), current, "", "longer", writer.Options{})
	data, _ := json.Marshal(cannedRevision(p.got))
	var d writer.Draft
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if d.Title != current.Title || d.Caption.GlobalMetadata != "Folk ballad" || d.Caption.VocalDetails != "Weathered male lead" {
		t.Fatalf("revision %+v", d)
	}
	rest := d.Lyrics
	for _, l := range strings.Split(current.Lyrics, "\n") {
		i := strings.Index(rest, l)
		if i < 0 {
			t.Fatalf("line %q dropped or out of order:\n%s", l, d.Lyrics)
		}
		rest = rest[i+len(l):]
	}
	if !strings.Contains(d.Lyrics, "[Bridge]") || !strings.Contains(d.Lyrics, "[Pre-Chorus]") {
		t.Fatalf("nothing added:\n%s", d.Lyrics)
	}
}
