// Package contractcheck checks that the CLI matches the contract bundle it
// commits: contracts/manifest.json, the OpenAPI documents and the generated
// clients under internal/generated.
package contractcheck

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
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

const exclusionsFile = "published-operation-exclusions.json"

type operationExclusion struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

func operationExclusions(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(exclusionsFile)
	if err != nil {
		t.Fatal(err)
	}
	var entries []operationExclusion
	if err := jsonv2.Unmarshal(data, &entries, jsonv2.RejectUnknownMembers(true)); err != nil {
		t.Fatalf("%s: %v", exclusionsFile, err)
	}
	if entries == nil {
		t.Fatalf("%s must contain a JSON array", exclusionsFile)
	}
	exclusions := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.ID == "" || strings.TrimSpace(entry.ID) != entry.ID {
			t.Fatalf("%s: invalid public ID %q", exclusionsFile, entry.ID)
		}
		if strings.TrimSpace(entry.Reason) == "" || strings.ContainsAny(entry.Reason, "\r\n\v\f\u0085\u2028\u2029") {
			t.Fatalf("%s: %s requires a nonempty, one-line reason", exclusionsFile, entry.ID)
		}
		if _, exists := exclusions[entry.ID]; exists {
			t.Fatalf("%s: duplicate exclusion for %s", exclusionsFile, entry.ID)
		}
		exclusions[entry.ID] = entry.Reason
	}
	return exclusions
}

func TestPublishedOperationCoverage(t *testing.T) {
	for _, problem := range publishedOperationCoverage(manifest(t), cli.Commands(), operationExclusions(t)) {
		t.Error(problem)
	}
}

func publishedOperationCoverage(m *contracts.Manifest, registered []*cli.Command, exclusions map[string]string) []string {
	var problems []string
	byOperation := map[string][]*cli.Command{}
	for _, c := range registered {
		if c.Operation != "" {
			byOperation[c.Operation] = append(byOperation[c.Operation], c)
		}
	}
	known := map[string]bool{}
	for _, b := range m.Bundles {
		for _, op := range b.Operations {
			id := op.PublicID
			known[id] = true
			commands := byOperation[id]
			canonicalCount := 0
			for _, c := range commands {
				wantPath := op.Command
				switch {
				case c.IsAlias():
					if c.App != b.Service || !slices.ContainsFunc(op.FormerIDs, func(former contracts.FormerID) bool {
						return c.Name() == former.Command
					}) {
						problems = append(problems, fmt.Sprintf("operation %s: undeclared CLI alias %q in app %q", id, c.Name(), c.App))
					}
					continue
				case c.Presentation != nil:
					wantPath = strings.Join(c.Presentation.Path, " ")
				}
				canonicalCount++
				if c.App != b.Service || c.Name() != wantPath {
					problems = append(problems, fmt.Sprintf("operation %s: app/path = %q/%q, want %q/%q", id, c.App, c.Name(), b.Service, wantPath))
				}
			}
			_, excluded := exclusions[id]
			switch {
			case canonicalCount == 0 && !excluded:
				problems = append(problems, fmt.Sprintf("%s has no CLI command or exclusion in %s", id, exclusionsFile))
			case len(commands) > 0 && excluded:
				problems = append(problems, fmt.Sprintf("stale exclusion for %s: a CLI command already covers it", id))
			}
			if len(commands) > 0 && cli.RequiresPublishedOperationExclusion(op) {
				problems = append(problems, fmt.Sprintf("%s is a new external write. Remove its CLI command and add an exclusion in %s", id, exclusionsFile))
			}
			if canonicalCount > 1 {
				problems = append(problems, fmt.Sprintf("%s has %d CLI commands, want at most 1", id, canonicalCount))
			}
		}
	}
	for id := range byOperation {
		if !known[id] {
			problems = append(problems, fmt.Sprintf("a command names operation %s, which the manifest lacks", id))
		}
	}
	for id := range exclusions {
		if !known[id] {
			problems = append(problems, fmt.Sprintf("stale exclusion for %s: the manifest lacks this operation", id))
		}
	}
	return problems
}

func TestPublishedOperationCoverageDispositions(t *testing.T) {
	const service = "whatsapp"
	const newID = "whatsapp.messages.forward"
	const removedID = "whatsapp.messages.removed"
	const grandfatheredID = "whatsapp.messages.create"

	read := contracts.Effect{Mutation: "none", Interaction: "closed"}
	externalWrite := contracts.Effect{Mutation: "additive", Interaction: "external"}
	closedWrite := contracts.Effect{Mutation: "additive", Interaction: "closed"}

	for _, tt := range []struct {
		name       string
		id         string
		effect     contracts.Effect
		registered bool
		excludedID string
		want       []string
	}{
		{
			name:       "valid exclusion",
			id:         newID,
			effect:     externalWrite,
			excludedID: newID,
		},
		{
			name:   "uncovered operation",
			id:     newID,
			effect: externalWrite,
			want:   []string{newID + " has no CLI command or exclusion in " + exclusionsFile},
		},
		{
			name:       "exclusion for covered operation",
			id:         newID,
			effect:     read,
			registered: true,
			excludedID: newID,
			want:       []string{"stale exclusion for " + newID + ": a CLI command already covers it"},
		},
		{
			name:       "exclusion for removed operation",
			id:         newID,
			effect:     read,
			registered: true,
			excludedID: removedID,
			want:       []string{"stale exclusion for " + removedID + ": the manifest lacks this operation"},
		},
		{
			name:       "new external write with a command",
			id:         newID,
			effect:     externalWrite,
			registered: true,
			want:       []string{newID + " is a new external write. Remove its CLI command and add an exclusion in " + exclusionsFile},
		},
		{
			name:       "grandfathered external write",
			id:         grandfatheredID,
			effect:     externalWrite,
			registered: true,
		},
		{
			name:       "new closed write with a command",
			id:         newID,
			effect:     closedWrite,
			registered: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &cli.Command{App: service, Operation: tt.id, Path: strings.Split(tt.id, ".")}
			m := &contracts.Manifest{Bundles: []contracts.Bundle{{
				Service: service,
				Operations: []contracts.Operation{{
					PublicID: tt.id,
					Command:  c.Name(),
					Effect:   tt.effect,
				}},
			}}}
			var registered []*cli.Command
			if tt.registered {
				registered = append(registered, c)
			}
			exclusions := map[string]string{}
			if tt.excludedID != "" {
				exclusions[tt.excludedID] = "Deferred from this CLI release."
			}
			if got := publishedOperationCoverage(m, registered, exclusions); !slices.Equal(got, tt.want) {
				t.Errorf("coverage problems = %q, want %q", got, tt.want)
			}
		})
	}
}

// Every generated binding and manifest entry must agree on a published path
// that parses as the runtime command for that operation.
func TestBindingCommandPaths(t *testing.T) {
	exclusions := operationExclusions(t)
	commandPattern := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*( [a-z0-9]+(-[a-z0-9]+)*)*$`)
	runtimePaths := map[string]string{}
	for _, c := range cli.Commands() {
		if !c.IsAlias() {
			runtimePaths[c.Operation] = c.Name()
		}
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
				if _, excluded := exclusions[op.PublicID]; excluded {
					return
				}
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
			continue // reported by TestPublishedOperationCoverage
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
	exclusions := operationExclusions(t)
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
				ID          string          `json:"x-gd-public-id"`
				Batch       json.RawMessage `json:"x-gd-batch"`
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
					if _, excluded := exclusions[route.ID]; !excluded {
						t.Errorf("missing command for %s", route.ID)
					}
					continue
				}
				flags := map[string]cli.FlagSpec{}
				for _, f := range c.Flags {
					flags[f.Name] = f
				}
				input, hasInput := flags["input"]
				// A batch body is read from --file and sent in chunks.
				file, hasFile := flags["file"]
				batch := route.Batch != nil
				if hasInput != (route.RequestBody != nil && !batch) || hasFile != batch || batch && (!file.Required || route.RequestBody == nil) {
					t.Errorf("%s: --input present=%v, --file present=%v, OpenAPI request body present=%v, batch=%v", c.Name(), hasInput, hasFile, route.RequestBody != nil, batch)
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
			continue // reported by TestPublishedOperationCoverage
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
