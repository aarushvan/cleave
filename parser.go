package main

import (
	"crypto/sha256"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Line spans are one-based and inclusive. Byte offsets are end-exclusive.
type Block struct {
	Kind, Name, Receiver, Signature string
	StartLine, EndLine, Start, End  int
	Tokens                          []string
	Hash                            [32]byte
	Children                        []Block
	raw                             string
}

func language(path string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
}

func supported(lang string) bool {
	switch lang {
	case "go", "js", "ts", "rs", "py", "c", "cpp":
		return true
	}
	return false
}

type source struct {
	text  string
	lines []int
}

func newSource(text string) source {
	s := source{text: text, lines: []int{0}}
	for i := range len(text) {
		if text[i] == '\n' {
			s.lines = append(s.lines, i+1)
		}
	}
	return s
}

func (s source) line(offset int) int {
	return sort.SearchInts(s.lines, offset+1)
}

func (s source) block(kind, name, receiver, lang string, start, head, end int) Block {
	body := s.text[start:end]
	var tokens []lexeme
	if lang == "go" {
		tokens = goTokens(body)
	} else {
		tokens = lex(body, lang)
	}
	words := make([]string, 0, len(tokens))
	for _, t := range tokens {
		words = append(words, t.word)
	}
	return Block{Kind: kind, Name: name, Receiver: receiver,
		Signature: strings.TrimSpace(s.text[head:end]),
		Start:     start, End: end, StartLine: s.line(start), EndLine: s.line(end - 1),
		Tokens: words, Hash: sha256.Sum256([]byte(strings.Join(words, "\x00"))), raw: body}
}

func ParseBlocks(text, lang string) ([]Block, error) {
	if lang == "go" {
		return parseGo(text)
	}
	s := newSource(text)
	ts := lex(text, lang)
	switch lang {
	case "js", "ts", "rs", "c", "cpp":
		return s.braces(ts, delimiters(ts), lang, "", false, 0, len(ts)), nil
	case "py":
		return s.python(ts), nil
	}
	return nil, nil
}

func parseGo(text string) ([]Block, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", text, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	s := newSource(text)
	offset := func(p token.Pos) int { return fset.PositionFor(p, false).Offset }
	// Older Go parsers may put a leading line directive in a separate comment
	// group. Keep adjacent directives with their declaration on every Go version.
	directives := make(map[int]int)
	for _, group := range file.Comments {
		for _, comment := range group.List {
			start := offset(comment.Pos())
			line := s.line(start)
			if strings.HasPrefix(comment.Text, "//line ") && start == s.lines[line-1] {
				directives[line] = start
			}
		}
	}
	declarationStart := func(pos token.Pos, doc *ast.CommentGroup) int {
		if doc != nil {
			pos = doc.Pos()
		}
		start := offset(pos)
		for line := s.line(start) - 1; line > 0; line-- {
			directive, ok := directives[line]
			if !ok {
				break
			}
			start = directive
		}
		return start
	}
	var out []Block
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			start, head, stop := declarationStart(d.Pos(), d.Doc), offset(d.Pos()), offset(d.End())
			signatureEnd := stop
			if d.Body != nil {
				signatureEnd = offset(d.Body.Pos())
			}
			kind, receiver := "fn", ""
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind, receiver = "method", receiverName(d.Recv.List[0].Type, text, offset)
			}
			b := s.block(kind, d.Name.Name, receiver, "go", start, head, stop)
			b.Signature = strings.TrimSpace(text[head:signatureEnd])
			out = append(out, b)
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			start, head, stop := declarationStart(d.Pos(), d.Doc), offset(d.Pos()), offset(d.End())
			var names []string
			for _, spec := range d.Specs {
				names = append(names, spec.(*ast.TypeSpec).Name.Name)
			}
			b := s.block("type", strings.Join(names, ", "), "", "go", start, head, stop)
			if len(d.Specs) == 1 && !d.Lparen.IsValid() {
				var brace token.Pos
				switch t := d.Specs[0].(*ast.TypeSpec).Type.(type) {
				case *ast.StructType:
					brace = t.Fields.Opening
				case *ast.InterfaceType:
					brace = t.Methods.Opening
				}
				if brace.IsValid() {
					b.Signature = strings.TrimSpace(text[head:offset(brace)])
				}
			}
			out = append(out, b)
		}
	}
	return out, nil
}

func receiverName(x ast.Expr, text string, offset func(token.Pos) int) string {
	for {
		switch t := x.(type) {
		case *ast.StarExpr:
			x = t.X
		case *ast.IndexExpr:
			x = t.X
		case *ast.IndexListExpr:
			x = t.X
		case *ast.ParenExpr:
			x = t.X
		default:
			return text[offset(x.Pos()):offset(x.End())]
		}
	}
}

type lexeme struct {
	word       string
	start, end int
}

func goTokens(text string) []lexeme {
	f := token.NewFileSet().AddFile("", -1, len(text))
	var scan scanner.Scanner
	scan.Init(f, []byte(text), nil, 0)
	var out []lexeme
	for {
		pos, kind, word := scan.Scan()
		if kind == token.EOF {
			return out
		}
		if kind == token.SEMICOLON || word == "" {
			word = kind.String()
		}
		out = append(out, lexeme{word: word, start: f.Offset(pos)})
	}
}

func identifier(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return r == '_' || r == '$' || unicode.IsLetter(r)
}

func lex(s, lang string) []lexeme {
	var out []lexeme
	for i := 0; i < len(s); {
		start := i
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case unicode.IsSpace(r):
			continue
		case (lang == "c" || lang == "cpp") && r == '#' &&
			strings.TrimSpace(s[strings.LastIndexByte(s[:start], '\n')+1:start]) == "":
			for i < len(s) {
				if s[i] == '\n' && !strings.HasSuffix(strings.TrimSuffix(s[start:i], "\r"), "\\") {
					break
				}
				i++
			}
			continue
		case lang == "py" && r == '#', lang != "py" && strings.HasPrefix(s[start:], "//"):
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		case lang != "py" && strings.HasPrefix(s[start:], "/*"):
			i++
			depth := 1
			for i < len(s) && depth > 0 {
				switch {
				case strings.HasPrefix(s[i:], "*/"):
					depth--
					i += 2
				case lang == "rs" && strings.HasPrefix(s[i:], "/*"):
					depth++
					i += 2
				default:
					i++
				}
			}
			continue
		case lang == "rs" && r == 'r' && strings.HasPrefix(strings.TrimLeft(s[i:], "#"), "\""):
			rest := strings.TrimLeft(s[i:], "#")
			hashes := s[i : len(s)-len(rest)]
			i = through(s, i+len(hashes)+1, "\""+hashes)
		case lang == "cpp" && r == 'R' && i < len(s) && s[i] == '"':
			if n := strings.IndexByte(s[i+1:], '('); n >= 0 && n <= 16 {
				i = through(s, i+n+2, ")"+s[i+1:i+1+n]+"\"")
			}
		case r == '"' || r == '\'' || r == 0x60:
			if lang == "rs" && r == '\'' && identifier(s[i:]) {
				j := wordEnd(s, i)
				if j == len(s) || s[j] != '\'' {
					break // Lifetime or label.
				}
			}
			quote := s[start:i]
			if lang == "py" && strings.HasPrefix(s[i:], quote+quote) {
				quote += quote + quote
				i += 2
			}
			for i < len(s) {
				if strings.HasPrefix(s[i:], quote) {
					i += len(quote)
					break
				}
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				i++
			}
		case r == '/' && (lang == "js" || lang == "ts") && regexStart(out):
			inClass := false
			for i < len(s) && s[i] != '\n' {
				c := s[i]
				i++
				if c == '\\' && i < len(s) {
					i++
				} else if c == '[' {
					inClass = true
				} else if c == ']' {
					inClass = false
				} else if c == '/' && !inClass {
					i = wordEnd(s, i)
					break
				}
			}
		case identifier(s[start:]), unicode.IsDigit(r):
			i = wordEnd(s, i)
		case r == '=' && i < len(s) && s[i] == '>', r == ':' && i < len(s) && s[i] == ':':
			i++
		}
		out = append(out, lexeme{s[start:i], start, i})
	}
	return out
}

func through(s string, start int, end string) int {
	if i := strings.Index(s[start:], end); i >= 0 {
		return start + i + len(end)
	}
	return len(s)
}

func wordEnd(s string, i int) int {
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !identifier(s[i:]) && !unicode.IsDigit(r) && !unicode.IsMark(r) {
			break
		}
		i += size
	}
	return i
}

func regexStart(ts []lexeme) bool {
	if len(ts) == 0 {
		return true
	}
	switch ts[len(ts)-1].word {
	case "=", "(", "[", ",", ":", "return", "=>", "!", "?", "throw", ";":
		return true
	}
	return false
}

func delimiters(ts []lexeme) []int {
	pairs := make([]int, len(ts))
	for i := range pairs {
		pairs[i] = -1
	}
	var stack []int
	for i, t := range ts {
		switch t.word {
		case "(", "[", "{":
			stack = append(stack, i)
		case ")", "]", "}":
			if n := len(stack); n > 0 {
				j := stack[n-1]
				if strings.Index("([{", ts[j].word) == strings.Index(")]}", t.word) {
					pairs[i], pairs[j] = j, i
					stack = stack[:n-1]
				}
			}
		}
	}
	return pairs
}

func angleEnd(ts []lexeme, start, end int) int {
	depth := 0
	for i := start; i < end; i++ {
		switch ts[i].word {
		case "<":
			depth++
		case ">":
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return start
}

func declaration(ts []lexeme, pairs []int, start, end int, member bool, lang string) (string, string) {
	for i := start; i < end; i++ {
		w := ts[i].word
		if (w == "(" || w == "[") && pairs[i] > i {
			i = pairs[i]
			continue
		}
		if w == "template" && i+1 < end && ts[i+1].word == "<" {
			i = angleEnd(ts, i+1, end) - 1
			continue
		}
		if (w == "function" || w == "fn") && i+1 < end {
			j := i + 1
			if ts[j].word == "*" {
				j++
			}
			if j < end && identifier(ts[j].word) {
				if member {
					return "method", ts[j].word
				}
				return "fn", ts[j].word
			}
		}
		switch w {
		case "class", "struct", "interface", "enum", "union", "namespace", "trait", "impl", "mod":
			j := i + 1
			if w == "impl" && j < end && ts[j].word == "<" {
				j = angleEnd(ts, j, end)
			}
			if j < end && identifier(ts[j].word) &&
				(j+1 == end || ts[j+1].word != "(") &&
				(j+2 >= end || ts[j+2].word != "(" || !identifier(ts[j+1].word)) {
				name := ts[j].word
				if w == "impl" {
					for j++; j+1 < end; j++ {
						if ts[j].word == "for" && identifier(ts[j+1].word) {
							name += " for " + ts[j+1].word
							break
						}
					}
				}
				return w, name
			}
		case "=>":
			for j := start; j+1 < i; j++ {
				switch ts[j].word {
				case "const", "let", "var":
					if identifier(ts[j+1].word) {
						return "fn", ts[j+1].word
					}
				}
			}
			if member && identifier(ts[start].word) {
				return "method", ts[start].word
			}
		}
	}
	if lang == "rs" || (!member && (lang == "js" || lang == "ts")) {
		return "", ""
	}
	for i := start; i < end; i++ {
		if ts[i].word != "(" || pairs[i] < i || i == start {
			continue
		}
		j := i - 1
		if ts[j].word == ">" {
			depth := 1
			for k := j - 1; k > start; k-- {
				switch ts[k].word {
				case ">":
					depth++
				case "<":
					depth--
				}
				if depth == 0 {
					j = k - 1
					break
				}
			}
		}
		name := ts[j].word
		if !identifier(name) {
			continue
		}
		switch name {
		case "if", "for", "while", "switch", "catch", "sizeof", "alignas", "noexcept", "requires":
			i = pairs[i]
			continue
		}
		if j > start && ts[j-1].word == "~" {
			j--
			name = "~" + name
		}
		for j >= start+2 && ts[j-1].word == "::" && identifier(ts[j-2].word) {
			name = ts[j-2].word + "::" + name
			j -= 2
		}
		if member || strings.Contains(name, "::") {
			return "method", name
		}
		return "fn", name
	}
	return "", ""
}

func (s source) braces(ts []lexeme, pairs []int, lang, scope string, member bool, from, to int) []Block {
	var out []Block
	start := from
	for i := from; i < to; i++ {
		switch ts[i].word {
		case "(", "[":
			if pairs[i] > i {
				i = pairs[i]
			}
		case ";", "}":
			start = i + 1
		case "{":
			close := pairs[i]
			if close < i || close >= to {
				continue
			}
			if i == start {
				i, start = close, close+1
				continue
			}
			// An object return type can precede the TypeScript function body.
			if lang == "ts" && close+1 < to &&
				(ts[close+1].word == "{" || ts[close+1].word == "=>") {
				i = close
				continue
			}
			for start+1 < i && ts[start+1].word == ":" && lang == "cpp" {
				start += 2 // Access label.
			}
			kind, name := declaration(ts, pairs, start, i, member, lang)
			if kind != "" {
				last := close
				if last+1 < to && ts[last+1].word == ";" {
					last++
				}
				b := s.block(kind, name, scope, lang, ts[start].start, ts[start].start, ts[last].end)
				b.Signature = strings.TrimSpace(s.text[b.Start:ts[i].start])
				if kind != "fn" && kind != "method" {
					childScope := name
					if scope != "" {
						childScope = scope + "." + name
					}
					members := kind != "namespace" && kind != "mod"
					b.Children = s.braces(ts, pairs, lang, childScope, members, i+1, close)
				}
				out = append(out, b)
				close = last
			}
			i, start = close, close+1
		}
		// Common declaration boundaries with automatic semicolon insertion.
		if (lang == "js" || lang == "ts") && i+1 < to && start <= i &&
			s.line(ts[i].end-1) < s.line(ts[i+1].start) {
			switch ts[i].word {
			case "export", "default", "async", "declare", "abstract", "=":
				continue
			}
			switch ts[i+1].word {
			case "export", "import", "const", "let", "var", "function", "class", "interface":
				start = i + 1
			}
		}
	}
	return out
}

func indentation(s string) int {
	n := 0
	for _, r := range s {
		if r == '\t' {
			n += 8 - n%8
		} else if r == ' ' {
			n++
		}
	}
	return n
}

func (s source) python(ts []lexeme) []Block {
	pairs := delimiters(ts)
	var statements []int
	for i := 0; i < len(ts); i++ {
		if i == 0 || (ts[i-1].word != "\\" && s.line(ts[i-1].end-1) < s.line(ts[i].start)) {
			statements = append(statements, i)
		}
		if strings.Contains("([{", ts[i].word) && pairs[i] > i {
			i = pairs[i]
		}
	}
	statements = append(statements, len(ts))
	indent := func(stmt int) int {
		t := ts[statements[stmt]]
		return indentation(s.text[s.lines[s.line(t.start)-1]:t.start])
	}
	var walk func(int, int, string) []Block
	walk = func(from, to int, scope string) []Block {
		var out []Block
		decorator := -1
		for i := from; i < to; i++ {
			begin, head := statements[i], statements[i]
			if ts[head].word == "@" {
				if decorator < 0 {
					decorator = i
				}
				continue
			}
			if ts[head].word == "async" {
				head++
			}
			if head+1 >= statements[i+1] ||
				(ts[head].word != "def" && ts[head].word != "class") {
				decorator = -1
				continue
			}
			if decorator >= 0 && indent(decorator) == indent(i) {
				begin = statements[decorator]
			}
			decorator = -1
			j := i + 1
			for j < to && indent(j) > indent(i) {
				j++
			}
			kind, name := "fn", ts[head+1].word
			if ts[head].word == "class" {
				kind = "class"
			} else if scope != "" {
				kind = "method"
			}
			end := ts[statements[j]-1].end
			b := s.block(kind, name, scope, "py", ts[begin].start, ts[head].start, end)
			for k := head + 2; k < statements[i+1]; k++ {
				if ts[k].word == ":" {
					b.Signature = strings.TrimSpace(s.text[ts[head].start:ts[k].end])
					break
				}
				if pairs[k] > k {
					k = pairs[k]
				}
			}
			if kind == "class" {
				childScope := name
				if scope != "" {
					childScope = scope + "." + name
				}
				b.Children = walk(i+1, j, childScope)
			}
			out = append(out, b)
			i = j - 1
		}
		return out
	}
	return walk(0, len(statements)-1, "")
}
