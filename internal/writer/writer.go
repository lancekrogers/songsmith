// Package writer drafts song lyrics and a MiniMax Music 3 style caption from
// a short idea using a local LLM.
package writer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/lancekrogers/songsmith/internal/ollama"
)

// Caption is the three-part structured description MiniMax Music 3 expects.
type Caption struct {
	GlobalMetadata string `json:"global_metadata" yaml:"global_metadata"`
	VocalDetails   string `json:"vocal_details" yaml:"vocal_details"`
	Arrangement    string `json:"arrangement" yaml:"arrangement"`
}

// String renders the caption in the layout used by the official examples.
func (c Caption) String() string {
	return "Global Metadata: " + strings.TrimSpace(c.GlobalMetadata) +
		"\n\nVocal Details: " + strings.TrimSpace(c.VocalDetails) +
		"\n\nArrangement: " + strings.TrimSpace(c.Arrangement)
}

type Draft struct {
	Title   string  `json:"title" yaml:"title"`
	Lyrics  string  `json:"lyrics" yaml:"lyrics"`
	Caption Caption `json:"caption" yaml:"caption"`
	Seconds float64 `json:"seconds" yaml:"seconds"`
}

const (
	MinSeconds = 10
	MaxSeconds = 300
)

var sectionTag = regexp.MustCompile(`(?m)^\s*\[(Intro|Verse|Pre-Chorus|Chorus|Post-Chorus|Bridge|Instrumental|Solo|Outro|Hook|Break|Interlude)[^\]]*\]\s*$`)

// Validate reports the first problem that would make a render fail or ignore
// the user's structure.
func (d Draft) Validate() error {
	var errs []error
	if strings.TrimSpace(d.Lyrics) == "" {
		errs = append(errs, errors.New("lyrics are empty"))
	} else if !sectionTag.MatchString(d.Lyrics) {
		errs = append(errs, errors.New("lyrics need at least one section tag such as [Verse] or [Chorus] on its own line"))
	}
	if strings.TrimSpace(d.Caption.GlobalMetadata) == "" {
		errs = append(errs, errors.New("caption global metadata is empty"))
	}
	if strings.TrimSpace(d.Caption.VocalDetails) == "" {
		errs = append(errs, errors.New("caption vocal details are empty"))
	}
	if strings.TrimSpace(d.Caption.Arrangement) == "" {
		errs = append(errs, errors.New("caption arrangement is empty"))
	}
	if d.Seconds < MinSeconds || d.Seconds > MaxSeconds {
		errs = append(errs, fmt.Errorf("length %.0fs is outside %d-%d seconds", d.Seconds, MinSeconds, MaxSeconds))
	}
	return errors.Join(errs...)
}

type Chatter interface {
	Chat(ctx context.Context, req ollama.ChatRequest) (string, error)
}

type Writer struct {
	llm   Chatter
	model string
}

func New(llm Chatter, model string) *Writer {
	return &Writer{llm: llm, model: model}
}

type Options struct {
	// Seconds is the target length; zero lets the model choose.
	Seconds float64
	// Instrumental asks for a song without vocals.
	Instrumental bool
}

var draftSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"title":  map[string]any{"type": "string"},
		"lyrics": map[string]any{"type": "string"},
		"caption": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"global_metadata": map[string]any{"type": "string"},
				"vocal_details":   map[string]any{"type": "string"},
				"arrangement":     map[string]any{"type": "string"},
			},
			"required": []string{"global_metadata", "vocal_details", "arrangement"},
		},
		"seconds": map[string]any{"type": "number"},
	},
	"required": []string{"title", "lyrics", "caption", "seconds"},
}

const systemPrompt = `You write songs for MiniMax Music 3, a model that sings the lyrics it is given and follows a structured style caption.

Return JSON with:
- title: a short song title.
- lyrics: the full lyrics. Put each section tag alone on its own line, using only these tags: [Intro], [Verse], [Pre-Chorus], [Chorus], [Post-Chorus], [Bridge], [Instrumental], [Solo], [Outro]. Write singable lines with natural rhythm and rhyme. Tags carry the structure; never write stage directions inside lyric lines.
- caption.global_metadata: genre and subgenres, tempo (BPM or a range), key and scale only when useful, the emotional progression across the song, listening scenario, and production profile.
- caption.vocal_details: lead vocal gender, timbre, register and delivery, plus harmonies, backing vocals and restrained effects. For an instrumental, say it is instrumental and name the lead instrument.
- caption.arrangement: a section-by-section timeline matching the lyric tags. For each section, state what enters, exits or intensifies: drums and groove, bass, harmonic instruments, leads, textures and transitions. Prefer concrete musical changes over decorative adjectives.
- seconds: the song length in seconds that fits the lyrics, between 10 and 300.

Keep the three caption parts together around 250-450 words. Never copy lyric lines into the caption. Write in English unless the idea asks for another language.`

// Write drafts a song from idea.
func (w *Writer) Write(ctx context.Context, idea string, opts Options) (Draft, error) {
	idea = strings.TrimSpace(idea)
	if idea == "" {
		return Draft{}, errors.New("song idea is empty")
	}
	var user strings.Builder
	user.WriteString("Song idea: " + idea)
	if opts.Seconds > 0 {
		fmt.Fprintf(&user, "\nTarget length: about %.0f seconds; size the lyrics to fit.", opts.Seconds)
	}
	if opts.Instrumental {
		user.WriteString("\nThis is an instrumental: use only [Intro], [Instrumental], [Solo] and [Outro] tags with no sung lines.")
	}
	think := false
	reply, err := w.llm.Chat(ctx, ollama.ChatRequest{
		Model: w.model,
		Messages: []ollama.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: user.String()},
		},
		Format:  draftSchema,
		Think:   &think,
		Options: map[string]any{"temperature": 0.8},
	})
	if err != nil {
		return Draft{}, fmt.Errorf("draft song with %s: %w", w.model, err)
	}
	var d Draft
	if err := json.Unmarshal([]byte(reply), &d); err != nil {
		return Draft{}, fmt.Errorf("model %s returned invalid JSON: %w", w.model, err)
	}
	d.Lyrics = strings.TrimSpace(d.Lyrics)
	if opts.Seconds > 0 {
		d.Seconds = opts.Seconds
	}
	d.Seconds = min(max(d.Seconds, MinSeconds), MaxSeconds)
	if err := d.Validate(); err != nil {
		return d, fmt.Errorf("draft from %s is incomplete: %w", w.model, err)
	}
	return d, nil
}
