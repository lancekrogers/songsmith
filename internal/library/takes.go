package library

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lancekrogers/songsmith/internal/master"
)

// TakeKind says how a take was made.
type TakeKind string

const (
	TakeRender TakeKind = "render"
	TakeMaster TakeKind = "master"
)

// Take is one audio file of a song: a render, or a master made from one.
// Nothing ever deletes or overwrites a take.
type Take struct {
	File string   `json:"file"`
	Kind TakeKind `json:"kind"`
	// Source is the take a master was made from.
	Source   string    `json:"source,omitempty"`
	Created  time.Time `json:"created"`
	Duration float64   `json:"duration,omitzero"`
	// Loudness is the take's integrated loudness in LUFS, when measured.
	Loudness float64 `json:"loudness,omitzero"`
	// Master is the chain a master was made with.
	Master *master.Settings `json:"master,omitempty"`
}

// Label names the take for a list: its kind, and for a master its presets.
func (t Take) Label() string {
	if t.Kind == TakeMaster && t.Master != nil && !t.Master.IsZero() {
		return "mastered (" + master.Name(*t.Master) + ")"
	}
	if t.Kind == TakeMaster {
		return "mastered"
	}
	return "render"
}

var (
	renderFile = regexp.MustCompile(`^song(-[0-9]+)?\.flac$`)
	masterFile = regexp.MustCompile(`^(.+)\.master-([0-9]+)\.flac$`)
)

// discovered describes a take file found on disk without a record.
func discovered(name string, fi fs.FileInfo) (Take, bool) {
	t := Take{File: name, Created: fi.ModTime()}
	switch {
	case renderFile.MatchString(name):
		t.Kind = TakeRender
	case masterFile.MatchString(name):
		t.Kind = TakeMaster
		t.Source = masterFile.FindStringSubmatch(name)[1] + ".flac"
	default:
		return Take{}, false
	}
	return t, true
}

// Takes lists a song's takes, oldest first: those song.json records that
// are still on disk, then any take files it does not record.
func (s *Store) Takes(song Song) []Take {
	dir := s.songDir(song.ID)
	var out []Take
	seen := map[string]bool{}
	for _, t := range song.Takes {
		if seen[t.File] {
			continue
		}
		fi, err := os.Stat(filepath.Join(dir, t.File))
		if err != nil {
			continue
		}
		if t.Created.IsZero() {
			t.Created = fi.ModTime()
		}
		seen[t.File] = true
		out = append(out, t)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || seen[e.Name()] {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if t, ok := discovered(e.Name(), fi); ok {
			out = append(out, t)
		}
	}
	for i := range out {
		if out[i].File == song.Audio && out[i].Duration == 0 {
			out[i].Duration = song.Duration
		}
	}
	slices.SortStableFunc(out, func(a, b Take) int {
		return cmp.Or(a.Created.Compare(b.Created), takeOrder(a.File)-takeOrder(b.File), strings.Compare(a.File, b.File))
	})
	return out
}

// takeOrder sorts renders by their number when their times tie.
func takeOrder(name string) int {
	m := renderFile.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(m[1], "-"))
	return max(n, 1)
}

// TakeIndex is file's place among takes, from 1, or 0 when it is not one.
func TakeIndex(takes []Take, file string) int {
	return slices.IndexFunc(takes, func(t Take) bool { return t.File == file }) + 1
}

// NextMaster returns an unused file name for a master of source, such as
// "song-2.master-1.flac", so a master never collides with a render or
// replaces another master.
func (s *Store) NextMaster(song Song, source string) string {
	base := strings.TrimSuffix(source, filepath.Ext(source))
	for n := 1; ; n++ {
		name := fmt.Sprintf("%s.master-%d.flac", base, n)
		if _, err := os.Lstat(filepath.Join(s.songDir(song.ID), name)); errors.Is(err, fs.ErrNotExist) {
			return name
		}
	}
}

// TakePath is where a take of song lives.
func (s *Store) TakePath(song Song, file string) string {
	return filepath.Join(s.songDir(song.ID), file)
}

// Update loads song id, applies fn and saves the result, holding the
// store's lock so changes made through Update in this process never lose
// one another.
func (s *Store) Update(id string, fn func(*Song) error) (Song, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	song, err := s.Load(id)
	if err != nil {
		return Song{}, err
	}
	if err := fn(&song); err != nil {
		return song, err
	}
	return song, s.save(song)
}

// AddTake records t on song id, replacing any record of the same file.
func (s *Store) AddTake(id string, t Take) (Song, error) {
	return s.Update(id, func(song *Song) error {
		song.Takes = slices.DeleteFunc(song.Takes, func(x Take) bool { return x.File == t.File })
		song.Takes = append(song.Takes, t)
		return nil
	})
}

// ErrNotATake means a file is not one of the song's takes.
var ErrNotATake = errors.New("not one of this song's takes")

// SetCurrent makes file the take the library plays for song id. The take
// it replaces keeps its length on record, and a take of unknown length is
// probed, so switching back and forth never loses a length.
func (s *Store) SetCurrent(id, file string) (Song, error) {
	return s.Update(id, func(song *Song) error {
		takes := s.Takes(*song)
		i := TakeIndex(takes, file)
		if i == 0 {
			return fmt.Errorf("%s: %w", file, ErrNotATake)
		}
		if song.Status == StatusRendering || song.Status == StatusQueued {
			return fmt.Errorf("%s is %s; choose its take when it is done", song.Title, song.Status)
		}
		if out := TakeIndex(takes, song.Audio); out > 0 {
			recordTake(song, takes[out-1])
		}
		next := takes[i-1]
		if next.Duration == 0 && s.Probe != nil {
			next.Duration = s.Probe(s.TakePath(*song, file))
			recordTake(song, next)
		}
		song.Audio, song.Duration = file, next.Duration
		song.Status = StatusDone
		return nil
	})
}

// recordTake puts t on record, filling only what the record lacks.
func recordTake(song *Song, t Take) {
	for i, r := range song.Takes {
		if r.File == t.File {
			if r.Duration == 0 {
				song.Takes[i].Duration = t.Duration
			}
			if r.Loudness == 0 {
				song.Takes[i].Loudness = t.Loudness
			}
			return
		}
	}
	song.Takes = append(song.Takes, t)
}

// RecordMeasure keeps a take's measured length and loudness on record, so
// lists show them without measuring again.
func (s *Store) RecordMeasure(id, file string, duration, loudness float64) error {
	_, err := s.Update(id, func(song *Song) error {
		i := TakeIndex(s.Takes(*song), file)
		if i == 0 {
			return fmt.Errorf("%s: %w", file, ErrNotATake)
		}
		t := s.Takes(*song)[i-1]
		t.Duration, t.Loudness = duration, loudness
		recordTake(song, t)
		return nil
	})
	return err
}

// MasterFunc masters in into out with s, reporting progress from 0 to 1.
type MasterFunc func(ctx context.Context, in, out string, s master.Settings, progress func(float64)) (master.Measure, error)

// MasterTake masters source into a new take of song id and records it. The
// current take does not change.
func (s *Store) MasterTake(ctx context.Context, id, source string, set master.Settings, run MasterFunc, progress func(float64)) (Take, Song, error) {
	song, err := s.Load(id)
	if err != nil {
		return Take{}, Song{}, err
	}
	if TakeIndex(s.Takes(song), source) == 0 {
		return Take{}, song, fmt.Errorf("%s: %w", source, ErrNotATake)
	}
	name := s.NextMaster(song, source)
	m, err := run(ctx, s.TakePath(song, source), s.TakePath(song, name), set, progress)
	if err != nil {
		return Take{}, song, err
	}
	settings := set
	t := Take{File: name, Kind: TakeMaster, Source: source, Created: s.now(), Duration: m.Duration, Loudness: m.Loudness, Master: &settings}
	song, err = s.AddTake(id, t)
	return t, song, err
}
