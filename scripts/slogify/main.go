// Command slogify rewrites the daemon's log.Logger calls to log/slog calls.
//
// It is a source-to-source codemod, safe to run again on a newer tree: it
// only touches calls it recognises, and a converted tree is a fixed point.
// See README.md in this directory.
package main

import (
	"bytes"
	"flag"
	"flodni/internal/flog"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// skipDirs are never rewritten: the vendored engine (hard rule 4), the
// GoStorm fork (it logs through its own TLogln), and the logging package.
var skipDirs = []string{"internal/anacrolix-torrent", "internal/gostorm", "internal/flog"}

// recvRE matches the expressions that are a *log.Logger in this tree.
var recvRE = regexp.MustCompile(`^(logger|evictLog|logf|(\w+\.)*(logger|Logger))$`)

var levelMethod = map[string]string{"DEBUG": "Debug", "INFO": "Info", "WARN": "Warn", "ERROR": "Error"}

type edit struct {
	start, end int
	text       string
}

type report struct {
	files, calls, dynamic int
	manual                []string
}

func main() {
	dry := flag.Bool("n", false, "dry run: print what would change, write nothing")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: slogify [-n] <repo root>")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	rep, err := run(flag.Arg(0), *dry)
	if err != nil {
		fmt.Fprintln(os.Stderr, "slogify:", err)
		os.Exit(1)
	}
	fmt.Printf("slogify: %d files changed, %d calls converted (%d with a runtime-classified level)\n", rep.files, rep.calls, rep.dynamic)
	for _, m := range rep.manual {
		fmt.Println("MANUAL:", m)
	}
}

func run(root string, dry bool) (report, error) {
	var rep report
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				for _, s := range skipDirs {
					if rel == s {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") {
				return nil
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out, n, dyn, manual, err := convert(rel, src)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			rep.manual = append(rep.manual, manual...)
			rep.calls += n
			rep.dynamic += dyn
			if bytes.Equal(out, src) {
				return nil
			}
			rep.files++
			if dry {
				fmt.Println("would change", rel)
				return nil
			}
			return os.WriteFile(p, out, 0o644)
		})
		if err != nil {
			return rep, err
		}
	}
	sort.Strings(rep.manual)
	return rep, nil
}

// convert rewrites one file. It returns the new source, how many calls it
// converted, how many of them take their level from the rendered text, and
// the places a person has to finish.
func convert(name string, src []byte) (out []byte, calls, dyn int, manual []string, err error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
	if err != nil {
		return nil, 0, 0, nil, err
	}
	stdLog := importName(f, "log") // local name of the standard "log" package, "" if absent
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	text := func(n ast.Node) string { return string(src[off(n.Pos()):off(n.End())]) }
	where := func(n ast.Node) string { return fmt.Sprintf("%s:%d", name, fset.Position(n.Pos()).Line) }

	// recv returns the replacement text of a logger expression, or ok=false.
	recv := func(x ast.Expr) (string, bool) {
		t := text(x)
		if id, isID := x.(*ast.Ident); isID && stdLog != "" && id.Name == stdLog {
			return "flog.Std", true
		}
		if recvRE.MatchString(t) {
			return t, true
		}
		return "", false
	}

	var edits []edit
	callee := map[ast.Node]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			callee[c.Fun] = true
		}
		return true
	})

	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.InterfaceType:
			for _, m := range n.Methods.List {
				for _, id := range m.Names {
					if id.Name == "Printf" {
						manual = append(manual, where(m)+": interface with a Printf method; give it a *slog.Logger by hand")
					}
				}
			}
		case *ast.StarExpr:
			if sel, ok := n.X.(*ast.SelectorExpr); ok && isPkg(sel.X, stdLog) && sel.Sel.Name == "Logger" {
				edits = append(edits, edit{off(sel.Pos()), off(sel.End()), "slog.Logger"})
			}
		case *ast.SelectorExpr:
			// a method value such as logf: logger.Printf
			if !callee[n] && n.Sel.Name == "Printf" {
				if r, ok := recv(n.X); ok {
					edits = append(edits, edit{off(n.Pos()), off(n.End()), "flog.Logf(" + r + ")"})
				}
			}
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if isPkg(sel.X, stdLog) {
				switch sel.Sel.Name {
				case "New":
					if t, ok := newLogger(n, text); ok {
						edits = append(edits, edit{off(n.Pos()), off(n.End()), t})
						return true
					}
					manual = append(manual, where(n)+": log.New with arguments the tool does not recognise")
					return true
				case "Default":
					edits = append(edits, edit{off(n.Pos()), off(n.End()), "flog.Std"})
					return true
				case "Fatal", "Fatalf", "Fatalln", "Panic", "Panicf", "Panicln":
					manual = append(manual, where(n)+": log."+sel.Sel.Name+" exits or panics; slog has no equivalent")
					return true
				}
			}
			r, isLogger := recv(sel.X)
			if !isLogger {
				return true
			}
			switch sel.Sel.Name {
			case "Printf", "Println", "Print":
				t, d, ok := call(n, sel.Sel.Name, r, text)
				if !ok {
					manual = append(manual, where(n)+": "+sel.Sel.Name+" with arguments the tool does not recognise")
					return true
				}
				calls++
				if d {
					dyn++
				}
				edits = append(edits, edit{off(n.Pos()), off(n.End()), t})
			case "SetOutput", "SetPrefix", "SetFlags", "Writer", "Prefix", "Flags", "Output", "Fatal", "Fatalf", "Fatalln", "Panic", "Panicf", "Panicln":
				if r != "flog.Std" {
					manual = append(manual, where(n)+": ."+sel.Sel.Name+" has no *slog.Logger equivalent")
				}
			}
		}
		return true
	})

	if len(edits) == 0 {
		return src, 0, 0, manual, nil
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	buf := append([]byte(nil), src...)
	last := len(src) + 1
	nested := false
	for _, e := range edits {
		if e.end > last { // contains an edit already applied: redo it on the next pass
			nested = true
			continue
		}
		buf = append(buf[:e.start], append([]byte(e.text), buf[e.end:]...)...)
		last = e.start
	}
	buf, err = fixImports(name, buf)
	if err != nil {
		return nil, 0, 0, nil, err
	}
	if nested {
		// a call inside the arguments of another converted call: the outer
		// call was skipped, convert it now that the inner one is done
		more, c2, d2, _, err := convert(name, buf)
		if err != nil {
			return nil, 0, 0, nil, err
		}
		return more, calls + c2, dyn + d2, manual, nil
	}
	return buf, calls, dyn, manual, nil
}

func isPkg(x ast.Expr, name string) bool {
	id, ok := x.(*ast.Ident)
	return ok && name != "" && id.Name == name && id.Obj == nil
}

// importName returns the local name the file gives the standard library
// package path, or "" if it does not import it.
func importName(f *ast.File, path string) string {
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		if p != path {
			continue
		}
		if im.Name != nil {
			return im.Name.Name
		}
		return filepath.Base(path)
	}
	return ""
}

// newLogger converts log.New(w, prefix, flags).
func newLogger(c *ast.CallExpr, text func(ast.Node) string) (string, bool) {
	if len(c.Args) != 3 {
		return "", false
	}
	prefix, ok := stringLit(c.Args[1])
	if !ok {
		return "", false
	}
	w, flags := text(c.Args[0]), text(c.Args[2])
	name := strings.TrimSpace(strings.Trim(strings.TrimSpace(prefix), "[]"))
	if strings.ContainsAny(name, "[] ") {
		return "", false
	}
	if flags == "0" {
		if name == "" {
			return "flog.NewText(" + w + ")", true
		}
		return fmt.Sprintf("flog.New(%s, %q, false)", w, name), true
	}
	return fmt.Sprintf("flog.New(%s, %q, true)", w, name), true
}

// stringLit evaluates a string literal or a concatenation of them.
func stringLit(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		l, ok1 := stringLit(e.X)
		r, ok2 := stringLit(e.Y)
		return l + r, ok1 && ok2
	case *ast.ParenExpr:
		return stringLit(e.X)
	}
	return "", false
}

// call builds the slog call for one Printf, Println or Print.
func call(c *ast.CallExpr, method, r string, text func(ast.Node) string) (string, bool, bool) {
	if len(c.Args) == 0 {
		return "", false, false
	}
	first := c.Args[0]
	format, isLit := stringLit(first)
	switch method {
	case "Printf":
		if c.Ellipsis.IsValid() && len(c.Args) < 2 {
			return "", false, false
		}
		if !isLit || c.Ellipsis.IsValid() {
			return "flog.Printf(" + r + ", " + argsText(c, text) + ")", true, true
		}
		var msg string
		if len(c.Args) == 1 && !strings.Contains(format, "%") {
			msg = text(first)
		} else {
			msg = "fmt.Sprintf(" + argsText(c, text) + ")"
		}
		return levelCall(r, flog.Classify(format), msg, fields(format, c.Args[1:], text)), false, true
	case "Println":
		if len(c.Args) == 1 && isLit {
			return levelCall(r, flog.Classify(format), text(first), nil), false, true
		}
		return "", false, false
	case "Print":
		if len(c.Args) != 1 {
			return "", false, false
		}
		if isLit {
			return levelCall(r, flog.Classify(format), text(first), nil), false, true
		}
		if conv, ok := first.(*ast.CallExpr); ok {
			if id, ok := conv.Fun.(*ast.Ident); ok && id.Name == "string" && len(conv.Args) == 1 {
				return r + ".Info(" + text(first) + ")", false, true
			}
		}
		return r + ".Info(fmt.Sprint(" + text(first) + "))", false, true
	}
	return "", false, false
}

// argsText is the source of all arguments, from the first to the closing
// parenthesis, so comments and line breaks between them survive.
func argsText(c *ast.CallExpr, text func(ast.Node) string) string {
	var parts []string
	for _, a := range c.Args {
		parts = append(parts, text(a))
	}
	s := strings.Join(parts, ", ")
	if c.Ellipsis.IsValid() {
		s += "..."
	}
	return s
}

func levelCall(r string, lv fmt.Stringer, msg string, fs []string) string {
	m := levelMethod[strings.ToUpper(lv.String())]
	if len(fs) == 0 {
		return r + "." + m + "(" + msg + ")"
	}
	return r + "." + m + "(" + msg + ", " + strings.Join(fs, ", ") + ")"
}

// fields returns `"key", value` pairs for the arguments that are plain
// variables, so the JSON format carries them as keys. The text format drops
// the ones the message already shows.
func fields(format string, args []ast.Expr, text func(ast.Node) string) []string {
	verbs, ok := verbsOf(format)
	if !ok || len(verbs) != len(args) {
		return nil
	}
	if strings.HasPrefix(format, "%") {
		// the text comes from the arguments (a prefix and a payload, as in the
		// documented JSON lines): the message is the payload, add no fields
		return nil
	}
	seen := map[string]int{}
	var out []string
	for i, a := range args {
		if !strings.ContainsRune("svdq", rune(verbs[i])) {
			continue
		}
		key, ok := keyOf(a)
		if !ok {
			continue
		}
		seen[key]++
		if n := seen[key]; n > 1 {
			key += "_" + strconv.Itoa(n)
		}
		out = append(out, strconv.Quote(key)+", "+text(a))
	}
	return out
}

// verbsOf lists the verb letters that consume an argument. ok is false for
// formats with * widths or explicit argument indexes.
func verbsOf(f string) ([]byte, bool) {
	var vs []byte
	for i := 0; i < len(f); i++ {
		if f[i] != '%' {
			continue
		}
		i++
		for i < len(f) && strings.IndexByte("+-# 0123456789.", f[i]) >= 0 {
			i++
		}
		if i >= len(f) || f[i] == '*' || f[i] == '[' {
			return nil, false
		}
		if f[i] != '%' {
			vs = append(vs, f[i])
		}
	}
	return vs, true
}

// keyOf names a field after a side-effect-free argument: x, a.b, x[:8] and
// filepath.Base(x). Anything else is left in the message only.
func keyOf(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.Ident:
		if e.Name == "nil" || e.Name == "true" || e.Name == "false" || e.Name == "_" {
			return "", false
		}
		return snake(e.Name), true
	case *ast.SelectorExpr:
		if _, ok := keyOf(e.X); !ok {
			return "", false
		}
		return snake(e.Sel.Name), true
	case *ast.SliceExpr:
		if _, ok := keyOf(e.X); !ok {
			return "", false
		}
		return keyOf(e.X)
	case *ast.CallExpr:
		if s, ok := e.Fun.(*ast.SelectorExpr); ok && len(e.Args) == 1 && s.Sel.Name == "Base" {
			if p, ok := s.X.(*ast.Ident); ok && p.Name == "filepath" {
				if _, ok := keyOf(e.Args[0]); ok {
					return "file", true
				}
			}
		}
	}
	return "", false
}

func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// fixImports adds fmt, log/slog and flodni/internal/flog where the new code
// uses them, drops "log" when nothing refers to it any more, and gofmts.
func fixImports(name string, src []byte) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("converted file does not parse: %w", err)
	}
	used := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if s, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := s.X.(*ast.Ident); ok && id.Obj == nil {
				used[id.Name] = true
			}
		}
		return true
	})
	have := map[string]bool{}
	var logSpec *ast.ImportSpec
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		have[p] = true
		if p == "log" && im.Name == nil {
			logSpec = im
		}
	}
	var add []string
	for _, w := range []struct{ pkg, ident string }{{"fmt", "fmt"}, {"log/slog", "slog"}, {"flodni/internal/flog", "flog"}} {
		if used[w.ident] && !have[w.pkg] {
			add = append(add, w.pkg)
		}
	}
	dropLog := logSpec != nil && !used["log"]
	if len(add) == 0 && !dropLog {
		return format.Source(src)
	}
	lines := strings.SplitAfter(string(src), "\n")
	var out []string
	inserted := false
	for i, l := range lines {
		ln := i + 1
		if dropLog && ln == fset.Position(logSpec.Pos()).Line {
			continue
		}
		if !inserted && len(add) > 0 && strings.HasPrefix(l, "import (") {
			out = append(out, l)
			for _, p := range add {
				out = append(out, "\t"+strconv.Quote(p)+"\n")
			}
			inserted = true
			continue
		}
		out = append(out, l)
	}
	res := strings.Join(out, "")
	if len(add) > 0 && !inserted {
		return nil, fmt.Errorf("%s: no import ( block to extend; add %v by hand", name, add)
	}
	return format.Source([]byte(res))
}
