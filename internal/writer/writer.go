// Package writer drafts song lyrics and a MiniMax Music 3 style caption from
// a short idea using a local LLM.
package writer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/lancekrogers/songsmith/internal/ollama"
)

// Caption is the three-part structured description MiniMax Music 3 expects.
type Caption struct {
	GlobalMetadata string `json:"global_metadata" yaml:"global_metadata"`
	VocalDetails   string `json:"vocal_details" yaml:"vocal_details"`
	Arrangement    string `json:"arrangement" yaml:"arrangement"`
}

var captionHeader = regexp.MustCompile(`(?mi)^\s*(Global Metadata|Vocal Details|Arrangement)\s*:`)

// String renders the caption in the layout used by the official examples,
// skipping empty parts.
func (c Caption) String() string {
	var parts []string
	for _, p := range [...]struct{ name, text string }{
		{"Global Metadata", c.GlobalMetadata},
		{"Vocal Details", c.VocalDetails},
		{"Arrangement", c.Arrangement},
	} {
		if t := strings.TrimSpace(p.text); t != "" {
			parts = append(parts, p.name+": "+t)
		}
	}
	return strings.Join(parts, "\n\n")
}

// ParseCaption splits caption text on its "Global Metadata:", "Vocal
// Details:" and "Arrangement:" headers. Text before the first header, or
// text with no headers, becomes global metadata.
func ParseCaption(text string) Caption {
	var c Caption
	locs := captionHeader.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		c.GlobalMetadata = strings.TrimSpace(text)
		return c
	}
	if pre := strings.TrimSpace(text[:locs[0][0]]); pre != "" {
		c.GlobalMetadata = pre
	}
	for i, loc := range locs {
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		body := strings.TrimSpace(text[loc[1]:end])
		switch strings.ToLower(text[loc[2]:loc[3]]) {
		case "global metadata":
			c.GlobalMetadata = strings.TrimSpace(c.GlobalMetadata + "\n" + body)
		case "vocal details":
			c.VocalDetails = body
		case "arrangement":
			c.Arrangement = body
		}
	}
	return c
}

type Draft struct {
	Title   string  `json:"title" yaml:"title"`
	Lyrics  string  `json:"lyrics" yaml:"lyrics"`
	Caption Caption `json:"caption" yaml:"caption"`
	Seconds float64 `json:"seconds" yaml:"seconds"`
	// Instrumental drafts have section tags but no sung lines.
	Instrumental bool `json:"instrumental,omitempty" yaml:"instrumental,omitempty"`
}

const (
	MinSeconds = 10
	MaxSeconds = 300
)

// SectionTags are the lyric section tags the writer is asked to use and the
// editor offers, in song order.
var SectionTags = []string{"Intro", "Verse", "Pre-Chorus", "Chorus", "Post-Chorus", "Bridge", "Instrumental", "Solo", "Outro"}

// AcceptedSectionTags are the tags Validate counts: SectionTags and a few
// more that are not suggested.
var AcceptedSectionTags = append(slices.Clone(SectionTags), "Hook", "Break", "Interlude")

var (
	sectionTag     = regexp.MustCompile(`(?m)^\s*\[(` + tagAlternation() + `)[^\]]*\]\s*$`)
	sectionTagName = regexp.MustCompile(`^\[(` + tagAlternation() + `)[^\]]*\]$`)
	tagLine        = regexp.MustCompile(`^\s*(\[[^\]]*\])\s*$`)
)

func tagAlternation() string {
	var names []string
	for _, t := range AcceptedSectionTags {
		names = append(names, regexp.QuoteMeta(t))
	}
	return strings.Join(names, "|")
}

// TagLine reports whether line holds nothing but a [tag], and the byte span
// of the tag. section is the tag Validate counts it as, such as "Verse" for
// "[Verse 2]", or "" when it would not count.
func TagLine(line string) (start, end int, section string, ok bool) {
	loc := tagLine.FindStringSubmatchIndex(line)
	if loc == nil {
		return 0, 0, "", false
	}
	start, end = loc[2], loc[3]
	if m := sectionTagName.FindStringSubmatch(line[start:end]); m != nil {
		section = m[1]
	}
	return start, end, section, true
}

// IsSectionTag reports whether name is a tag Validate counts, such as "Verse".
func IsSectionTag(name string) bool {
	return slices.Contains(AcceptedSectionTags, name)
}

// Fields a FieldError can name.
const (
	FieldLyrics         = "lyrics"
	FieldGlobalMetadata = "caption.global_metadata"
	FieldVocalDetails   = "caption.vocal_details"
	FieldArrangement    = "caption.arrangement"
	FieldSeconds        = "seconds"
)

// FieldError is one Validate problem and the draft field it concerns.
type FieldError struct {
	Field string
	Msg   string
}

func (e *FieldError) Error() string { return e.Msg }

// Validate reports every problem that would make a render fail or ignore the
// user's structure, as FieldErrors in field order.
func (d Draft) Validate() error {
	var errs []error
	problem := func(field, msg string) { errs = append(errs, &FieldError{Field: field, Msg: msg}) }
	switch {
	case d.Instrumental && !sectionTag.MatchString(d.Lyrics):
		problem(FieldLyrics, "an instrumental still needs section tags such as [Intro], [Instrumental] and [Outro], each on its own line")
	case strings.TrimSpace(d.Lyrics) == "":
		problem(FieldLyrics, "lyrics are empty")
	case !sectionTag.MatchString(d.Lyrics):
		problem(FieldLyrics, "lyrics need at least one section tag such as [Verse] or [Chorus] on its own line")
	}
	if strings.TrimSpace(d.Caption.GlobalMetadata) == "" {
		problem(FieldGlobalMetadata, "caption global metadata is empty")
	}
	if strings.TrimSpace(d.Caption.VocalDetails) == "" {
		problem(FieldVocalDetails, "caption vocal details are empty")
	}
	if strings.TrimSpace(d.Caption.Arrangement) == "" {
		problem(FieldArrangement, "caption arrangement is empty")
	}
	if d.Seconds < MinSeconds || d.Seconds > MaxSeconds {
		problem(FieldSeconds, fmt.Sprintf("length %.0fs is outside %d-%d seconds", d.Seconds, MinSeconds, MaxSeconds))
	}
	return errors.Join(errs...)
}

type Chatter interface {
	Chat(ctx context.Context, req ollama.ChatRequest) (string, error)
}

// DefaultTemperature is the sampling temperature drafts use unless configured.
const DefaultTemperature = 0.8

type Writer struct {
	llm         Chatter
	model       string
	temperature float64
	system      string
	numCtx      int
	keepAlive   string
}

func New(llm Chatter, model string) *Writer {
	return &Writer{llm: llm, model: model, temperature: DefaultTemperature, system: BuiltinInstructions}
}

func (w *Writer) WithTemperature(t float64) *Writer {
	w.temperature = t
	return w
}

// WithContext asks for a context of n tokens; 0 leaves it to Ollama.
func (w *Writer) WithContext(n int) *Writer {
	w.numCtx = n
	return w
}

// WithKeepAlive sets how long Ollama keeps the model loaded after a draft;
// blank leaves it to Ollama.
func (w *Writer) WithKeepAlive(d string) *Writer {
	w.keepAlive = d
	return w
}

// WithInstructions replaces the system prompt; blank keeps the built-in one.
func (w *Writer) WithInstructions(prompt string) *Writer {
	if strings.TrimSpace(prompt) != "" {
		w.system = prompt
	}
	return w
}

// Instructions returns the system prompt for an instructions file: the
// file's text, or the built-in prompt when path is blank. An unreadable or
// empty file also gives the built-in prompt, with the error for the caller to
// report.
func Instructions(path string) (string, error) {
	if path == "" {
		return BuiltinInstructions, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return BuiltinInstructions, fmt.Errorf("read writing instructions: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return BuiltinInstructions, fmt.Errorf("writing instructions %s are empty", path)
	}
	return string(data), nil
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

// BuiltinInstructions is the system prompt drafts use unless configured.
var BuiltinInstructions = `You write songs for MiniMax Music 3, a model that sings the lyrics it is given and follows a structured style caption.

Return JSON with:
- title: a short song title.
- lyrics: the full lyrics. Put each section tag alone on its own line, using only these tags: ` + bracketed(SectionTags) + `. Write singable lines with natural rhythm and rhyme. Tags carry the structure; never write stage directions inside lyric lines.
- caption.global_metadata: genre and subgenres, tempo (BPM or a range), key and scale only when useful, the emotional progression across the song, listening scenario, and production profile.
- caption.vocal_details: lead vocal gender, timbre, register and delivery, plus harmonies, backing vocals and restrained effects. For an instrumental, say it is instrumental and name the lead instrument.
- caption.arrangement: a section-by-section timeline matching the lyric tags. For each section, state what enters, exits or intensifies: drums and groove, bass, harmonic instruments, leads, textures and transitions. Prefer concrete musical changes over decorative adjectives.
- seconds: the song length in seconds that fits the lyrics, between 10 and 300.

Keep the three caption parts together around 250-450 words. Never copy lyric lines into the caption. Write in English unless the idea asks for another language.`

// bracketed lists tags as "[Intro], [Verse]".
func bracketed(tags []string) string {
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = "[" + t + "]"
	}
	return strings.Join(out, ", ")
}

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
	options := map[string]any{"temperature": w.temperature}
	if w.numCtx > 0 {
		options["num_ctx"] = w.numCtx
	}
	reply, err := w.llm.Chat(ctx, ollama.ChatRequest{
		Model: w.model,
		Messages: []ollama.Message{
			{Role: "system", Content: w.system},
			{Role: "user", Content: user.String()},
		},
		Format:    draftSchema,
		Think:     &think,
		Options:   options,
		KeepAlive: ollama.KeepAlive(w.keepAlive),
	})
	if err != nil {
		return Draft{}, fmt.Errorf("draft song with %s: %w", w.model, err)
	}
	var d Draft
	if err := json.Unmarshal([]byte(reply), &d); err != nil {
		return Draft{}, fmt.Errorf("model %s returned invalid JSON: %w", w.model, err)
	}
	d.Lyrics = strings.TrimSpace(d.Lyrics)
	d.Instrumental = opts.Instrumental
	if opts.Seconds > 0 {
		d.Seconds = opts.Seconds
	}
	d.Seconds = min(max(d.Seconds, MinSeconds), MaxSeconds)
	if err := d.Validate(); err != nil {
		return d, fmt.Errorf("draft from %s is incomplete: %w", w.model, err)
	}
	return d, nil
}
