package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// A folder is any directory in the library that is not a song, that is one
// without a song.json. Songs live in folders as deep as the user likes; a
// song's ID is still its own directory's name, unique across the library,
// so moving a song never changes its ID.

// ErrNoSong means no song in the library has the ID.
var ErrNoSong = fmt.Errorf("no such song: %w", fs.ErrNotExist)

// ErrBusy means a song is queued or rendering, so it cannot be moved or
// deleted until it finishes or is cancelled.
var ErrBusy = errors.New("is queued or rendering; cancel it or wait for it to finish")

// maxDepth bounds how deep the library is searched for songs.
const maxDepth = 16

// isSongDir reports whether dir holds a song.
func isSongDir(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, songFile))
	return err == nil && fi.Mode().IsRegular()
}

// validName reports whether name can be one element of a library path.
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.HasPrefix(name, ".") &&
		!strings.ContainsAny(name, `/\`) && !strings.ContainsRune(name, 0)
}

// CleanFolder checks a folder path typed by a user, such as "Albums/Demos",
// and returns it in the form the library uses, "" for the top level.
func CleanFolder(path string) (string, error) {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return "", nil
	}
	parts := strings.Split(path, "/")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if !validName(p) {
			if strings.HasPrefix(p, ".") && p != "." && p != ".." {
				return "", fmt.Errorf("folder %q: a name cannot start with a dot", path)
			}
			return "", fmt.Errorf("folder %q: each part needs a name", path)
		}
		parts[i] = p
	}
	if len(parts) > maxDepth-1 {
		return "", fmt.Errorf("folder %q is nested too deep", path)
	}
	return strings.Join(parts, "/"), nil
}

// folderPath is where a folder lives on disk.
func (s *Store) folderPath(folder string) string {
	return filepath.Join(s.dir, filepath.FromSlash(folder))
}

// scan walks the library, recording where each song is, and returns every
// folder, parents before children.
func (s *Store) scan() (songs map[string]string, folders []string, err error) {
	songs = map[string]string{}
	var errs []error
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		entries, err := os.ReadDir(s.folderPath(rel))
		if err != nil {
			if rel != "" || !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("list library: %w", err))
			}
			return
		}
		for _, e := range entries {
			if !e.IsDir() || !validName(e.Name()) {
				continue
			}
			child := e.Name()
			if rel != "" {
				child = rel + "/" + e.Name()
			}
			if isSongDir(s.folderPath(child)) {
				if _, dup := songs[e.Name()]; !dup {
					songs[e.Name()] = rel
				}
				continue
			}
			folders = append(folders, child)
			if depth < maxDepth {
				walk(child, depth+1)
			}
		}
	}
	walk("", 1)
	s.idx.Lock()
	s.where = songs
	s.idx.Unlock()
	return songs, folders, errors.Join(errs...)
}

// locate finds the folder song id is in, searching the library when the
// song is not where it was last seen: another process may have moved it.
func (s *Store) locate(id string) (string, bool) {
	if !validName(id) {
		return "", false
	}
	s.idx.Lock()
	folder, known := s.where[id]
	s.idx.Unlock()
	if known && isSongDir(filepath.Join(s.folderPath(folder), id)) {
		return folder, true
	}
	if isSongDir(filepath.Join(s.dir, id)) {
		s.remember(id, "")
		return "", true
	}
	songs, _, _ := s.scan()
	folder, ok := songs[id]
	return folder, ok
}

func (s *Store) remember(id, folder string) {
	s.idx.Lock()
	defer s.idx.Unlock()
	if s.where == nil {
		s.where = map[string]string{}
	}
	s.where[id] = folder
}

func (s *Store) forget(id string) {
	s.idx.Lock()
	defer s.idx.Unlock()
	delete(s.where, id)
}

// Folders lists every folder in the library, such as "Albums" and
// "Albums/Demos", empty ones included, sorted.
func (s *Store) Folders() ([]string, error) {
	_, folders, err := s.scan()
	slices.Sort(folders)
	return folders, err
}

// FolderOf is the folder song id is in, "" for the top level.
func (s *Store) FolderOf(id string) (string, error) {
	folder, ok := s.locate(id)
	if !ok {
		return "", fmt.Errorf("%s: %w", id, ErrNoSong)
	}
	return folder, nil
}

// MakeFolder creates a folder, and any parents it needs.
func (s *Store) MakeFolder(path string) (string, error) {
	folder, err := CleanFolder(path)
	if err != nil {
		return "", err
	}
	if folder == "" {
		return "", nil
	}
	if err := s.checkFolderPath(folder); err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.folderPath(folder), 0o755); err != nil {
		return "", fmt.Errorf("create folder %s: %w", folder, err)
	}
	return folder, nil
}

// checkFolderPath refuses a folder path that runs through a song.
func (s *Store) checkFolderPath(folder string) error {
	parts := strings.Split(folder, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		if isSongDir(s.folderPath(p)) {
			return fmt.Errorf("%s is a song, not a folder", p)
		}
		if fi, err := os.Stat(s.folderPath(p)); err == nil && !fi.IsDir() {
			return fmt.Errorf("%s is a file, not a folder", p)
		}
	}
	return nil
}

func busy(song Song) bool {
	return song.Status == StatusQueued || song.Status == StatusRendering
}

// Move puts song id in folder, "" for the top level, creating the folder
// if needed. A queued or rendering song stays where it is.
func (s *Store) Move(id, folder string) (Song, error) {
	folder, err := CleanFolder(folder)
	if err != nil {
		return Song{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	song, err := s.Load(id)
	if err != nil {
		return Song{}, err
	}
	if busy(song) {
		return song, fmt.Errorf("%s %w", song.Title, ErrBusy)
	}
	if song.Folder == folder {
		return song, nil
	}
	if folder != "" {
		if err := s.checkFolderPath(folder); err != nil {
			return song, err
		}
		if err := os.MkdirAll(s.folderPath(folder), 0o755); err != nil {
			return song, fmt.Errorf("create folder %s: %w", folder, err)
		}
	}
	to := filepath.Join(s.folderPath(folder), id)
	if _, err := os.Lstat(to); err == nil {
		return song, fmt.Errorf("move %s: %s already has a %s", song.Title, cmpFolder(folder), id)
	}
	if err := os.Rename(s.songDir(id), to); err != nil {
		return song, fmt.Errorf("move %s: %w", song.Title, err)
	}
	s.remember(id, folder)
	song.Folder = folder
	return song, nil
}

func cmpFolder(folder string) string {
	if folder == "" {
		return "the library"
	}
	return folder
}

// Delete removes song id and every take of it, for good. A queued or
// rendering song is refused.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	song, err := s.Load(id)
	if err != nil {
		return err
	}
	if busy(song) {
		return fmt.Errorf("%s %w", song.Title, ErrBusy)
	}
	dir := s.songDir(id)
	if !isSongDir(dir) || filepath.Clean(dir) == filepath.Clean(s.dir) {
		return fmt.Errorf("delete %s: %w", id, ErrNoSong)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("delete %s: %w", song.Title, err)
	}
	s.forget(id)
	return nil
}

// SongsIn lists the songs in folder and the folders inside it.
func (s *Store) SongsIn(folder string) ([]Song, error) {
	songs, err := s.List()
	return slices.DeleteFunc(songs, func(song Song) bool { return !InFolder(song.Folder, folder) }), err
}

// InFolder reports whether path is folder or inside it.
func InFolder(path, folder string) bool {
	return path == folder || strings.HasPrefix(path, folder+"/")
}

// openFolder checks that folder names an existing folder in the library.
func (s *Store) openFolder(path string) (string, error) {
	folder, err := CleanFolder(path)
	if err != nil {
		return "", err
	}
	if folder == "" {
		return "", errors.New("the library itself is not a folder that can be changed")
	}
	fi, err := os.Stat(s.folderPath(folder))
	if err != nil || !fi.IsDir() || isSongDir(s.folderPath(folder)) {
		return "", fmt.Errorf("no folder %s in the library", folder)
	}
	return folder, nil
}

// MoveFolder moves or renames folder from to the path to, with every song
// in it. It is refused while any of those songs is queued or rendering.
func (s *Store) MoveFolder(from, to string) (string, error) {
	from, err := s.openFolder(from)
	if err != nil {
		return "", err
	}
	to, err = CleanFolder(to)
	if err != nil {
		return "", err
	}
	if to == "" {
		return "", errors.New("give the folder a name")
	}
	if InFolder(to, from) {
		if to == from {
			return from, nil
		}
		return "", fmt.Errorf("%s cannot go inside itself", from)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refuseBusy(from); err != nil {
		return "", err
	}
	if _, err := os.Lstat(s.folderPath(to)); err == nil {
		return "", fmt.Errorf("%s already exists", to)
	}
	if parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(to))); parent != "." {
		if err := s.checkFolderPath(parent); err != nil {
			return "", err
		}
		if err := os.MkdirAll(s.folderPath(parent), 0o755); err != nil {
			return "", fmt.Errorf("create folder %s: %w", parent, err)
		}
	}
	if err := os.Rename(s.folderPath(from), s.folderPath(to)); err != nil {
		return "", fmt.Errorf("move %s: %w", from, err)
	}
	s.scan()
	return to, nil
}

// DeleteFolder removes folder and every song in it, for good, returning
// how many songs it removed. It is refused while any of them is queued or
// rendering.
func (s *Store) DeleteFolder(path string) (int, error) {
	folder, err := s.openFolder(path)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refuseBusy(folder); err != nil {
		return 0, err
	}
	songs, _ := s.SongsIn(folder)
	if err := os.RemoveAll(s.folderPath(folder)); err != nil {
		return 0, fmt.Errorf("delete folder %s: %w", folder, err)
	}
	s.scan()
	return len(songs), nil
}

// refuseBusy fails when a song in folder is queued or rendering. Call with
// s.mu held.
func (s *Store) refuseBusy(folder string) error {
	songs, _ := s.SongsIn(folder)
	for _, song := range songs {
		if busy(song) {
			return fmt.Errorf("%s holds %s, which %w", folder, song.Title, ErrBusy)
		}
	}
	return nil
}
