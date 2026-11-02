// Package contractcheck checks that the CLI matches the contract bundle it
// commits: contracts/manifest.json, the OpenAPI documents and the generated
// clients under internal/generated.
package contractcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/contracts"
	"github.com/gatherdeal/cli/internal/cli"
	"github.com/gatherdeal/cli/internal/crm"
)

const expansionPrefix = "with_"

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func manifest(t *testing.T) *contracts.Manifest {
	t.Helper()
	m, err := contracts.Load()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// The generated package, the manifest and the embedded document all name
// the digest of the committed contract file.
func TestBundleDigest(t *testing.T) {
	for _, b := range manifest(t).Bundles {
		t.Run(b.Service+"/"+b.Version, func(t *testing.T) {
			onDisk, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(b.Contract)))
			if err != nil {
				t.Fatal(err)
			}
			embedded, err := b.OpenAPI()
			if err != nil {
				t.Fatal(err)
			}
			if digest(onDisk) != b.Digest || digest(embedded) != b.Digest {
				t.Fatal("contract digest mismatch")
			}
			paths, err := filepath.Glob(filepath.Join(repoRoot(t), b.GoPackage, "*_gen.go"))
			if err != nil {
				t.Fatal(err)
			}
			methods := map[string]bool{}
			constants := map[string]string{}
			for _, path := range paths {
				f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
				if err != nil {
					t.Fatal(err)
				}
				for _, decl := range f.Decls {
					switch d := decl.(type) {
					case *ast.FuncDecl:
						if d.Recv != nil && len(d.Recv.List) == 1 {
							if star, ok := d.Recv.List[0].Type.(*ast.StarExpr); ok {
								if id, ok := star.X.(*ast.Ident); ok && id.Name == "Client" {
									methods[d.Name.Name] = true
								}
							}
						}
					case *ast.GenDecl:
						if d.Tok != token.CONST {
							continue
						}
						for _, spec := range d.Specs {
							v := spec.(*ast.ValueSpec)
							for i, name := range v.Names {
								if i < len(v.Values) {
									if lit, ok := v.Values[i].(*ast.BasicLit); ok {
										constants[name.Name], _ = strconv.Unquote(lit.Value)
									}
								}
							}
						}
					}
				}
			}
			if constants["BundleDigest"] != b.Digest || constants["Service"] != b.Service || constants["Version"] != b.Version {
				t.Fatal("generated bundle metadata mismatch")
			}
			for _, op := range b.Operations {
				if !methods[op.GoMethod] {
					t.Errorf("missing Client.%s", op.GoMethod)
				}
			}
		})
	}
}

// Every manifest operation has exactly one CLI command, every command that
// names an operation names a manifest one, and the generated client has the
// operation's method.
func TestEveryOperationHasACommand(t *testing.T) {
	byOperation := map[string][]*cli.Command{}
	for _, c := range cli.Commands() {
		if c.Operation != "" {
			byOperation[c.Operation] = append(byOperation[c.Operation], c)
		}
	}
	known := map[string]bool{}
	for _, b := range manifest(t).Bundles {
		for _, op := range b.Operations {
			known[op.PublicID] = true
			for _, c := range byOperation[op.PublicID] {
				if c.App != b.Service || (c.Presentation == nil && c.Name() != op.Command) {
					t.Errorf("operation %s: app/path = %q/%q, want %q/%q", op.PublicID, c.App, c.Name(), b.Service, op.Command)
				}
			}
			if n := len(byOperation[op.PublicID]); n != 1 {
				t.Errorf("operation %s has %d CLI commands, want 1", op.PublicID, n)
			}

		}
	}
	for id := range byOperation {
		if !known[id] {
			t.Errorf("a command names operation %s, which the manifest lacks", id)
		}
	}
}

// A command's --with-* flags are exactly its operation's delegated
// expansions, and its edge filters are among the delegated ones.
func TestFlagsWithinManifest(t *testing.T) {
	m := manifest(t)
	for _, c := range cli.Commands() {
		var with []string
		for _, f := range c.Flags {
			if strings.HasPrefix(f.Name, "with-") {
				with = append(with, strings.ReplaceAll(f.Name, "-", "_"))
			}
		}
		if c.Operation == "" {
			if len(with) > 0 {
				t.Errorf("%s has expansion flags but no operation", c.Name())
			}
			continue
		}
		op, ok := m.Operation(c.Operation)
		if !ok {
			continue // reported by TestEveryOperationHasACommand
		}
		for _, p := range with {
			if !slices.Contains(op.DelegatedExpansions, p) {
				t.Errorf("%s: --%s is not a delegated expansion of %s", c.Name(), strings.ReplaceAll(p, "_", "-"), op.PublicID)
			}
		}
		for _, p := range op.DelegatedExpansions {
			if !strings.HasPrefix(p, expansionPrefix) {
				t.Errorf("%s: expansion %q lacks the %s prefix", op.PublicID, p, expansionPrefix)
			}
			if !slices.Contains(with, p) {
				t.Errorf("%s: no flag for delegated expansion %s", c.Name(), p)
			}
		}
	}
	list, ok := m.Operation(crm.ListOperation)
	if !ok || !slices.Contains(list.DelegatedEdgeFilters, crm.TagsEdge) {
		t.Errorf("%s does not allow the %q edge filter that --tag uses", crm.ListOperation, crm.TagsEdge)
	}
}

// The generated packages import the standard library only.
func TestGeneratedImportsStdlibOnly(t *testing.T) {
	root := filepath.Join(repoRoot(t), "internal", "generated")
	files := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		files++
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if first, _, _ := strings.Cut(p, "/"); strings.Contains(first, ".") {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s imports non-stdlib %s", rel, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("no generated Go files found")
	}
}

// pageFlagNames are the paging flags every list command has.
var pageFlagNames = []string{"page", "page-size", "after", "total", "all", "limit"}

// A command has the paging flags exactly when its operation declares
// pagination, and that pagination is the adaptive style the CLI speaks.
func TestPagingFlagsMatchManifest(t *testing.T) {
	m := manifest(t)
	for _, c := range cli.Commands() {
		if c.Operation == "" {
			continue
		}
		op, ok := m.Operation(c.Operation)
		if !ok {
			continue // reported by TestEveryOperationHasACommand
		}
		names := map[string]bool{}
		for _, f := range c.Flags {
			names[f.Name] = true
		}
		paged := op.Pagination != nil
		if paged && (op.Pagination.Style != contracts.PaginationAdaptive || op.Pagination.MaxPageSize < 1) {
			t.Errorf("%s: pagination %+v is not adaptive", op.PublicID, *op.Pagination)
		}
		for _, f := range pageFlagNames {
			if names[f] != paged {
				t.Errorf("%s: --%s present=%v, operation paginated=%v", c.Name(), f, names[f], paged)
			}
		}
	}
}
