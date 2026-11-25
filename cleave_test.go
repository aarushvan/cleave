package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const pkg = "package demo\n\n"
const foo = "func foo() int {\n\tx := 10\n\treturn x\n}\n\n"
const bar = "func bar() int {\n\treturn 20\n}\n\n"
const baz = "func baz() int {\n\treturn 30\n}\n\n"

func TestGoAST(t *testing.T) {
	src := "package p\n//line elsewhere.go:100\n// Box is generic.\n" +
		"type Box[T any] struct { Value T }\n\n" +
		"func (b *Box[T]) Get() T { return b.Value }\n"
	got, err := ParseBlocks(src, "go")
	if err != nil || len(got) != 2 {
		t.Fatalf("blocks = %+v, error = %v", got, err)
	}
	typ, method := got[0], got[1]
	if typ.Kind != "type" || typ.Name != "Box" || typ.StartLine != 2 ||
		typ.EndLine != 4 || typ.Signature != "type Box[T any] struct" {
		t.Fatalf("type = %+v", typ)
	}
	if method.Kind != "method" || method.Receiver != "Box" || method.Name != "Get" ||
		method.Signature != "func (b *Box[T]) Get() T" || method.StartLine != 6 ||
		method.EndLine != 6 || src[method.Start:method.End] != "func (b *Box[T]) Get() T { return b.Value }" {
		t.Fatalf("method = %+v", method)
	}
	reformatted, err := ParseBlocks("package p\nfunc (b *Box[T]) Get() T {  return b.Value /* comment */  }\n", "go")
	if err != nil || !reflect.DeepEqual(reformatted[0].Tokens, method.Tokens) || reformatted[0].Hash != method.Hash {
		t.Fatalf("normalization failed: %+v, %v", reformatted, err)
	}
}

func TestLexicalBlocks(t *testing.T) {
	for _, tc := range []struct{ lang, src, name, child string }{
		{"js", "export const solve = (x) => { return /[{}]/.test(x); };\n", "solve", ""},
		{"ts", "class C {\n  get<T>(v: T): {v: T} { return {v}; }\n}\n", "C", "get"},
		{"rs", "impl C {\n  fn get(&self) -> &str { r#\"}\"# }\n}\n", "C", "get"},
		{"py", "class C:\n    @logged\n    async def get(self):\n        return '''}\ntext'''\n", "C", "get"},
		{"c", "#include <stdio.h>\nint get(void) { return 1; }\n", "get", ""},
		{"cpp", "struct C {\npublic:\n  int get() const { return 1; }\n};\n", "C", "get"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			got, err := ParseBlocks(tc.src, tc.lang)
			if err != nil || len(got) != 1 || got[0].Name != tc.name {
				t.Fatalf("blocks = %+v, error = %v", got, err)
			}
			if tc.child != "" && (len(got[0].Children) != 1 || got[0].Children[0].Name != tc.child) {
				t.Fatalf("children = %+v", got[0].Children)
			}
			if tc.lang == "c" && got[0].StartLine != 2 {
				t.Fatalf("C function starts on line %d", got[0].StartLine)
			}
		})
	}
}

func TestMovedEdited(t *testing.T) {
	a := "export function foo(x: number): number {\n  return x + 10;\n}\n"
	b := "export function bar(): number {\n  return 20;\n}\n"
	for _, tc := range []struct{ lang, old, new string }{
		{"go", pkg + foo + bar + baz, pkg + bar + baz + strings.Replace(foo, "10", "11", 1)},
		{"ts", a + b, b + strings.Replace(a, "10", "11", 1)},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			r := Diff(tc.old, tc.new, tc.lang, "ast")
			if len(r.Moves) != 1 || r.Moves[0].Name != "foo" || len(r.Moves[0].Edits) != 2 ||
				r.Moves[0].Similarity < 0.65 || r.Moves[0].Similarity >= 1 ||
				r.LinesSaved <= 0 || r.StructuralChurn != 2 || len(r.Edits) != 0 {
				t.Fatalf("unexpected diff: %+v", r)
			}
		})
	}
	r := Diff(pkg+foo+"var x = 1\n", pkg+"var x = 1\n"+foo, "go", "ast")
	if len(r.Moves) != 1 || r.Moves[0].Name != "foo" || r.Moves[0].Similarity != 1 {
		t.Fatalf("move across a non-block line: %+v", r)
	}
}

func TestMergeMovedFunction(t *testing.T) {
	base := pkg + foo + bar + baz
	moved := pkg + bar + baz + foo
	edited := pkg + strings.Replace(foo, "return x", "return x * 2", 1) + bar + baz
	want := strings.Replace(moved, "return x", "return x * 2", 1)
	for _, sides := range [][2]string{{moved, edited}, {edited, moved}} {
		got := Merge3(base, sides[0], sides[1], "go")
		if got.Conflicts != 0 || got.Text != want {
			t.Fatalf("merge = %+v, want %q", got, want)
		}
	}
	left := strings.Replace(moved, "x := 10", "x := 11", 1)
	want = strings.Replace(want, "x := 10", "x := 11", 1)
	if got := Merge3(base, left, edited, "go"); got.Conflicts != 0 || got.Text != want {
		t.Fatalf("merge with two body edits = %+v, want %q", got, want)
	}
}

func TestMergeConflict(t *testing.T) {
	base := pkg + foo + bar + baz
	left := strings.Replace(pkg+bar+baz+foo, "x := 10", "x := 11", 1)
	right := strings.Replace(base, "x := 10", "x := 12", 1)
	want := strings.Replace(left, "\tx := 11\n",
		"<<<<<<< left\n\tx := 11\n=======\n\tx := 12\n>>>>>>> right\n", 1)
	got := Merge3(base, left, right, "go")
	if got.Conflicts != 1 || got.Text != want {
		t.Fatalf("merge = %+v, want %q", got, want)
	}
}

func TestMergeIndependentAdjacentAndInlineEdits(t *testing.T) {
	base := pkg + "func f() int {\n\ta := add(1, 2)\n\tb := 3\n\treturn a + b\n}\n"
	left := strings.Replace(base, "add(1, 2)\n\tb := 3", "add(10, 2)\n\tb := 30", 1)
	right := strings.Replace(base, "add(1, 2)", "add(1, 20)", 1)
	want := strings.Replace(left, "add(10, 2)", "add(10, 20)", 1)
	if got := Merge3(base, left, right, "go"); got.Conflicts != 0 || got.Text != want {
		t.Fatalf("independent edits = %+v, want %q", got, want)
	}
}

func TestCLIJSON(t *testing.T) {
	dir := t.TempDir()
	names := []string{"base.go", "left.go", "right.go", "out.go"}
	for i := range names {
		names[i] = filepath.Join(dir, names[i])
	}
	base, left := pkg+foo+bar+baz, pkg+bar+baz+foo
	right := strings.Replace(base, "x := 10", "x := 12", 1)
	for i, text := range []string{base, left, right} {
		if err := os.WriteFile(names[i], []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	args := []string{"diff", names[0], names[1], "--json", "--mode", "ast"}
	var diff DiffResult
	if code := run(args, &stdout, &stderr, true); code != 0 ||
		json.Unmarshal(stdout.Bytes(), &diff) != nil || len(diff.Moves) != 1 || diff.LinesSaved <= 0 {
		t.Fatalf("diff: code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
	stdout.Reset()
	args = []string{"merge", "--json", names[0], names[1], names[2], "--out", names[3]}
	var merge MergeResult
	if code := run(args, &stdout, &stderr, false); code != 0 ||
		json.Unmarshal(stdout.Bytes(), &merge) != nil || merge.Conflicts != 0 ||
		!strings.Contains(merge.Text, "x := 12") {
		t.Fatalf("merge: code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
	written, err := os.ReadFile(names[3])
	if err != nil || string(written) != merge.Text {
		t.Fatalf("output file: %q, %v", written, err)
	}
	if err := os.WriteFile(names[1], []byte(strings.Replace(left, "x := 10", "x := 11", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := run(args, &stdout, &stderr, false); code != 1 ||
		json.Unmarshal(stdout.Bytes(), &merge) != nil || merge.Conflicts != 1 {
		t.Fatalf("conflict: code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
}

func TestMyersShortestScript(t *testing.T) {
	cases := [][]string{nil}
	for n, from := 1, 0; n <= 6; n++ {
		to := len(cases)
		for _, prefix := range cases[from:to] {
			for _, word := range []string{"A", "B"} {
				cases = append(cases, append(append([]string(nil), prefix...), word))
			}
		}
		from = to
	}
	for _, a := range cases {
		for _, b := range cases {
			dp := make([]int, len(b)+1)
			for j := range dp {
				dp[j] = j
			}
			for i, word := range a {
				prev := dp[0]
				dp[0] = i + 1
				for j, other := range b {
					next := 1 + min(dp[j], dp[j+1])
					if word == other {
						next = prev
					}
					prev, dp[j+1] = dp[j+1], next
				}
			}
			x, y, cost := 0, 0, 0
			for _, s := range myers(a, b) {
				if s.a != x || s.b != y {
					t.Fatalf("noncontiguous script for %v -> %v at %+v", a, b, s)
				}
				switch s.op {
				case '=':
					if a[x] != b[y] {
						t.Fatalf("unequal match for %v -> %v at %+v", a, b, s)
					}
					x, y = x+1, y+1
				case '-':
					x, cost = x+1, cost+1
				case '+':
					y, cost = y+1, cost+1
				}
			}
			if x != len(a) || y != len(b) || cost != dp[len(b)] {
				t.Fatalf("%v -> %v costs %d, want %d", a, b, cost, dp[len(b)])
			}
		}
	}
}
