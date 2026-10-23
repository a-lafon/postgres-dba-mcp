package main

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
)

const repoRoot = "../.."

// readDoc returns the text of a file of the repository root.
func readDoc(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// section returns the text under the "## <title>" heading, up to the next "## " heading.
func section(t *testing.T, doc, title string) string {
	t.Helper()
	_, rest, ok := strings.Cut(doc, "\n## "+title+"\n")
	if !ok {
		t.Fatalf("no section %q", title)
	}
	body, _, _ := strings.Cut(rest, "\n## ")
	return body
}

func TestDocsJSONSnippetsParse(t *testing.T) {
	fence := regexp.MustCompile("(?s)```json\n(.*?)```")
	for _, name := range []string{"README.md", "MIGRATION.md"} {
		t.Run(name, func(t *testing.T) {
			blocks := fence.FindAllStringSubmatch(readDoc(t, name), -1)
			if len(blocks) == 0 {
				t.Fatal("no json block found")
			}
			for i, b := range blocks {
				if !json.Valid([]byte(b[1])) {
					t.Errorf("json block %d does not parse:\n%s", i+1, b[1])
				}
			}
		})
	}
}

// registeredFlags returns the names of the flags declared on the flag set of config.Parse. The flag set is
// local to Parse, so the declarations are read from its source: fs.XxxVar(&dst, "name", ...).
func registeredFlags(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repoRoot, "internal/config/config.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); !ok || recv.Name != "fs" || !strings.HasSuffix(sel.Sel.Name, "Var") {
			return true
		}
		for _, a := range call.Args {
			if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				name, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				names = append(names, name)
				break
			}
		}
		return true
	})
	if len(names) < 10 {
		t.Fatalf("found only %d flags in config.go: %v; the flag declarations changed shape", len(names), names)
	}
	return names
}

func TestREADMEDocumentsEveryFlag(t *testing.T) {
	flags := section(t, readDoc(t, "README.md"), "Flags")
	for _, name := range registeredFlags(t) {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(flags, "`--"+name+"`") {
				t.Errorf("flag --%s is not in the README flags table", name)
			}
		})
	}
}

// registeredTools returns the tool names the binary lists with every group enabled.
func registeredTools(t *testing.T) []string {
	t.Helper()
	cfg, err := config.Parse([]string{"--tools=all"}, func(k string) string {
		if k == "DATABASE_URI" {
			return "postgres://u@localhost:1/d" // never connected to: the pool is lazy
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	reg := server.New(cfg, d, "test")
	registerTools(reg)

	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := reg.MCP().Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "docs-test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	if len(names) == 0 {
		t.Fatal("no tool registered")
	}
	return names
}

func TestREADMEDocumentsEveryTool(t *testing.T) {
	tools := section(t, readDoc(t, "README.md"), "Tools")
	for _, name := range registeredTools(t) {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(tools, "`"+name+"`") {
				t.Errorf("tool %s is not in the README tools section", name)
			}
		})
	}
}

// goDirs returns the directories under cmd/ and internal/ that hold Go files, relative to the repository root.
func goDirs(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(path, ".go") {
				rel, err := filepath.Rel(repoRoot, filepath.Dir(path))
				if err != nil {
					return err
				}
				seen[filepath.ToSlash(rel)] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var dirs []string
	for d := range seen {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs
}

// Every Go directory must be in the map. The reverse is not checked: a map entry may name a package that
// another change adds (internal/indexadv before add-index-advisor is merged).
func TestContributingMapsEveryPackage(t *testing.T) {
	pkgMap := section(t, readDoc(t, "CONTRIBUTING.md"), "Package map")
	for _, dir := range goDirs(t) {
		t.Run(dir, func(t *testing.T) {
			if !strings.Contains(pkgMap, "| `"+dir+"` |") {
				t.Errorf("%s is not in the CONTRIBUTING.md package map", dir)
			}
		})
	}
}
