// Package library stores songs on disk, one directory per song.
package library

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/lancekrogers/songsmith/internal/writer"
)

type Status string

const (
	StatusDraft     Status = "draft"
	StatusQueued    Status = "queued"
	StatusRendering Status = "rendering"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
)

type Song struct {
	ID       string         `json:"id"`
	Created  time.Time      `json:"created"`
	Updated  time.Time      `json:"updated"`
	Idea     string         `json:"idea,omitempty"`
	Title    string         `json:"title"`
	Lyrics   string         `json:"lyrics"`
	Caption  writer.Caption `json:"caption"`
	Seconds  float64        `json:"seconds"`
	Seed     int64          `json:"seed"`
	Status   Status         `json:"status"`
	Error    string         `json:"error,omitempty"`
	Audio    string         `json:"audio,omitempty"`
	Duration float64        `json:"duration,omitzero"`
	Backend  string         `json:"backend,omitempty"`
	// JobID is the backend's ID for an unfinished render, kept so a render
	// interrupted by a crash can be resumed instead of submitted twice.
	JobID    string    `json:"job_id,omitempty"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	// Timings holds seconds spent in each render stage.
	Timings map[string]float64 `json:"timings,omitempty"`
	// Instrumental songs are drafted without sung lines.
	Instrumental bool `json:"instrumental,omitempty"`
}

const songFile = "song.json"

type Store struct {
	dir string
	now func() time.Time
}

func NewStore(dir string) *Store {
	return &Store{dir: dir, now: time.Now}
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) songDir(id string) string { return filepath.Join(s.dir, id) }

// AudioPath is where a song's rendered audio lives.
func (s *Store) AudioPath(song Song) string {
	return filepath.Join(s.songDir(song.ID), cmp.Or(song.Audio, "song.flac"))
}

// HasAudio reports whether the song's rendered audio file exists.
func (s *Store) HasAudio(song Song) bool {
	if song.Audio == "" {
		return false
	}
	_, err := os.Stat(s.AudioPath(song))
	return err == nil
}

// NextTake returns an unused audio file name for a new render, so a
// re-render never overwrites the take that is already there.
func (s *Store) NextTake(song Song) string {
	if !s.HasAudio(song) {
		return "song.flac"
	}
	for n := 2; ; n++ {
		name := fmt.Sprintf("song-%d.flac", n)
		if _, err := os.Stat(filepath.Join(s.songDir(song.ID), name)); errors.Is(err, fs.ErrNotExist) {
			return name
		}
	}
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// NewID builds a sortable, readable ID from the creation time and title.
func NewID(title string, at time.Time) string {
	slug := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	return at.Format("20060102-150405") + "-" + cmp.Or(slug, "untitled")
}

// Create assigns an ID and saves a new song.
func (s *Store) Create(song Song) (Song, error) {
	now := s.now()
	song.ID = NewID(song.Title, now)
	song.Created = now
	song.Status = cmp.Or(song.Status, StatusDraft)
	for i := 2; ; i++ {
		if _, err := os.Stat(s.songDir(song.ID)); errors.Is(err, fs.ErrNotExist) {
			break
		}
		song.ID = fmt.Sprintf("%s-%d", NewID(song.Title, now), i)
	}
	return song, s.Save(song)
}

// Save writes song.json atomically.
func (s *Store) Save(song Song) error {
	if song.ID == "" {
		return errors.New("save song: empty id")
	}
	dir := s.songDir(song.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create song dir: %w", err)
	}
	song.Updated = s.now()
	data, err := json.MarshalIndent(song, "", "  ")
	if err != nil {
		return fmt.Errorf("encode song: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".song-*.json")
	if err != nil {
		return fmt.Errorf("save song %s: %w", song.ID, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("save song %s: %w", song.ID, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save song %s: %w", song.ID, err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, songFile)); err != nil {
		return fmt.Errorf("save song %s: %w", song.ID, err)
	}
	return nil
}

func (s *Store) Load(id string) (Song, error) {
	data, err := os.ReadFile(filepath.Join(s.songDir(id), songFile))
	if err != nil {
		return Song{}, fmt.Errorf("load song %s: %w", id, err)
	}
	var song Song
	if err := json.Unmarshal(data, &song); err != nil {
		return Song{}, fmt.Errorf("load song %s: %w", id, err)
	}
	return song, nil
}

// List returns every readable song, newest first, plus an error describing
// any song files that could not be read.
func (s *Store) List() ([]Song, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list library: %w", err)
	}
	var songs []Song
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		song, err := s.Load(e.Name())
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		songs = append(songs, song)
	}
	slices.SortFunc(songs, func(a, b Song) int { return b.Created.Compare(a.Created) })
	return songs, errors.Join(errs...)
}

// Lock is an exclusive, process-wide claim on rendering for a library.
type Lock struct{ f *os.File }

// ErrLocked means another songsmith process is rendering from this library.
var ErrLocked = errors.New("another songsmith process is rendering from this library")

// TryLock takes the render lock without blocking.
func (s *Store) TryLock() (*Lock, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, fmt.Errorf("create library: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(s.dir, ".render.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open render lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("take render lock: %w", err)
	}
	return &Lock{f: f}, nil
}

func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	defer l.f.Close()
	return syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
}
