package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

var version = "v0.1.1"

const usage = "Usage:\n" +
	"  cleave diff <old> <new> [--mode ast|line] [--json] [--no-color]\n" +
	"  cleave merge <base> <left> <right> [--out <file>] [--json]\n" +
	"  cleave version\n"

func main() {
	info, err := os.Stdout.Stat()
	tty := err == nil && info.Mode()&os.ModeCharDevice != 0 && os.Getenv("TERM") != "dumb"
	_, noColor := os.LookupEnv("NO_COLOR")
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, tty && !noColor))
}

// flag stops at the first positional argument. Partition options so each
// subcommand accepts flags both before and after its input paths.
func parseFlags(fs *flag.FlagSet, args []string) error {
	var options, paths []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			paths = append(paths, args[i+1:]...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			paths = append(paths, arg)
			continue
		}
		options = append(options, arg)
		name, _, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if f := fs.Lookup(name); f != nil && !inline {
			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
				continue
			}
			if i+1 < len(args) {
				i++
				options = append(options, args[i])
			}
		}
	}
	return fs.Parse(append(append(options, "--"), paths...))
}

func run(args []string, stdout, stderr io.Writer, color bool) int {
	fail := func(err error) int {
		fmt.Fprintln(stderr, "cleave:", err)
		return 2
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version":
		if len(args) != 1 {
			return fail(fmt.Errorf("version takes no arguments"))
		}
		if _, err := fmt.Fprintln(stdout, version); err != nil {
			return fail(err)
		}
		return 0
	case "diff", "merge":
	default:
		return fail(fmt.Errorf("unknown subcommand %q", args[0]))
	}

	diff := args[0] == "diff"
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	asJSON := fs.Bool("json", false, "write JSON to stdout")
	mode, noColor, outPath := "ast", false, ""
	want := 3
	if diff {
		want = 2
		fs.StringVar(&mode, "mode", "ast", "diff mode: ast or line")
		fs.BoolVar(&noColor, "no-color", false, "disable ANSI colors")
	} else {
		fs.StringVar(&outPath, "out", "", "write merged text to this file")
	}
	if err := parseFlags(fs, args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	paths := fs.Args()
	if len(paths) != want {
		fs.Usage()
		return 2
	}
	if mode != "ast" && mode != "line" {
		return fail(fmt.Errorf("invalid diff mode %q", mode))
	}
	input := make([]string, want)
	lang := language(paths[0])
	for i, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fail(err)
		}
		input[i] = string(raw)
		if language(path) != lang {
			lang = ""
		}
	}

	if diff {
		result := Diff(input[0], input[1], lang, mode)
		var err error
		if *asJSON {
			err = json.NewEncoder(stdout).Encode(result)
		} else {
			var buf strings.Builder
			renderDiff(&buf, result, color && !noColor)
			_, err = io.WriteString(stdout, buf.String())
		}
		if err != nil {
			return fail(err)
		}
		return 0
	}

	result := Merge3(input[0], input[1], input[2], lang)
	if outPath != "" {
		if err := os.WriteFile(outPath, []byte(result.Text), 0644); err != nil {
			return fail(err)
		}
	}
	var err error
	if *asJSON {
		err = json.NewEncoder(stdout).Encode(result)
	} else if outPath == "" {
		_, err = io.WriteString(stdout, result.Text)
	}
	if err != nil {
		return fail(err)
	}
	if result.Conflicts > 0 {
		return 1
	}
	return 0
}
