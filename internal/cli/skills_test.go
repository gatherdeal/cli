package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/internal/buildinfo"
	"github.com/gatherdeal/cli/internal/output"
	"go.yaml.in/yaml/v3"
)

func TestSkillFrontmatterAndShow(t *testing.T) {
	text := skillMarkdown()
	parts := strings.SplitN(text, "---\n", 3)
	if len(parts) != 3 || parts[0] != "" {
		t.Fatal("missing frontmatter")
	}
	var front struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Metadata    struct {
			CLIVersion  string `yaml:"cli_version"`
			SkillSchema int    `yaml:"skill_schema"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(parts[1]), &front); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[a-z0-9-]{1,64}$`).MatchString(front.Name) || front.Name != "gatherdeal" {
		t.Fatalf("name %q", front.Name)
	}
	if len(front.Description) == 0 || len(front.Description) > 1024 || !strings.Contains(front.Description, "Reads and manages GatherDeal") || !strings.Contains(front.Description, "It is used when") {
		t.Fatalf("description %q", front.Description)
	}
	// The description names the apps and stays independent of their resources.
	for _, app := range skillApps(commandTree(Commands())) {
		if !strings.Contains(front.Description, app.path[0]) {
			t.Fatalf("description lacks app %s", app.path[0])
		}
	}
	for _, c := range Commands() {
		if c.App != "" && len(c.Path) > 2 && strings.Contains(front.Description, c.Path[1]) {
			t.Fatalf("description names resource %s", c.Path[1])
		}
	}
	if front.Metadata.CLIVersion != buildinfo.CurrentVersion() || front.Metadata.SkillSchema != 1 {
		t.Fatalf("metadata %+v", front.Metadata)
	}
	if !strings.Contains(parts[2], AgentContext) || !strings.Contains(parts[2], "references/crm.md") || !strings.Contains(parts[2], "gatherdeal skills install ... --force") {
		t.Fatal("missing shared guidance or skill instructions")
	}
	te := newTestEnv(t, nil)
	dir := t.TempDir()
	if res := te.run("skills", "install", "--dir", dir); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	installed, err := os.ReadFile(filepath.Join(dir, "gatherdeal", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	show := te.run("skills", "show")
	if show.exit != 0 || show.stdout != string(installed) || show.stdout != text {
		t.Fatal("show differs from installed skill")
	}
}

func TestSkillsLocations(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--dir", "custom"}, filepath.Join(cwd, "custom", "gatherdeal")},
		{[]string{"--dir", filepath.Join(cwd, "absolute")}, filepath.Join(cwd, "absolute", "gatherdeal")},
		{[]string{"--target", "claude", "--scope", "project"}, filepath.Join(resolvePath(t, cwd), ".claude", "skills", "gatherdeal")},
		{[]string{"--target", "claude", "--scope", "user"}, filepath.Join(resolvePath(t, home), ".claude", "skills", "gatherdeal")},
	} {
		res := newTestEnv(t, nil).run(append([]string{"skills", "install"}, tc.args...)...)
		if res.exit != 0 || res.data()["dir"] != tc.want {
			t.Fatalf("%v: %s", tc.args, res.stdout)
		}
	}
}

// resolvePath resolves symlinks and short names in path the way --target
// does, e.g. /var to /private/var on macOS or RUNNER~1 to the long name on
// Windows.
func resolvePath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestSkillsUsage(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--force"}, {"--dir", ""}, {"--dir", "x", "--target", "claude"},
		{"--dir", "x", "--scope", "project"}, {"--target", "claude"},
		{"--scope", "user"}, {"--target", "unknown", "--scope", "user"},
		{"--target", "claude", "--scope", "unknown"}, {"--target", "", "--scope", "user"},
	} {
		res := newTestEnv(t, nil).run(append([]string{"skills", "install"}, args...)...)
		if res.exit != 2 || res.errorCode() != "CLI_USAGE" {
			t.Fatalf("%v: %s", args, res.stdout)
		}
	}
}

// skillActions checks the reported action of SKILL.md, of references/crm.md
// and of every other skill file.
func skillActions(t *testing.T, res result, skill, crm, others string) {
	t.Helper()
	if res.exit != 0 {
		t.Fatal(res.stdout)
	}
	if res.data()["cli_version"] != buildinfo.CurrentVersion() {
		t.Fatal(res.stdout)
	}
	files := res.data()["files"].([]any)
	want := skillFiles()
	if len(files) != len(want) {
		t.Fatal(res.stdout)
	}
	for i, f := range want {
		action := map[string]string{"SKILL.md": skill, "references/crm.md": crm}[f.Path]
		if action == "" {
			action = others
		}
		file := files[i].(map[string]any)
		if file["path"] != f.Path || file["action"] != action {
			t.Fatal(res.stdout)
		}
	}
}

func TestSkillsInstallActionsAndConflicts(t *testing.T) {
	te := newTestEnv(t, nil)
	base := t.TempDir()
	args := []string{"skills", "install", "--dir", base}
	skillActions(t, te.run(args...), "created", "created", "created")
	dir := filepath.Join(base, "gatherdeal")
	skill := filepath.Join(dir, "SKILL.md")
	refs := filepath.Join(dir, "references", "crm.md")
	before, err := os.Stat(skill)
	if err != nil {
		t.Fatal(err)
	}
	skillActions(t, te.run(args...), "unchanged", "unchanged", "unchanged")
	after, err := os.Stat(skill)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged file touched")
	}
	for _, path := range []string{skill, refs, filepath.Join(dir, "keep.txt")} {
		if err := os.WriteFile(path, []byte("custom"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := te.run(args...)
	if res.exit != int(output.ExitConflict) || res.errorCode() != output.CodeSkillConflict || res.env["exit_code"] != float64(output.ExitConflict) || res.env["hint"] != "Re-run with --force to overwrite." {
		t.Fatal(res.stdout)
	}
	wantDetails := map[string]any{"paths": []any{"SKILL.md", "references/crm.md"}, "dir": dir}
	if !reflect.DeepEqual(res.env["error"].(map[string]any)["details"], wantDetails) {
		t.Fatal(res.stdout)
	}
	for _, path := range []string{skill, refs} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "custom" {
			t.Fatalf("conflict changed %s", path)
		}
	}
	// A conflict in the second file must not create the missing first file.
	if err := os.Remove(skill); err != nil {
		t.Fatal(err)
	}
	res = te.run(args...)
	wantDetails["paths"] = []any{"references/crm.md"}
	if res.exit != int(output.ExitConflict) || res.errorCode() != output.CodeSkillConflict || !reflect.DeepEqual(res.env["error"].(map[string]any)["details"], wantDetails) {
		t.Fatal(res.stdout)
	}
	if _, err := os.Lstat(skill); !os.IsNotExist(err) {
		t.Fatal("partial write before conflict")
	}
	skillActions(t, te.run(append(args, "--force")...), "created", "updated", "unchanged")
	if err := os.WriteFile(skill, []byte("old skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillActions(t, te.run(append(args, "--force")...), "updated", "unchanged", "unchanged")
	wantContents := map[string]string{filepath.Join(dir, "keep.txt"): "custom"}
	for _, f := range skillFiles() {
		wantContents[filepath.Join(dir, filepath.FromSlash(f.Path))] = f.Content
	}
	for path, want := range wantContents {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("unexpected contents %s: %v", path, err)
		}
	}
	if runtime.GOOS != "windows" {
		for path, mode := range map[string]os.FileMode{dir: 0o755, filepath.Dir(refs): 0o755, skill: 0o644, refs: 0o644} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != mode {
				t.Fatalf("%s mode %o", path, info.Mode().Perm())
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 3 {
		t.Fatalf("unexpected files: %v %v", entries, err)
	}
}

func TestSkillsRefuseSymlinks(t *testing.T) {
	for _, rel := range []string{".", "SKILL.md", "references", "references/crm.md"} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%t", rel, force), func(t *testing.T) {
				base, outside := t.TempDir(), t.TempDir()
				dir := filepath.Join(base, "gatherdeal")
				path := filepath.Join(dir, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				target := outside
				if strings.HasSuffix(rel, ".md") {
					target = filepath.Join(outside, "file")
					if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				args := []string{"skills", "install", "--dir", base}
				if force {
					args = append(args, "--force")
				}
				res := newTestEnv(t, nil).run(args...)
				if res.exit != int(output.ExitUsage) || res.errorCode() != output.CodeSkillPathInvalid || res.env["exit_code"] != float64(output.ExitUsage) ||
					!reflect.DeepEqual(res.env["error"].(map[string]any)["details"], map[string]any{"path": path}) {
					t.Fatal(res.stdout)
				}
				info, err := os.Lstat(path)
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("symlink changed")
				}
				if strings.HasSuffix(rel, ".md") {
					data, err := os.ReadFile(target)
					if err != nil || string(data) != "keep" {
						t.Fatal("symlink target changed")
					}
				} else {
					entries, err := os.ReadDir(outside)
					if err != nil || len(entries) != 0 {
						t.Fatal("symlink directory changed")
					}
				}
			})
		}
	}
}

func TestSkillIndexes(t *testing.T) {
	files := skillFiles()
	if !reflect.DeepEqual(files, skillFiles()) {
		t.Fatal("nondeterministic skill")
	}
	var paths []string
	content := map[string]string{}
	for _, f := range files {
		paths = append(paths, f.Path)
		content[f.Path] = f.Content
	}
	if want := []string{"SKILL.md", "references/crm.md", "references/outlets.md", "references/whatsapp.md"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("skill files %v, want %v", paths, want)
	}
	skill := content["SKILL.md"]
	for _, want := range []string{
		"- crm: Read CRM contacts. Notes and resources: references/crm.md or `gatherdeal agent-context crm`.\n",
		"- whatsapp: Manage WhatsApp agents and send WhatsApp messages. Notes and resources: references/whatsapp.md or `gatherdeal agent-context whatsapp`.\n",
		"- auth: Log in, check or end the access, and list local profiles. Verbs: login, logout, status.\n",
		"- version: Show the CLI version (no network).\n",
		"gatherdeal commands --search TEXT",
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("SKILL.md lacks %q", want)
		}
	}
	if want := "- whatsapp agents: List, create, pair, update and delete WhatsApp agents. Verbs: create, delete, get, list, pairing, update.\n"; !strings.Contains(content["references/whatsapp.md"], want) {
		t.Errorf("whatsapp index lacks %q", want)
	}
	// Every command appears as exactly one index entry: an app command as a
	// verb in its app's resource index, a core command in SKILL.md.
	for _, c := range Commands() {
		if c.Path[0] == "skills" && (c.Network || c.ReadOnly != (c.Path[1] == "show")) {
			t.Fatalf("incorrect metadata: %+v", c)
		}
		file := "SKILL.md"
		if c.App != "" {
			file = "references/" + c.Path[0] + ".md"
		}
		if len(c.Path) == 1 {
			if strings.Count(content[file], "- "+c.Name()+": "+c.Summary+"\n") != 1 {
				t.Errorf("%s: no single index entry in %s", c.Name(), file)
			}
			continue
		}
		group := strings.Join(c.Path[:len(c.Path)-1], " ")
		line := regexp.MustCompile(`(?m)^- ` + regexp.QuoteMeta(group) + `: ` + regexp.QuoteMeta(groupSummaries[group]) + ` Verbs: (.*)\.$`).FindStringSubmatch(content[file])
		if line == nil || !slices.Contains(strings.Split(line[1], ", "), c.Path[len(c.Path)-1]) {
			t.Errorf("%s: no index entry in %s", c.Name(), file)
		}
	}
	// Resource indexes name commands only; flags come from the catalog.
	for path, text := range content {
		if index := text[strings.Index(text, "\nResources\n")+1:]; path != "SKILL.md" && strings.Contains(index, "--") {
			t.Errorf("%s holds per-command details", path)
		}
	}
}

func TestSkillsRefuseWrongPathKinds(t *testing.T) {
	for _, rel := range []string{".", "references", "SKILL.md", "references/crm.md"} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%t", rel, force), func(t *testing.T) {
				base := t.TempDir()
				path := filepath.Join(base, "gatherdeal", filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(rel, ".md") {
					if err := os.Mkdir(path, 0o755); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
				args := []string{"skills", "install", "--dir", base}
				if force {
					args = append(args, "--force")
				}
				res := newTestEnv(t, nil).run(args...)
				if res.exit != int(output.ExitUsage) || res.errorCode() != output.CodeSkillPathInvalid || res.env["exit_code"] != float64(output.ExitUsage) ||
					!reflect.DeepEqual(res.env["error"].(map[string]any)["details"], map[string]any{"path": path}) {
					t.Fatal(res.stdout)
				}
			})
		}
	}
}

func TestSkillsAncestorSymlinks(t *testing.T) {
	for _, scope := range []string{"project", "user"} {
		for _, inside := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/inside=%t", scope, inside), func(t *testing.T) {
				base, destination := t.TempDir(), t.TempDir()
				if inside {
					destination = filepath.Join(base, "actual")
					if err := os.Mkdir(destination, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				t.Chdir(base)
				t.Setenv("HOME", base)
				t.Setenv("USERPROFILE", base)
				if err := os.Symlink(destination, filepath.Join(base, ".claude")); err != nil {
					t.Skip(err)
				}
				te := newTestEnv(t, nil)
				res := te.run("skills", "install", "--target", "claude", "--scope", scope)
				if inside {
					if res.exit != 0 || res.data()["dir"] != filepath.Join(resolvePath(t, destination), "skills", "gatherdeal") {
						t.Fatal(res.stdout)
					}
				} else {
					if res.exit != 2 || res.errorCode() != output.CodeSkillPathInvalid {
						t.Fatal(res.stdout)
					}
					entries, err := os.ReadDir(destination)
					if err != nil || len(entries) != 0 {
						t.Fatalf("escape wrote files: %v %v", entries, err)
					}
				}
				// Explicit --dir intentionally follows the chosen ancestor link.
				res = te.run("skills", "install", "--dir", filepath.Join(base, ".claude", "skills"))
				if res.exit != 0 {
					t.Fatal(res.stdout)
				}
				if _, err := os.Stat(filepath.Join(destination, "skills", "gatherdeal", "SKILL.md")); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestSkillsSymlinkedHome(t *testing.T) {
	home := t.TempDir()
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(home, link); err != nil {
		t.Skip(err)
	}
	t.Setenv("HOME", link)
	t.Setenv("USERPROFILE", link)
	res := newTestEnv(t, nil).run("skills", "install", "--target", "claude", "--scope", "user")
	if res.exit != 0 || res.data()["dir"] != filepath.Join(resolvePath(t, home), ".claude", "skills", "gatherdeal") {
		t.Fatal(res.stdout)
	}
}

// agent-context prints the same text as the skill: the general guidance in
// SKILL.md and each app's notes and resource index in references/<app>.md.
func TestAgentContextMatchesSkill(t *testing.T) {
	files := map[string]string{}
	for _, f := range skillFiles() {
		files[f.Path] = f.Content
	}
	general := newTestEnv(t, nil).run("agent-context")
	if general.exit != 0 || !strings.HasPrefix(general.stdout, AgentContext) || !strings.Contains(files["SKILL.md"], "---\n\n"+general.stdout+"\n") {
		t.Fatalf("agent-context differs from SKILL.md:\n%s", general.stdout)
	}
	apps := skillApps(commandTree(Commands()))
	for _, app := range apps {
		name := app.path[0]
		res := newTestEnv(t, nil).run("agent-context", name)
		if res.exit != 0 || res.stdout != files["references/"+name+".md"] {
			t.Fatalf("agent-context %s differs from references/%s.md:\n%s", name, name, res.stdout)
		}
		if notes := appNotes[name]; notes != "" && !strings.Contains(res.stdout[:strings.Index(res.stdout, "\nResources\n")], notes) {
			t.Errorf("%s: notes missing above the resource index", name)
		}
	}
	for name := range appNotes {
		if !slices.ContainsFunc(apps, func(app *commandNode) bool { return app.path[0] == name }) {
			t.Errorf("appNotes has an entry for %q, which is not an app", name)
		}
	}
	// App-specific guidance stays out of the general text.
	for _, notes := range appNotes {
		for _, line := range strings.Split(strings.TrimSpace(notes), "\n") {
			if strings.Contains(AgentContext, line) {
				t.Errorf("general text repeats app note %q", line)
			}
		}
	}
	for _, args := range [][]string{{"pos"}, {"crm", "contacts"}} {
		res := newTestEnv(t, nil).run(append([]string{"agent-context"}, args...)...)
		if res.exit != 2 || res.errorCode() != "CLI_USAGE" {
			t.Fatalf("%v: exit %d: %s", args, res.exit, res.stdout)
		}
	}
	res := newTestEnv(t, nil).run("agent-context", "pos")
	if want := `unknown app "pos"; valid apps: crm, outlets, whatsapp`; res.env["error"].(map[string]any)["message"] != want {
		t.Fatal(res.stdout)
	}
}
