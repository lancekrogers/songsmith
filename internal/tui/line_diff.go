package tui

import "strings"

type diffOp byte

const (
	diffSame    diffOp = ' '
	diffAdded   diffOp = '+'
	diffRemoved diffOp = '-'
)

type diffLine struct {
	op   diffOp
	text string
}

// lineDiff is the shortest edit from old to new by longest common
// subsequence, with removals before additions where both replace a block.
// Lines compare without trailing spaces.
func lineDiff(old, new []string) []diffLine {
	trim := func(lines []string) []string {
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = strings.TrimRight(l, " \t")
		}
		return out
	}
	a, b := trim(old), trim(new)
	// lcs[i][j] is the common length of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	out := make([]diffLine, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			out = append(out, diffLine{diffSame, b[j]})
			i++
			j++
		case i < len(a) && (j == len(b) || lcs[i+1][j] >= lcs[i][j+1]):
			out = append(out, diffLine{diffRemoved, a[i]})
			i++
		default:
			out = append(out, diffLine{diffAdded, b[j]})
			j++
		}
	}
	return out
}

// splitLines splits text into lines; empty text has none.
func splitLines(text string) []string {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
