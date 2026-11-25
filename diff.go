package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

type step struct {
	op   byte
	a, b int
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	split := strings.SplitAfter(s, "\n")
	if split[len(split)-1] == "" {
		split = split[:len(split)-1]
	}
	return split
}

// myers builds a shortest edit script using forward and reverse frontiers.
func myers(a, b []string) []step {
	out := make([]step, 0, max(len(a), len(b)))
	var walk func(int, int, int, int)
	walk = func(x, y, endX, endY int) {
		for x < endX && y < endY && a[x] == b[y] {
			out = append(out, step{'=', x, y})
			x++
			y++
		}
		tail := 0
		for x < endX && y < endY && a[endX-1] == b[endY-1] {
			endX--
			endY--
			tail++
		}
		sx, sy := 0, 0
		if x < endX && y < endY {
			sx, sy = midpoint(a[x:endX], b[y:endY])
		}
		if (sx == 0 && sy == 0) || (x+sx == endX && y+sy == endY) {
			for i := x; i < endX; i++ {
				out = append(out, step{'-', i, y})
			}
			for j := y; j < endY; j++ {
				out = append(out, step{'+', endX, j})
			}
		} else {
			walk(x, y, x+sx, y+sy)
			walk(x+sx, y+sy, endX, endY)
		}
		for i := 0; i < tail; i++ {
			out = append(out, step{'=', endX + i, endY + i})
		}
	}
	walk(0, 0, len(a), len(b))
	return out
}

func midpoint(a, b []string) (int, int) {
	n, m := len(a), len(b)
	limit := (n + m + 1) / 2
	center := limit + 1
	front, back := make([]int, 2*center+1), make([]int, 2*center+1)
	for i := range front {
		front[i], back[i] = -1, -1
	}
	front[center+1], back[center+1] = 0, 0
	delta := n - m
	for d := 0; d <= limit; d++ {
		for pass := 0; pass < 2; pass++ {
			v, other := front, back
			if pass == 1 {
				v, other = back, front
			}
			first := max(-d, -m)
			first += (d - first) & 1
			for k := first; k <= min(d, n); k += 2 {
				i := center + k
				var x int
				if k == -d || (k != d && v[i-1] < v[i+1]) {
					x = v[i+1]
				} else {
					x = v[i-1] + 1
				}
				y := x - k
				if x < 0 || y < 0 || x > n || y > m {
					v[i] = -1
					continue
				}
				for x < n && y < m {
					ax, by := x, y
					if pass == 1 {
						ax, by = n-1-x, m-1-y
					}
					if a[ax] != b[by] {
						break
					}
					x++
					y++
				}
				v[i] = x
				opp := delta - k
				prev := d - 1 + pass
				if (delta&1) == (1-pass) && opp >= -prev && opp <= prev &&
					other[center+opp] >= 0 && x+other[center+opp] >= n {
					if pass == 0 {
						return x, y
					}
					return n - x, m - y
				}
			}
		}
	}
	return 0, 0
}

func similarity(a, b []string) float64 {
	total := len(a) + len(b)
	if total == 0 {
		return 1
	}
	counts := make(map[string]int)
	for _, word := range a {
		counts[word]++
	}
	common := 0
	for _, word := range b {
		if counts[word] > 0 {
			counts[word]--
			common++
		}
	}
	jaccard := float64(common) / float64(total-common)
	lcs := 0
	for _, s := range myers(a, b) {
		if s.op == '=' {
			lcs++
		}
	}
	return (jaccard + 2*float64(lcs)/float64(total)) / 2
}

type identity struct{ kind, name, receiver string }

func key(b Block) identity { return identity{b.Kind, b.Name, b.Receiver} }

type blockPair struct {
	a, b  int
	score float64
	exact bool
}

func pairBlocks(a, b []Block, merge bool) []blockPair {
	type fingerprint struct {
		id   identity
		hash [32]byte
	}
	hashes := make(map[fingerprint][]int)
	names := make(map[identity][]int)
	leftNames := make(map[identity]int)
	for _, block := range a {
		leftNames[key(block)]++
	}
	for j, block := range b {
		h := fingerprint{key(block), block.Hash}
		hashes[h] = append(hashes[h], j)
		names[h.id] = append(names[h.id], j)
	}
	usedA, usedB := make([]bool, len(a)), make([]bool, len(b))
	var pairs, candidates []blockPair
	for i, block := range a {
		h := fingerprint{key(block), block.Hash}
		if js := hashes[h]; len(js) > 0 {
			j := js[0]
			hashes[h] = js[1:]
			pairs = append(pairs, blockPair{i, j, 1, block.raw == b[j].raw})
			usedA[i], usedB[j] = true, true
		}
	}
	for i, block := range a {
		if usedA[i] {
			continue
		}
		id := key(block)
		for _, j := range names[id] {
			if usedB[j] {
				continue
			}
			score := similarity(block.Tokens, b[j].Tokens)
			if score >= 0.65 || (merge && leftNames[id] == 1 && len(names[id]) == 1) {
				candidates = append(candidates, blockPair{i, j, score, false})
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		p, q := candidates[i], candidates[j]
		if p.score != q.score {
			return p.score > q.score
		}
		return abs(p.a-p.b) < abs(q.a-q.b)
	})
	for _, p := range candidates {
		if !usedA[p.a] && !usedB[p.b] {
			pairs = append(pairs, p)
			usedA[p.a], usedB[p.b] = true, true
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].a < pairs[j].a })
	return pairs
}

func abs(n int) int { return max(n, -n) }

// An increasing subsequence anchors declarations in their old relative order.
// Exact pairs break ties so a two-block swap can expose the edited mover.
func stationary(pairs []blockPair) []bool {
	best := -1
	weight, prev := make([]int, len(pairs)), make([]int, len(pairs))
	for i, p := range pairs {
		w := len(pairs) + 1
		if p.exact {
			w++
		}
		weight[i], prev[i] = w, -1
		for j := range i {
			if pairs[j].b < p.b && weight[j]+w > weight[i] {
				weight[i], prev[i] = weight[j]+w, j
			}
		}
		if best == -1 || weight[i] > weight[best] {
			best = i
		}
	}
	keep := make([]bool, len(pairs))
	for best >= 0 {
		keep[best] = true
		best = prev[best]
	}
	return keep
}

// Declarations that share a line with other code stay in the line diff.
func wholeBlocks(text string, blocks []Block) []Block {
	var out []Block
	for _, b := range blocks {
		start := strings.LastIndexByte(text[:b.Start], '\n') + 1
		end := b.End
		for end < len(text) && text[end] != '\n' {
			end++
		}
		suffix := strings.TrimSpace(text[b.End:end])
		if strings.TrimSpace(text[start:b.Start]) == "" &&
			(suffix == "" || strings.HasPrefix(suffix, "//") || strings.HasPrefix(suffix, "#")) {
			out = append(out, b)
		}
	}
	return out
}

type Edit struct {
	Op   string `json:"op"`
	Old  int    `json:"old_line,omitempty"`
	New  int    `json:"new_line,omitempty"`
	Text string `json:"text"`
}

type Move struct {
	Kind       string  `json:"kind"`
	Name       string  `json:"name"`
	OldStart   int     `json:"old_start"`
	OldEnd     int     `json:"old_end"`
	NewStart   int     `json:"new_start"`
	NewEnd     int     `json:"new_end"`
	Similarity float64 `json:"similarity"`
	Edits      []Edit  `json:"edits"`
}

type DiffResult struct {
	Mode            string `json:"mode"`
	Moves           []Move `json:"moves"`
	Edits           []Edit `json:"edits"`
	NaiveChurn      int    `json:"naive_churn"`
	StructuralChurn int    `json:"structural_churn"`
	LinesSaved      int    `json:"lines_saved"`
}

type numbered struct {
	text []string
	at   []int
}

func exclude(text []string, from, to int, blocks []Block) numbered {
	var out numbered
	i := from
	for _, b := range blocks {
		for ; i < b.StartLine-1; i++ {
			out.text, out.at = append(out.text, text[i]), append(out.at, i+1)
		}
		i = b.EndLine
	}
	for ; i < to; i++ {
		out.text, out.at = append(out.text, text[i]), append(out.at, i+1)
	}
	return out
}

func changed(a, b numbered) []Edit {
	out := []Edit{}
	for _, s := range myers(a.text, b.text) {
		switch s.op {
		case '-':
			out = append(out, Edit{Op: "delete", Old: a.at[s.a], Text: a.text[s.a]})
		case '+':
			out = append(out, Edit{Op: "insert", New: b.at[s.b], Text: b.text[s.b]})
		}
	}
	return out
}

// Compare gap lines independently to detect moves across non-block code.
func gapAnchors(a, b numbered) [][2]int {
	var anchors [][2]int
	for _, s := range myers(a.text, b.text) {
		if s.op != '=' {
			continue
		}
		t := strings.TrimSpace(a.text[s.a])
		if identifier(t) {
			anchors = append(anchors, [2]int{a.at[s.a], b.at[s.b]})
		}
	}
	return anchors
}

func findMoves(old, new string, a, b []string, ax, bx []Block, af, ae, bf, be int, r *DiffResult) {
	ax, bx = wholeBlocks(old, ax), wholeBlocks(new, bx)
	pairs := pairBlocks(ax, bx, false)
	keep := stationary(pairs)
	anchors := gapAnchors(exclude(a, af, ae, ax), exclude(b, bf, be, bx))
	for i, p := range pairs {
		x, y := ax[p.a], bx[p.b]
		moved := !keep[i]
		for _, anchor := range anchors {
			if (x.StartLine < anchor[0]) != (y.StartLine < anchor[1]) {
				moved = true
				break
			}
		}
		if !moved {
			if len(x.Children) > 0 && len(y.Children) > 0 {
				findMoves(old, new, a, b, x.Children, y.Children,
					x.StartLine-1, x.EndLine, y.StartLine-1, y.EndLine, r)
			}
			continue
		}
		name := x.Name
		if x.Receiver != "" {
			name = x.Receiver + "." + name
		}
		edits := changed(exclude(a, x.StartLine-1, x.EndLine, nil), exclude(b, y.StartLine-1, y.EndLine, nil))
		r.Moves = append(r.Moves, Move{x.Kind, name, x.StartLine, x.EndLine, y.StartLine, y.EndLine, p.score, edits})
	}
}

func Diff(old, new, lang, mode string) DiffResult {
	a, b := lines(old), lines(new)
	r := DiffResult{Mode: "line", Moves: []Move{}}
	r.Edits = changed(exclude(a, 0, len(a), nil), exclude(b, 0, len(b), nil))
	r.NaiveChurn, r.StructuralChurn = len(r.Edits), len(r.Edits)
	if mode != "ast" || !supported(lang) {
		return r
	}
	ax, xerr := ParseBlocks(old, lang)
	bx, yerr := ParseBlocks(new, lang)
	if xerr != nil || yerr != nil {
		return r
	}
	r.Mode = "ast"
	findMoves(old, new, a, b, ax, bx, 0, len(a), 0, len(b), &r)
	var skipA, skipB []Block
	usedA, usedB := make(map[int]bool), make(map[int]bool)
	separator := func(text []string, used map[int]bool, start, end int) int {
		for _, line := range [2]int{end + 1, start - 1} {
			if line > 0 && line <= len(text) && !used[line] && strings.TrimSpace(text[line-1]) == "" {
				return line
			}
		}
		return 0
	}
	inner := 0
	for _, m := range r.Moves {
		skipA = append(skipA, Block{StartLine: m.OldStart, EndLine: m.OldEnd})
		skipB = append(skipB, Block{StartLine: m.NewStart, EndLine: m.NewEnd})
		// A blank separator that changed sides belongs to the move's layout.
		x := separator(a, usedA, m.OldStart, m.OldEnd)
		y := separator(b, usedB, m.NewStart, m.NewEnd)
		if x != 0 && y != 0 {
			skipA = append(skipA, Block{StartLine: x, EndLine: x})
			skipB = append(skipB, Block{StartLine: y, EndLine: y})
			usedA[x], usedB[y] = true, true
		}
		inner += len(m.Edits)
	}
	sort.Slice(skipA, func(i, j int) bool { return skipA[i].StartLine < skipA[j].StartLine })
	sort.Slice(skipB, func(i, j int) bool { return skipB[i].StartLine < skipB[j].StartLine })
	if len(r.Moves) > 0 {
		r.Edits = changed(exclude(a, 0, len(a), skipA), exclude(b, 0, len(b), skipB))
	}
	r.StructuralChurn = len(r.Edits) + inner
	r.LinesSaved = r.NaiveChurn - r.StructuralChurn
	return r
}

func renderDiff(w io.Writer, r DiffResult, color bool) {
	printEdits := func(edits []Edit, indent string) {
		for _, e := range edits {
			sign, shade, line := "-", 31, e.Old
			if e.Op == "insert" {
				sign, shade, line = "+", 32, e.New
			}
			raw := strings.TrimSuffix(strings.TrimSuffix(e.Text, "\n"), "\r")
			fmt.Fprintf(w, "%s%s\n", indent, tint(fmt.Sprintf("%s L%d %s", sign, line, raw), shade, color))
			if !strings.HasSuffix(e.Text, "\n") {
				fmt.Fprintln(w, indent+"\\ No newline at end of file")
			}
		}
	}
	for _, m := range r.Moves {
		header := fmt.Sprintf("MOVED %s %s (L%d-L%d -> L%d-L%d, %.0f%% match)",
			m.Kind, m.Name, m.OldStart, m.OldEnd, m.NewStart, m.NewEnd, 100*m.Similarity)
		fmt.Fprintln(w, tint(header, 36, color))
		printEdits(m.Edits, "  ")
	}
	printEdits(r.Edits, "")
	fmt.Fprintf(w, "naive_churn=%d structural_churn=%d lines_saved=%d\n",
		r.NaiveChurn, r.StructuralChurn, r.LinesSaved)
}

func tint(s string, shade int, color bool) string {
	if !color {
		return s
	}
	return fmt.Sprintf("\x1b[%dm%s\x1b[0m", shade, s)
}
