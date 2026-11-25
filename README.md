# cleave v0.1.0

Cleave is a move-aware, single-file diff and three-way merge CLI. It builds
with Go 1.22 or later and uses only the standard library.

## Build

```sh
CGO_ENABLED=0 go build -ldflags="-s -w" -o cleave .
./cleave version
go test ./...
```

Pass `-X main.version=v0.1.0` inside the linker flags to set the reported version.

## Diff

```sh
./cleave diff old.go new.go
./cleave diff old.ts new.ts --mode ast --json
./cleave diff old.txt new.txt --mode line --no-color
```

Structural mode pairs declarations by kind, name, and receiver or scope.
Normalized token hashes match first. Remaining candidates are ranked by
the average of multiset Jaccard similarity and the normalized token LCS.
A score of 0.65 or higher qualifies. Declaration order and unchanged lines
between declarations identify moves. A move prints its old and new line
spans and its inner edits, followed by any edits elsewhere in the file.

```text
MOVED fn foo (L3-L6 -> L11-L14, 90% match)
  - L4     x := 10
  + L12    x := 11
naive_churn=10 structural_churn=2 lines_saved=8
```

Churn counts inserted and deleted lines. Naive churn uses a shortest Myers
line edit script. Structural churn adds the inner edits to the residual line
edits after accounting for moves and their adjoining blank separators.
Lines saved is naive churn minus structural churn. JSON includes the mode
used, moves, residual edits, and all three counts. Edit records use
one-based line numbers and retain line terminators in their text.

## Merge

```sh
./cleave merge base.go left.go right.go --out merged.go
./cleave merge base.ts left.ts right.ts --json
```

Supply the common ancestor, left branch, and right branch in that order.
If one side moves a declaration and the other edits it, the merged result
places the edits at the new location. With no output path, merged text goes
to stdout. JSON returns `text` and `conflicts`. With both flags, the file receives
merged text and stdout receives JSON.

Exit status is 0 for a clean merge or a successful diff, 1 for a merge
with unresolved conflicts, and 2 for usage or I/O errors. An unresolved
merge includes standard left and right conflict markers. If both branches
choose incompatible declaration placements, the marked region can include
the containing scope.

## Language coverage

Go declarations are read with `go/parser`. JavaScript, TypeScript, Rust,
Python, C, and C++ use lexical recognition for common declarations and
methods, including indented Python suites and brace-delimited bodies.
Lexical recognition is heuristic for language-specific syntax such as
macros, computed method names, and complex declarators. Shared-line
declarations use line-level comparison. Unsupported or mismatched
extensions and malformed Go inputs fall back to line-level comparison.

## Release

Pushing a `v*` tag builds `darwin-arm64`, `linux-x86_64`, and `linux-arm64`
binaries with CGO disabled. The workflow checks the 5 MB limit for each
binary and publishes the three binaries and `SHA256SUMS` to the tagged release.
