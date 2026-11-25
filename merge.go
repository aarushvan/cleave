package main

import (
	"slices"
	"strconv"
	"strings"
)

type MergeResult struct {
	Text      string `json:"text"`
	Conflicts int    `json:"conflicts"`
}

type hunk struct {
	start, end int
	lines      []string
}

func hunks(base, variant []string) []hunk {
	var out []hunk
	var h hunk
	open := false
	flush := func() {
		if width := h.end - h.start; width > 1 && width == len(h.lines) {
			// Preserve matching edits on adjacent lines as separate changes.
			for i, line := range h.lines {
				at := h.start + i
				if base[at] != line {
					out = append(out, hunk{at, at + 1, h.lines[i : i+1]})
				}
			}
		} else {
			out = append(out, h)
		}
		open = false
	}
	for _, s := range myers(base, variant) {
		if s.op == '=' {
			if open {
				flush()
			}
			continue
		}
		if !open {
			h = hunk{start: s.a, end: s.a}
			open = true
		}
		if s.op == '-' {
			h.end++
		} else {
			h.lines = append(h.lines, variant[s.b])
		}
	}
	if open {
		flush()
	}
	return out
}

func overlaps(a, b hunk) bool {
	if a.start == b.start {
		return (a.start == a.end) == (b.start == b.end)
	}
	if a.start < b.start {
		return b.start < a.end
	}
	return a.start < b.end
}

func applyHunks(base []string, start, end int, edits []hunk) []string {
	var out []string
	for _, h := range edits {
		out = append(out, base[start:h.start]...)
		out = append(out, h.lines...)
		start = h.end
	}
	return append(out, base[start:end]...)
}

func conflictLines(out, left, right []string) []string {
	out = append(out, "<<<<<<< left\n")
	out = append(out, left...)
	out = append(out, "=======\n")
	out = append(out, right...)
	return append(out, ">>>>>>> right\n")
}

func mergeLines(base, left, right []string, lang string) ([]string, int) {
	switch {
	case slices.Equal(left, right), slices.Equal(base, right):
		return left, 0
	case slices.Equal(base, left):
		return right, 0
	}
	a, b := hunks(base, left), hunks(base, right)
	var out []string
	pos, conflicts := 0, 0
	for len(a) > 0 || len(b) > 0 {
		if len(a) > 0 && len(b) > 0 && overlaps(a[0], b[0]) {
			start, end := min(a[0].start, b[0].start), max(a[0].end, b[0].end)
			i, j := 1, 1
			for {
				prevI, prevJ := i, j
				for i < len(a) && a[i].start < end {
					end = max(end, a[i].end)
					i++
				}
				for j < len(b) && b[j].start < end {
					end = max(end, b[j].end)
					j++
				}
				if i == prevI && j == prevJ {
					break
				}
			}
			l, r := applyHunks(base, start, end, a[:i]), applyHunks(base, start, end, b[:j])
			out = append(out, base[pos:start]...)
			switch {
			case slices.Equal(l, r), slices.Equal(base[start:end], r):
				out = append(out, l...)
			case slices.Equal(base[start:end], l):
				out = append(out, r...)
			default:
				if supported(lang) && end == start+1 && len(l) == 1 && len(r) == 1 &&
					!strings.ContainsRune(base[start]+l[0]+r[0], '\x00') {
					parts, n := mergeLines(tokenParts(base[start], lang), tokenParts(l[0], lang), tokenParts(r[0], lang), "")
					if n == 0 {
						out = append(out, strings.Join(parts, ""))
						pos, a, b = end, a[i:], b[j:]
						continue
					}
				}
				out = conflictLines(out, l, r)
				conflicts++
			}
			pos, a, b = end, a[i:], b[j:]
			continue
		}
		var h hunk
		if len(b) == 0 || (len(a) > 0 &&
			(a[0].start < b[0].start || (a[0].start == b[0].start && a[0].end < b[0].end))) {
			h, a = a[0], a[1:]
		} else {
			h, b = b[0], b[1:]
		}
		out = append(out, base[pos:h.start]...)
		out = append(out, h.lines...)
		pos = h.end
	}
	return append(out, base[pos:]...), conflicts
}

func tokenParts(line, lang string) []string {
	var parts []string
	pos := 0
	for _, t := range lex(line, lang) {
		if pos < t.start {
			parts = append(parts, line[pos:t.start])
		}
		parts = append(parts, t.word)
		pos = t.end
	}
	if pos < len(line) {
		parts = append(parts, line[pos:])
	}
	return parts
}

type region struct {
	text     string
	lines    []string
	blocks   []Block
	from, to int
}

func (r region) sub(b Block) region {
	return region{r.text, r.lines, wholeBlocks(r.text, b.Children), b.StartLine - 1, b.EndLine}
}

func (r region) raw() []string {
	return r.lines[r.from:r.to]
}

func (r region) layout(labels map[int]string) []string {
	var out []string
	pos := r.from
	for i, b := range r.blocks {
		if label, ok := labels[i]; ok {
			out = append(out, r.lines[pos:b.StartLine-1]...)
			out = append(out, label)
			pos = b.EndLine
		}
	}
	return append(out, r.lines[pos:r.to]...)
}

func partners(base, other []Block) map[int]int {
	out := make(map[int]int)
	for _, p := range pairBlocks(base, other, true) {
		out[p.a] = p.b
	}
	return out
}

// A layout diff moves declaration IDs, while each declaration's contents
// merge against its original bytes. Resolved bodies expand at the chosen IDs.
func mergeRegion(base, left, right region, prefix, lang string) ([]string, int) {
	switch {
	case slices.Equal(left.raw(), right.raw()), slices.Equal(base.raw(), right.raw()):
		return left.raw(), 0
	case slices.Equal(base.raw(), left.raw()):
		return right.raw(), 0
	}
	li, ri := partners(base.blocks, left.blocks), partners(base.blocks, right.blocks)
	bl, ll, rl := make(map[int]string), make(map[int]string), make(map[int]string)
	type declaration struct {
		lines     []string
		conflicts int
	}
	bodies := make(map[string]declaration)
	for i, b := range base.blocks {
		l, lok := li[i]
		r, rok := ri[i]
		if !lok || !rok {
			continue
		}
		label := prefix + strconv.Itoa(i) + "\n"
		bl[i], ll[l], rl[r] = label, label, label
		content, n := mergeRegion(base.sub(b), left.sub(left.blocks[l]), right.sub(right.blocks[r]), prefix, lang)
		bodies[label] = declaration{content, n}
	}
	if len(bodies) == 0 {
		return mergeLines(base.raw(), left.raw(), right.raw(), lang)
	}
	layout, conflicts := mergeLines(base.layout(bl), left.layout(ll), right.layout(rl), lang)
	seen := make(map[string]int)
	var out []string
	for _, line := range layout {
		if body, ok := bodies[line]; ok {
			seen[line]++
			out = append(out, body.lines...)
			conflicts += body.conflicts
		} else {
			out = append(out, line)
		}
	}
	// Independent-looking layout edits can give a declaration two locations.
	// Keep both branch alternatives when their clean layouts cannot compose.
	for label := range bodies {
		if seen[label] == 0 || (seen[label] > 1 && !slices.Contains(layout, "<<<<<<< left\n")) {
			return conflictLines(nil, left.raw(), right.raw()), 1
		}
	}
	return out, conflicts
}

func Merge3(base, left, right, lang string) MergeResult {
	// Treat the file's final newline separately so moving a declaration to
	// or from EOF does not turn an otherwise identical line into a body edit.
	regionOf := func(text string) region {
		ls := lines(text)
		if len(ls) > 0 && !strings.HasSuffix(text, "\n") {
			ls[len(ls)-1] += "\n"
		}
		return region{text: text, lines: ls, to: len(ls)}
	}
	b, l, r := regionOf(base), regionOf(left), regionOf(right)
	if supported(lang) {
		bb, be := ParseBlocks(base, lang)
		lb, le := ParseBlocks(left, lang)
		rb, re := ParseBlocks(right, lang)
		if be == nil && le == nil && re == nil {
			b.blocks, l.blocks, r.blocks = wholeBlocks(base, bb), wholeBlocks(left, lb), wholeBlocks(right, rb)
		}
	}
	prefix := "\x00"
	for strings.Contains(base, prefix) || strings.Contains(left, prefix) || strings.Contains(right, prefix) {
		prefix += "\x00"
	}
	body, conflicts := mergeRegion(b, l, r, prefix, lang)
	text := strings.Join(body, "")
	finalNL := strings.HasSuffix(left, "\n")
	if finalNL == strings.HasSuffix(base, "\n") {
		finalNL = strings.HasSuffix(right, "\n")
	}
	if conflicts == 0 && !finalNL {
		text = strings.TrimSuffix(text, "\n")
	}
	return MergeResult{Text: text, Conflicts: conflicts}
}
