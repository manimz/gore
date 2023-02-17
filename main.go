package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

func main() {
	var root, origin, newPath string
	var mod bool
	flag.StringVar(&root, "root", "", "root directory")
	flag.BoolVar(&mod, "m", false, "mode")
	flag.Parse()
	if (len(flag.Args()) < 2 && !mod) || (len(flag.Args()) < 1 && mod) {
		log.Fatal("not enough arguments")
	}
	if len(root) < 1 {
		var err error
		root, err = os.Getwd()
		if err != nil {
			log.Fatal(err)
		}
	}
	if mod {
		var err error
		origin, newPath, err = collectPaths(flag.Args())
		if err != nil {
			log.Fatal(err)
		}
		if len(origin) < 1 {
			log.Fatal("could not find module path")
		}
		err = modifyMod(newPath)
		if err != nil {
			log.Fatal(err)
		}
	} else {
		origin, newPath = flag.Arg(0), flag.Arg(1)
	}
	fset := token.NewFileSet()
	WalkDir(root, origin, newPath, fset, mod)
}
func collectPaths(args []string) ( // comment
	string, string, error) {
	var list struct {
		Path string `json:"Path"`
	}
	out, err := runGo("list", "-m", "-json")
	if err != nil {
		return "", "", err
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return "", "", err
	}
	origin := list.Path
	return origin, args[0], nil
}
func modifyMod(path string) error {
	_, err := runGo("mod", "edit", "-module", path)
	return err
}

type Walker struct {
	fset            *token.FileSet
	origin, newPath string
	VisitFile       func(*ast.File, *Walker) (*bytes.Buffer, error)
}

func WalkDir(root string, origin, newPath string, fs *token.FileSet, mod bool) error {
	handler := importHandler
	if mod {
		handler = modHandler
	}
	walker := &Walker{fset: fs, origin: origin, newPath: newPath, VisitFile: handler}
	return filepath.WalkDir(root, walker.Visit)
}
func WriteFile(path string, src []byte, info fs.FileInfo) error {
	return os.WriteFile(path, src, info.Mode())
}
func importHandler(file *ast.File, w *Walker) (*bytes.Buffer, error) {
	s := astutil.RewriteImport(w.fset, file, w.origin, w.newPath)
	if !s {
		return nil, nil
	}
	buf := bytes.NewBuffer([]byte{})
	if err := printer.Fprint(buf, w.fset, file); err != nil {
		return buf, err
	}
	return buf, nil
}
func modHandler(file *ast.File, w *Walker) (*bytes.Buffer, error) {
	var wrote bool
	for _, imp := range file.Imports {
		if strings.Contains(imp.Path.Value, w.origin) {
			newImp := strings.Replace(imp.Path.Value, w.origin, w.newPath, 1)
			newImp, _ = strconv.Unquote(newImp)
			imp.Path.Value = strconv.Quote(newImp)
			wrote = true
		}
	}
	buf := bytes.NewBuffer([]byte{})
	if !wrote {
		return buf, nil
	}
	astPrinter := printer.Config{Mode: printer.TabIndent | printer.UseSpaces, Tabwidth: 6}
	if err := astPrinter.Fprint(buf, w.fset, file); err != nil {
		return buf, err
	}
	return buf, nil
}
func (w *Walker) Visit(path string, d fs.DirEntry, _ error) error {
	if d.IsDir() {
		return nil
	}
	if !strings.HasSuffix(path, ".go") {
		return nil
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	src, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	pkg, _ := parser.ParseFile(w.fset, path, src, parser.ParseComments)
	if err != nil {
		log.Println(err)
		return nil
	}
	buf, err := w.VisitFile(pkg, w)
	if err != nil || buf == nil {
		return nil
	}
	if buf.Len() > 0 {
		if err := file.Truncate(int64(buf.Len())); err != nil {
			return err
		}
		if _, err := file.WriteAt(buf.Bytes(), 0); err != nil {
			return err
		}
	}
	err = file.Close()
	return err
}
func packageOff(src []byte) int64 {
	off := 0
	w := 0
	for len(src) > 0 {
		newLine := bytes.IndexByte(src[w:], '\n')
		if newLine < 0 || newLine == len(src)-1 {
			break
		}
		line := src[:newLine]
		line = bytes.TrimSpace(src[:newLine])
		if len(line) < 1 {
			continue
		}
		if bytes.HasPrefix(line, []byte("//")) {
			off = newLine + 1
		}
		if bytes.HasPrefix(line, []byte("/*")) {
			end := bytes.Index(src[w:], []byte("*/"))
			if end < 0 {
				break
			}
			end += 2
			for ; end < len(src)-1; end++ {
				if src[end] != ' ' {
					if src[end] == '\n' {
						end++
					}
					break
				}
			}
			off = end
			break
		}
		if len(line) > 0 {
			break
		}
		w += newLine + 1
	}
	return int64(off)
}
func runGo(args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go run: %q: %v: %s", args, err, out)
	}
	return out, err
}
