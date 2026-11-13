package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// writeMu serializes writers in this process so two Settings changes cannot
// read the same file and lose one of the edits.
var writeMu sync.Mutex

// Set changes one dotted key (such as "ollama.model" or "render.steps") in the
// YAML file at path. Comments, key order and every other value in the file
// are kept; the file and its directory are created when missing. Only keys
// the file names are written, so defaults never get baked into it.
func Set(path, key string, value any) error {
	field, err := fieldType(key)
	if err != nil {
		return err
	}
	var val yaml.Node
	if err := val.Encode(value); err != nil {
		return fmt.Errorf("encode %s: %w", key, err)
	}
	// yaml.v3 truncates a float into an int field, so whole numbers are checked by tag.
	isInt := field.Kind() >= reflect.Int && field.Kind() <= reflect.Int64
	if err := val.Decode(reflect.New(field).Interface()); err != nil || isInt && val.Tag != "!!int" {
		return fmt.Errorf("%s: %v is not a valid %s", key, value, field)
	}

	writeMu.Lock()
	defer writeMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	if doc.Kind == 0 {
		// An empty or comment-only file parses to nothing; keep its comments.
		doc = yaml.Node{Kind: yaml.DocumentNode, HeadComment: strings.TrimSpace(string(data)), Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if isNull(root) {
		*root = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: root.HeadComment, LineComment: root.LineComment, FootComment: root.FootComment}
	}
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("config %s: top level is not a map", path)
	}
	if err := setPath(root, strings.Split(key, "."), &val); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	var check Config
	if err := yaml.Unmarshal(out.Bytes(), &check); err != nil {
		return fmt.Errorf("updated config does not parse: %w", err)
	}
	return writeFile(path, out.Bytes())
}

// setPath walks segments under m, creating maps where they are missing, and
// puts val at the last one. An existing scalar keeps its comments.
func setPath(m *yaml.Node, segments []string, val *yaml.Node) error {
	name := segments[0]
	var cur *yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			cur = m.Content[i+1]
			break
		}
	}
	if len(segments) == 1 {
		if cur == nil {
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, val)
			return nil
		}
		val.HeadComment, val.LineComment, val.FootComment = cur.HeadComment, cur.LineComment, cur.FootComment
		*cur = *val
		return nil
	}
	if cur == nil {
		cur = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, cur)
	}
	if isNull(cur) {
		*cur = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: cur.HeadComment, LineComment: cur.LineComment, FootComment: cur.FootComment}
	}
	if cur.Kind != yaml.MappingNode {
		return fmt.Errorf("%s is not a map", name)
	}
	return setPath(cur, segments[1:], val)
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || n.Value == "" && n.Tag == "")
}

// fieldType resolves a dotted key to the Go type of its Config field, so a
// typo or a value of the wrong type is refused before anything is written.
func fieldType(key string) (reflect.Type, error) {
	t := reflect.TypeFor[Config]()
	for seg := range strings.SplitSeq(key, ".") {
		if t.Kind() != reflect.Struct {
			return nil, fmt.Errorf("unknown config key %q", key)
		}
		found := false
		for i := range t.NumField() {
			if f := t.Field(i); strings.Split(f.Tag.Get("yaml"), ",")[0] == seg {
				t, found = f.Type, true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown config key %q", key)
		}
	}
	if t.Kind() == reflect.Struct {
		return nil, fmt.Errorf("config key %q is a section, not a value", key)
	}
	return t, nil
}

// writeFile replaces path atomically, keeping an existing file's mode.
func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("save config %s: %w", path, err)
	}
	return nil
}
