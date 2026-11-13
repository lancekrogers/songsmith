package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lancekrogers/songsmith/internal/config"
	"github.com/lancekrogers/songsmith/internal/library"
	"github.com/lancekrogers/songsmith/internal/ollama"
	"github.com/lancekrogers/songsmith/internal/writer"
)

const revisedReply = `{"title":"Coast Road","lyrics":"[Verse]\nWindows down\n[Verse]\nPast the pier\n[Chorus]\nInto the light","caption":{"global_metadata":"Indie pop","vocal_details":"Warm lead","arrangement":"Verse: guitar. Verse 2: bass. Chorus: drums."},"seconds":150}`

// reviseEnv is a temp config whose Ollama answers every chat with
// revisedReply, and a library holding one song.
type reviseEnv struct {
	path  string
	store *library.Store
	mu    sync.Mutex
	chats []ollama.ChatRequest
}

func newReviseEnv(t *testing.T) *reviseEnv {
	t.Helper()
	e := &reviseEnv{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollama.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		e.mu.Lock()
		e.chats = append(e.chats, req)
		e.mu.Unlock()
		content, _ := json.Marshal(revisedReply)
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":` + string(content) + `}}`))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	e.path = filepath.Join(dir, "config.yaml")
	for _, kv := range []struct {
		key   string
		value any
	}{
		{"library_dir", filepath.Join(dir, "library")}, {"state_dir", filepath.Join(dir, "state")},
		{"comfyui.dir", filepath.Join(dir, "ComfyUI")}, {"comfyui.port", 18188},
		{"ollama.url", srv.URL}, {"ollama.model", "writer:1b"},
	} {
		if err := config.Set(e.path, kv.key, kv.value); err != nil {
			t.Fatal(err)
		}
	}
	e.store = library.NewStore(filepath.Join(dir, "library"))
	return e
}

func (e *reviseEnv) run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"--config", e.path}, args...))
	err := cmd.ExecuteContext(t.Context())
	return out.String(), errOut.String(), err
}

func (e *reviseEnv) song(t *testing.T, s library.Song) library.Song {
	t.Helper()
	s.Lyrics = "[Verse]\nWindows down\n[Chorus]\nInto the light"
	s.Caption = writer.Caption{GlobalMetadata: "Indie pop", VocalDetails: "Warm lead", Arrangement: "Verse: guitar. Chorus: drums."}
	created, err := e.store.Create(s)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestRevisePrintsTheRevisionAndSavesNothing(t *testing.T) {
	e := newReviseEnv(t)
	src := e.song(t, library.Song{Title: "Coast Road", Seconds: 90, Status: library.StatusDraft})
	out, _, err := e.run(t, "revise", src.ID, "add a verse about the pier")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Past the pier") || !strings.Contains(out, "seconds: 150") {
		t.Fatalf("output:\n%s", out)
	}
	if len(e.chats) != 1 || !strings.Contains(e.chats[0].Messages[1].Content, "Instruction: add a verse about the pier") || e.chats[0].Model != "writer:1b" {
		t.Fatalf("chats %+v", e.chats)
	}
	if songs, _ := e.store.List(); len(songs) != 1 {
		t.Fatalf("%d songs", len(songs))
	}
	if _, _, err := e.run(t, "revise", "--seed", "5", src.ID, "longer"); err == nil || !strings.Contains(err.Error(), "--save") {
		t.Fatalf("--seed without --save: %v", err)
	}
}

func TestReviseSaveMakesANewSongAndKeepsTheSource(t *testing.T) {
	e := newReviseEnv(t)
	done := e.song(t, library.Song{Title: "Coast Road", Seconds: 180, Seed: 281474976710, Status: library.StatusDone})
	draft := e.song(t, library.Song{Title: "Draft Road", Seconds: 60, Status: library.StatusDraft})

	if _, msg, err := e.run(t, "revise", "--save", done.ID, "longer"); err != nil || !strings.Contains(msg, "Saved ") {
		t.Fatalf("save: %v %q", err, msg)
	}
	if _, _, err := e.run(t, "revise", "--queue", "--seed", "12", draft.ID, "longer"); err != nil {
		t.Fatal(err)
	}
	songs, _ := e.store.List()
	if len(songs) != 4 {
		t.Fatalf("%d songs", len(songs))
	}
	by := map[string]library.Song{}
	for _, s := range songs {
		by[s.DerivedFrom] = s
	}
	copyOfDone, copyOfDraft := by[done.ID], by[draft.ID]
	if copyOfDone.Title != "Coast Road (extended)" || !copyOfDone.SeedPinned || copyOfDone.Seed != done.Seed ||
		copyOfDone.Seconds != 180 || copyOfDone.Status != library.StatusDraft || !strings.Contains(copyOfDone.Lyrics, "Past the pier") {
		t.Fatalf("copy of the done song: %+v", copyOfDone)
	}
	if copyOfDraft.Title != "Coast Road" || copyOfDraft.Seed != 12 || !copyOfDraft.SeedPinned || copyOfDraft.Seconds != 150 || copyOfDraft.Status != library.StatusQueued {
		t.Fatalf("copy of the draft: %+v", copyOfDraft)
	}
	for _, src := range []library.Song{done, draft} {
		stored, _ := e.store.Load(src.ID)
		if stored.Lyrics != src.Lyrics || stored.Status != src.Status {
			t.Fatalf("source changed: %+v", stored)
		}
	}

	out, _, err := e.run(t, "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{done.ID, "seed 281474976710 random", "seed 12 pinned", "seed -"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list lacks %q:\n%s", want, out)
		}
	}
}

func TestReviseUnknownSong(t *testing.T) {
	e := newReviseEnv(t)
	if _, _, err := e.run(t, "revise", "20200101-000000-missing", "longer"); err == nil {
		t.Fatal("want an error")
	}
	if len(e.chats) != 0 {
		t.Fatal("asked the writer about a missing song")
	}
}
