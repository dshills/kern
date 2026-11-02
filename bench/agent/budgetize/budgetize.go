// Package budgetize derives the budgeted agent suite from suite 4
// (specs/declared-budgets §5): every function of every start and
// reference file gets `budget { bounds_checks 0 }`, so a plain-text agent
// is told by `kern check` every runtime-checked bounds check it must
// remove.
package budgetize

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/format"
	"github.com/dshills/kern/compiler/lexer"
	"github.com/dshills/kern/compiler/parser"
	"github.com/dshills/kern/compiler/source"
)

// Block is the budget every function of the budgeted suite declares.
const Block = "budget { bounds_checks 0 }"

// Sentence is appended to every task.md of the budgeted suite.
const Sentence = "The program declares its bounds-check budgets; `check` reports any function that misses one."

// SuiteName is the only destination directory name Suite writes.
const SuiteName = "tasks-budgeted"

// Source returns src with Block on its own line before the `func` keyword
// of every function declaration that has no budget block (top level and in
// impl, after any contract; extern functions take none), formatted.
func Source(name string, src []byte) ([]byte, error) {
	f, ds := parser.Parse(&source.File{Name: name, Bytes: src})
	if len(ds) > 0 {
		return nil, fmt.Errorf("budgetize %s: %s", name, ds[0].Message)
	}
	var decls []*ast.FuncDecl
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Extern == "" {
				decls = append(decls, d)
			}
		case *ast.ImplDecl:
			decls = append(decls, d.Funcs...)
		}
	}
	toks, _ := lexer.Lex(src)
	var at []int // offsets of the func keywords to prefix, ascending
	for _, d := range decls {
		if d.Budget != nil {
			continue
		}
		for _, t := range toks {
			if t.Kind == lexer.KwFunc && t.Start >= d.Start {
				at = append(at, t.Start)
				break
			}
		}
	}
	var b bytes.Buffer
	prev := 0
	for _, off := range at {
		b.Write(src[prev:off])
		b.WriteString(Block + "\n")
		prev = off
	}
	b.Write(src[prev:])
	out, ds := format.Format(&source.File{Name: name, Bytes: b.Bytes()})
	if len(ds) > 0 {
		return nil, fmt.Errorf("budgetize %s: formatting: %s", name, ds[0].Message)
	}
	return out, nil
}

// Suite writes the budgeted copy of the suite in from to to: Source on
// every .kern file under each task's start/ and reference/ (subdirectories
// included), Sentence appended
// to each task.md, every other file copied byte for byte. It refuses,
// before writing anything, a destination equal to, inside or containing
// the source, one not named SuiteName, one that is a symbolic link, and
// one with a .new or .old sibling left by a failed run. It generates into
// to.new and then replaces to; a generation error leaves to untouched.
func Suite(from, to string) error {
	src, dst, err := paths(from, to)
	if err != nil {
		return err
	}
	tmp, old := dst+".new", dst+".old"
	if _, err := os.Lstat(old); err == nil {
		return fmt.Errorf("budgetize: %s exists, left by a failed run: remove it", old)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("budgetize: stat %s: %w", old, err)
	}
	// creating the staging directory reserves it: a concurrent run, or one
	// that failed, makes this fail rather than share or remove its files
	if err := os.Mkdir(tmp, 0o755); errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("budgetize: %s exists, left by a failed or running run: remove it", tmp)
	} else if err != nil {
		return fmt.Errorf("budgetize: creating %s: %w", tmp, err)
	}
	if err := generate(src, tmp); err != nil {
		if rerr := os.RemoveAll(tmp); rerr != nil {
			return errors.Join(err, fmt.Errorf("budgetize: removing %s: %w", tmp, rerr))
		}
		return err
	}
	_, statErr := os.Lstat(dst)
	exists := statErr == nil
	if exists {
		if err := os.Rename(dst, old); err != nil {
			_ = os.RemoveAll(tmp)
			return fmt.Errorf("budgetize: rename %s to %s: %w", dst, old, err)
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		err = fmt.Errorf("budgetize: rename %s to %s: %w", tmp, dst, err)
		if exists {
			if rerr := os.Rename(old, dst); rerr != nil {
				return errors.Join(err, fmt.Errorf("budgetize: restoring %s from %s: %w", dst, old, rerr))
			}
		}
		return err
	}
	if exists {
		if err := os.RemoveAll(old); err != nil {
			return fmt.Errorf("budgetize: removing %s: %w", old, err)
		}
	}
	return nil
}

// paths canonicalizes from and to and applies Suite's refusals.
func paths(from, to string) (string, string, error) {
	src, err := filepath.Abs(from)
	if err == nil {
		src, err = filepath.EvalSymlinks(src)
	}
	if err != nil {
		return "", "", fmt.Errorf("budgetize: source %s: %w", from, err)
	}
	abs, err := filepath.Abs(to)
	if err != nil {
		return "", "", fmt.Errorf("budgetize: destination %s: %w", to, err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", "", fmt.Errorf("budgetize: destination parent of %s: %w", to, err)
	}
	dst := filepath.Join(parent, filepath.Base(abs))
	if filepath.Base(dst) != SuiteName {
		return "", "", fmt.Errorf("budgetize: destination %s is not named %s", to, SuiteName)
	}
	if st, err := os.Lstat(dst); err == nil && st.Mode()&fs.ModeSymlink != 0 {
		return "", "", fmt.Errorf("budgetize: destination %s is a symbolic link", to)
	}
	if within(src, dst) || within(dst, src) {
		return "", "", fmt.Errorf("budgetize: source %s and destination %s overlap", src, dst)
	}
	return src, dst, nil
}

// within reports whether p is base or inside it.
func within(base, p string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// generate writes the budgeted copy of src into dst, an empty directory.
func generate(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("budgetize: reading %s: %w", p, err)
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return fmt.Errorf("budgetize: %s: %w", p, err)
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return fmt.Errorf("budgetize: creating %s: %w", out, err)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("budgetize: %s is not a regular file", p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("budgetize: reading %s: %w", p, err)
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		switch {
		case len(parts) >= 3 && (parts[1] == "start" || parts[1] == "reference") && strings.HasSuffix(parts[len(parts)-1], ".kern"):
			if b, err = Source(p, b); err != nil {
				return err
			}
		case len(parts) == 2 && parts[1] == "task.md":
			b = append(bytes.TrimRight(b, "\n"), []byte("\n\n"+Sentence+"\n")...)
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			return fmt.Errorf("budgetize: writing %s: %w", out, err)
		}
		return nil
	})
}
