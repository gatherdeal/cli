// Package contractcheck checks that the CLI matches the contract bundle it
// commits: contracts/manifest.json, the OpenAPI documents and the generated
// clients under internal/generated.
package contractcheck

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/contracts"
	"github.com/gatherdeal/cli/internal/cli"
	"github.com/gatherdeal/cli/internal/crm"
	crmv1 "github.com/gatherdeal/cli/internal/generated/crm/v1"
	identityv1 "github.com/gatherdeal/cli/internal/generated/identity/v1"
	whatsappv1 "github.com/gatherdeal/cli/internal/generated/whatsapp/v1"
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
				wantPath := op.Command
				if c.Presentation != nil {
					wantPath = strings.Join(c.Presentation.Path, " ")
				}
				if c.App != b.Service || c.Name() != wantPath {
					t.Errorf("operation %s: app/path = %q/%q, want %q/%q", op.PublicID, c.App, c.Name(), b.Service, wantPath)
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

// Every generated binding and manifest entry must agree on a published path
// that parses as the runtime command for that operation.
func TestBindingCommandPaths(t *testing.T) {
	commandPattern := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*( [a-z0-9]+(-[a-z0-9]+)*)*$`)
	runtimePaths := map[string]string{}
	for _, c := range cli.Commands() {
		runtimePaths[c.Operation] = c.Name()
	}
	bindings := map[string]string{}
	recordBinding := func(id, path string) {
		if _, duplicate := bindings[id]; duplicate {
			t.Errorf("duplicate generated binding %s", id)
		}
		bindings[id] = path
	}
	for _, b := range crmv1.CLIBindings() {
		recordBinding(b.PublicID, b.Command)
	}
	for _, b := range identityv1.CLIBindings() {
		recordBinding(b.PublicID, b.Command)
	}
	for _, b := range whatsappv1.CLIBindings() {
		recordBinding(b.PublicID, b.Command)
	}
	for _, b := range manifest(t).Bundles {
		for _, op := range b.Operations {
			t.Run(op.PublicID, func(t *testing.T) {
				if !commandPattern.MatchString(op.Command) {
					t.Errorf("invalid CLI path %q", op.Command)
				}
				if got, ok := bindings[op.PublicID]; !ok || got != op.Command {
					t.Errorf("generated path = %q, want %q", got, op.Command)
				}
				delete(bindings, op.PublicID)
				var stdout bytes.Buffer
				args := append(strings.Fields(op.Command), "--help")
				exit := cli.Run(t.Context(), &cli.Env{Stdout: &stdout, Stderr: io.Discard}, args)
				firstLine, _, _ := strings.Cut(stdout.String(), "\n")
				want := "gatherdeal " + runtimePaths[op.PublicID]
				if exit != 0 || (firstLine != want && !strings.HasPrefix(firstLine, want+" ")) {
					t.Errorf("%q --help exited %d and printed %q, want help for %q", op.Command, exit, firstLine, want)
				}
			})
		}
	}
	for id := range bindings {
		t.Errorf("generated binding %s lacks a manifest operation", id)
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

func TestRequestFlagsMatchContracts(t *testing.T) {
	commands := map[string]*cli.Command{}
	for _, c := range cli.Commands() {
		commands[c.Operation] = c
	}
	for _, b := range manifest(t).Bundles {
		data, err := b.OpenAPI()
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Paths map[string]map[string]struct {
				ID          string `json:"x-gd-public-id"`
				RequestBody json.RawMessage
				Parameters  []struct {
					Name, In string
					Required bool
				}
			}
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		for _, path := range doc.Paths {
			for _, route := range path {
				c := commands[route.ID]
				if c == nil {
					t.Errorf("missing command for %s", route.ID)
					continue
				}
				flags := map[string]cli.FlagSpec{}
				for _, f := range c.Flags {
					flags[f.Name] = f
				}
				input, hasInput := flags["input"]
				if hasInput != (route.RequestBody != nil) {
					t.Errorf("%s: --input present=%v, OpenAPI request body present=%v", c.Name(), hasInput, route.RequestBody != nil)
				}
				if hasInput && input.Required != (route.ID != "whatsapp.agents.create") {
					t.Errorf("%s: unexpected --input required=%v", c.Name(), input.Required)
				}
				_, hasKey := flags["idempotency-key"]
				keyed := c.Contract().Idempotency == contracts.IdempotencyKeyed
				if hasKey != keyed {
					t.Errorf("%s: --idempotency-key present=%v, manifest keyed=%v", c.Name(), hasKey, keyed)
				}
				ifMatch := false
				for _, p := range route.Parameters {
					if p.In == "query" && p.Required {
						name := strings.ReplaceAll(p.Name, "_", "-")
						if !flags[name].Required {
							t.Errorf("%s: required query parameter %s has no required flag", c.Name(), p.Name)
						}
					}
					ifMatch = ifMatch || p.In == "header" && p.Name == "If-Match" && p.Required
				}
				if flag, ok := flags["if-match"]; ok != ifMatch || flag.Required != ifMatch {
					t.Errorf("%s: --if-match present=%v required=%v, OpenAPI requires If-Match=%v", c.Name(), ok, flag.Required, ifMatch)
				}
			}
		}
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
		if paged && strings.Contains(op.Pagination.ResultField, "_") {
			for _, f := range c.Flags {
				if slices.Contains(pageFlagNames, f.Name) && strings.Contains(strings.ToLower(f.Usage), op.Pagination.ResultField) {
					t.Errorf("%s: --%s help exposes wire result field %q", c.Name(), f.Name, op.Pagination.ResultField)
				}
			}
		}
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
