package tui

import (
	"strings"
	"testing"
)

func diffString(d []diffLine) string {
	var b strings.Builder
	for _, l := range d {
		b.WriteString(string(l.op) + l.text + "\n")
	}
	return b.String()
}

func TestLineDiff(t *testing.T) {
	cases := []struct {
		name     string
		old, new []string
		want     string
	}{
		{"unchanged", []string{"a", "b"}, []string{"a", "b"}, " a\n b\n"},
		{"added in the middle", []string{"a", "c"}, []string{"a", "b", "c"}, " a\n+b\n c\n"},
		{"added at the end", []string{"a"}, []string{"a", "b", "c"}, " a\n+b\n+c\n"},
		{"removed", []string{"a", "b", "c"}, []string{"a", "c"}, " a\n-b\n c\n"},
		{"replaced", []string{"a", "b", "c"}, []string{"a", "x", "c"}, " a\n-b\n+x\n c\n"},
		{"reordered blocks", []string{"v1", "v2", "c1", "c2"}, []string{"c1", "c2", "v1", "v2"}, "-v1\n-v2\n c1\n c2\n+v1\n+v2\n"},
		{"empty old", nil, []string{"a", "b"}, "+a\n+b\n"},
		{"empty new", []string{"a", "b"}, nil, "-a\n-b\n"},
		{"both empty", nil, nil, ""},
		{"trailing spaces are not a change", []string{"a  "}, []string{"a"}, " a\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := diffString(lineDiff(c.old, c.new)); got != c.want {
				t.Fatalf("got\n%swant\n%s", got, c.want)
			}
		})
	}
}

func TestLineDiffKeepsEveryLine(t *testing.T) {
	old := strings.Split("[Verse]\nla\nla\n\n[Chorus]\nhey\n\n[Verse]\nla", "\n")
	new := strings.Split("[Verse]\nla\nla\n\n[Pre-Chorus]\nup\n\n[Chorus]\nhey\n\n[Bridge]\nla\n\n[Verse]\nla", "\n")
	var gotOld, gotNew []string
	for _, l := range lineDiff(old, new) {
		if l.op != diffAdded {
			gotOld = append(gotOld, l.text)
		}
		if l.op != diffRemoved {
			gotNew = append(gotNew, l.text)
		}
	}
	if strings.Join(gotOld, "\n") != strings.Join(old, "\n") || strings.Join(gotNew, "\n") != strings.Join(new, "\n") {
		t.Fatalf("diff does not rebuild both sides:\n%s", diffString(lineDiff(old, new)))
	}
}
