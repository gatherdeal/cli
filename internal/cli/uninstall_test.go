package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/internal/server"
	"github.com/gatherdeal/cli/internal/store"
)

// isolateHome points the home directory at a temporary one, so uninstall
// never inspects the real default skill locations.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := resolvePath(t, t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// twoProfiles logs in "default" (keyring) and "work" (file store) on the
// production server.
func twoProfiles(t *testing.T) (*fakeServer, *testEnv) {
	t.Helper()
	fake := newFake(t, server.ProductionOrigin)
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	if res := te.run("auth", "login", "--profile", "work", "--server", server.ProductionOrigin, "--credential-store", "file"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	return fake, te
}

// treeSnapshot lists every path under the given roots with its size, without
// following symlinks.
func treeSnapshot(t *testing.T, roots ...string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			out[path] = info.Size()
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
	return out
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func profilesOf(t *testing.T, res result) []map[string]any {
	t.Helper()
	raw, _ := res.data()["profiles"].([]any)
	out := make([]map[string]any, len(raw))
	for i, p := range raw {
		out[i] = p.(map[string]any)
	}
	return out
}

func stepStatus(entry map[string]any, step string) string {
	s, _ := entry[step].(map[string]any)["status"].(string)
	return s
}

func skillsOf(res result) map[string]map[string]any {
	out := map[string]map[string]any{}
	raw, _ := res.data()["skills"].([]any)
	for _, s := range raw {
		m := s.(map[string]any)
		out[m["path"].(string)] = m
	}
	return out
}

func stillValid(res result) []string {
	var names []string
	raw, _ := res.data()["accesses_still_valid"].([]any)
	for _, a := range raw {
		names = append(names, a.(map[string]any)["profile"].(string))
	}
	return names
}

func TestUninstallPlanChangesNothing(t *testing.T) {
	home := isolateHome(t)
	fake, te := twoProfiles(t)
	if res := te.run("skills", "install", "--target", "claude", "--scope", "user"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	before := treeSnapshot(t, filepath.Dir(te.root), home)
	keyring := len(te.keyring.items)
	for _, args := range [][]string{{"uninstall"}, {"uninstall", "--dry-run"}, {"uninstall", "--dry-run", "--yes"}, {"uninstall", "--local-only"}} {
		res := te.run(args...)
		wantExit, wantCode := 0, ""
		if !reflect.DeepEqual(args, []string{"uninstall", "--dry-run"}) && !reflect.DeepEqual(args, []string{"uninstall", "--dry-run", "--yes"}) {
			wantExit, wantCode = 9, "CLI_WRITE_CONFIRMATION_REQUIRED"
		}
		if res.exit != wantExit || res.errorCode() != wantCode {
			t.Fatalf("%v: exit %d code %s", args, res.exit, res.errorCode())
		}
		profiles := profilesOf(t, res)
		if len(profiles) != 2 || profiles[0]["profile"] != "default" || profiles[1]["profile"] != "work" {
			t.Fatalf("%v: profiles %v", args, profiles)
		}
		wantRevoke := "planned"
		if args[len(args)-1] == "--local-only" {
			wantRevoke = "skipped"
			if got := stillValid(res); !reflect.DeepEqual(got, []string{"default", "work"}) {
				t.Fatalf("still valid %v", got)
			}
		}
		for _, p := range profiles {
			if stepStatus(p, "revoke") != wantRevoke || stepStatus(p, "delete") != "planned" {
				t.Fatalf("%v: %v", args, p)
			}
		}
		if c := profiles[0]["credential"].(map[string]any); c["store"] != "keyring" || c["keyring_service"] != store.KeyringService || c["keyring_account"] == "" {
			t.Fatalf("keyring credential %v", c)
		}
		if c := profiles[1]["credential"].(map[string]any); c["store"] != "file" || !exists(c["file"].(string)) {
			t.Fatalf("file credential %v", c)
		}
		skill := filepath.Join(home, ".claude", "skills", "gatherdeal")
		if s := skillsOf(res)[skill]; s == nil || s["status"] != "planned" || s["recorded"] != true || s["target"] != "claude" {
			t.Fatalf("skills %v", res.data()["skills"])
		}
		if d := res.data()["config_dir"].(map[string]any); d["path"] != te.root || d["status"] != "planned" {
			t.Fatalf("config dir %v", d)
		}
		if b := res.data()["binary"].(map[string]any); b["instructions"] == "" {
			t.Fatalf("binary %v", b)
		}
		if !strings.Contains(res.stderr, "Uninstall plan (nothing was changed)") {
			t.Fatalf("stderr %s", res.stderr)
		}
	}
	if after := treeSnapshot(t, filepath.Dir(te.root), home); !reflect.DeepEqual(before, after) {
		t.Fatalf("the plan changed files:\nbefore %v\nafter  %v", before, after)
	}
	if len(te.keyring.items) != keyring || len(fake.revokeForms) != 0 {
		t.Fatal("the plan changed credentials or contacted the server")
	}
}

func TestUninstallFullRun(t *testing.T) {
	home := isolateHome(t)
	fake, te := twoProfiles(t)
	tokens := append([]string(nil), fake.tokens...)
	for _, target := range []string{"claude", "agents"} {
		if res := te.run("skills", "install", "--target", target, "--scope", "user"); res.exit != 0 {
			t.Fatal(res.stdout)
		}
	}
	res := te.run("uninstall", "--yes")
	if res.exit != 0 {
		t.Fatalf("exit %d %s", res.exit, res.stdout)
	}
	for _, p := range profilesOf(t, res) {
		if stepStatus(p, "revoke") != "revoked" || stepStatus(p, "delete") != "deleted" {
			t.Fatalf("profile %v", p)
		}
	}
	for _, tok := range tokens {
		if !fake.revoked[tok] {
			t.Fatal("an access was not revoked")
		}
	}
	if len(te.keyring.items) != 0 {
		t.Fatalf("keyring left %v", te.keyring.items)
	}
	if exists(te.root) {
		t.Fatal("config directory left")
	}
	for _, dir := range []string{".claude", ".agents"} {
		skill := filepath.Join(home, dir, "skills", "gatherdeal")
		if exists(skill) || !exists(filepath.Dir(skill)) {
			t.Fatalf("%s: skill left or parent removed", skill)
		}
		if s := skillsOf(res)[skill]; s == nil || s["status"] != "deleted" {
			t.Fatalf("skills %v", res.data()["skills"])
		}
	}
	if d := res.data()["config_dir"].(map[string]any); d["status"] != "deleted" {
		t.Fatalf("config dir %v", d)
	}
	if len(stillValid(res)) != 0 {
		t.Fatalf("still valid %v", stillValid(res))
	}
	// Nothing is left to do.
	again := te.run("uninstall", "--yes")
	if again.exit != 0 || len(profilesOf(t, again)) != 0 || len(skillsOf(again)) != 0 ||
		again.data()["config_dir"].(map[string]any)["status"] != "absent" {
		t.Fatalf("second run %s", again.stdout)
	}
}

func TestUninstallCustomServer(t *testing.T) {
	isolateHome(t)
	fake := newFake(t, "")
	te := newTestEnv(t, fake)
	if res := te.run("auth", "login", "--server", fake.origin, "--accept-server-risk", fake.origin, "--profile", "dev"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	res := te.run("uninstall", "--yes")
	if res.exit != 0 || stepStatus(profilesOf(t, res)[0], "revoke") != "revoked" || !fake.revoked[fake.lastToken()] {
		t.Fatalf("exit %d %s", res.exit, res.stdout)
	}
}

func TestUninstallRevokeFailureStops(t *testing.T) {
	home := isolateHome(t)
	fake, te := twoProfiles(t)
	if res := te.run("skills", "install", "--target", "claude", "--scope", "user"); res.exit != 0 {
		t.Fatal(res.stdout)
	}
	// "default" revokes, "work" (the second token) fails.
	fake.revokeFailToken = fake.tokens[1]
	workFile := filepath.Join(te.root, "credentials")
	res := te.run("uninstall", "--yes")
	if res.exit != 8 || res.errorCode() != "CLI_REVOKE_FAILED" {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
	details := res.env["error"].(map[string]any)["details"].(map[string]any)
	if details["profile"] != "work" || details["server"] != server.ProductionOrigin {
		t.Fatalf("details %v", details)
	}
	if !strings.Contains(res.env["hint"].(string), "--local-only") || !strings.Contains(res.env["hint"].(string), "Sessions & devices") {
		t.Fatalf("hint %v", res.env["hint"])
	}
	profiles := profilesOf(t, res)
	if stepStatus(profiles[0], "revoke") != "revoked" || stepStatus(profiles[0], "delete") != "deleted" ||
		stepStatus(profiles[1], "revoke") != "failed" || stepStatus(profiles[1], "delete") != "not_attempted" {
		t.Fatalf("profiles %v", profiles)
	}
	if got := stillValid(res); !reflect.DeepEqual(got, []string{"work"}) {
		t.Fatalf("still valid %v", got)
	}
	skill := filepath.Join(home, ".claude", "skills", "gatherdeal")
	if s := skillsOf(res)[skill]; s["status"] != "not_attempted" || !exists(skill) {
		t.Fatalf("skill %v", s)
	}
	if d := res.data()["config_dir"].(map[string]any); d["status"] != "not_attempted" {
		t.Fatalf("config dir %v", d)
	}
	// The failed profile and its credential file stay; the revoked one is gone.
	p := loadProfiles(t, te)
	if p.Get("default") != nil || p.Get("work") == nil {
		t.Fatalf("profiles %v", p.Names())
	}
	if entries, _ := os.ReadDir(workFile); len(entries) != 1 {
		t.Fatalf("credential files %v", entries)
	}
	if st := te.run("auth", "status", "--profile", "work"); st.exit != 0 {
		t.Fatalf("work no longer usable: %s", st.stdout)
	}
	// Once the server recovers, the run completes.
	fake.revokeFailToken = ""
	if res := te.run("uninstall", "--yes"); res.exit != 0 || exists(te.root) || exists(skill) {
		t.Fatalf("retry: %s", res.stdout)
	}
}

func TestUninstallLocalOnly(t *testing.T) {
	isolateHome(t)
	fake, te := twoProfiles(t)
	res := te.run("uninstall", "--yes", "--local-only")
	if res.exit != 0 {
		t.Fatalf("exit %d %s", res.exit, res.stdout)
	}
	if len(fake.revokeForms) != 0 {
		t.Fatal("--local-only contacted the server")
	}
	for _, p := range profilesOf(t, res) {
		if stepStatus(p, "revoke") != "skipped" || stepStatus(p, "delete") != "deleted" {
			t.Fatalf("profile %v", p)
		}
	}
	if got := stillValid(res); !reflect.DeepEqual(got, []string{"default", "work"}) {
		t.Fatalf("still valid %v", got)
	}
	notes, _ := res.data()["notes"].([]any)
	if len(notes) == 0 || !strings.Contains(notes[0].(string), "passport › Security › Sessions & devices") {
		t.Fatalf("notes %v", notes)
	}
	if !strings.Contains(res.stderr, "Still valid on the server: profile \"work\"") {
		t.Fatalf("stderr %s", res.stderr)
	}
	if len(te.keyring.items) != 0 || exists(te.root) {
		t.Fatal("local state left")
	}
}

func TestUninstallAlreadyInactiveAndNoCredential(t *testing.T) {
	isolateHome(t)
	fake, te := loggedIn(t)
	// A login still waiting for approval: a profile without a credential
	// whose device code sits in the keyring.
	if res := te.run("auth", "login", "--no-wait", "--profile", "pending", "--server", server.ProductionOrigin); res.exit != 9 {
		t.Fatal(res.stdout)
	}
	fake.revoked[fake.tokens[0]] = true
	fake.revokeStatus = 400
	res := te.run("uninstall", "--yes")
	if res.exit != 0 {
		t.Fatalf("exit %d %s", res.exit, res.stdout)
	}
	profiles := profilesOf(t, res)
	if profiles[0]["profile"] != "default" || stepStatus(profiles[0], "revoke") != "already_inactive" ||
		profiles[1]["profile"] != "pending" || stepStatus(profiles[1], "revoke") != "no_credential" ||
		stepStatus(profiles[1], "delete") != "deleted" {
		t.Fatalf("profiles %v", profiles)
	}
	if len(te.keyring.items) != 0 || exists(te.root) {
		t.Fatalf("left keyring %v", te.keyring.items)
	}
}

func TestUninstallSkillsInstallRecords(t *testing.T) {
	home := isolateHome(t)
	te := newTestEnv(t, nil)
	custom := t.TempDir()
	for _, args := range [][]string{{"--target", "claude", "--scope", "user"}, {"--dir", custom}, {"--dir", custom, "--force"}} {
		if res := te.run(append([]string{"skills", "install"}, args...)...); res.exit != 0 {
			t.Fatal(res.stdout)
		}
	}
	installs, err := store.LoadSkillInstalls(te.root)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.SkillInstall{
		{Path: filepath.Join(home, ".claude", "skills", "gatherdeal"), Target: "claude", Scope: "user"},
		{Path: filepath.Join(resolvePath(t, custom), "gatherdeal")},
	}
	if got := installs.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded %+v", got)
	}
}

// writeGeneratedSkill writes the skill the CLI generates into dir.
func writeGeneratedSkill(t *testing.T, dir string) {
	t.Helper()
	for _, f := range skillFiles() {
		path := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(f.Content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUninstallSkillDirectories(t *testing.T) {
	home := isolateHome(t)
	te := newTestEnv(t, nil)
	clean, extra := t.TempDir(), t.TempDir()
	for _, dir := range []string{clean, extra} {
		if res := te.run("skills", "install", "--dir", dir); res.exit != 0 {
			t.Fatal(res.stdout)
		}
	}
	cleanSkill, extraSkill := filepath.Join(resolvePath(t, clean), "gatherdeal"), filepath.Join(resolvePath(t, extra), "gatherdeal")
	if err := os.WriteFile(filepath.Join(extraSkill, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An unrecorded, generated skill at a default user-scope location.
	claude := filepath.Join(home, ".claude", "skills", "gatherdeal")
	writeGeneratedSkill(t, claude)
	// A symlink at the other default location, to a generated skill.
	agents := filepath.Join(home, ".agents", "skills", "gatherdeal")
	linked := filepath.Join(t.TempDir(), "gatherdeal")
	writeGeneratedSkill(t, linked)
	if err := os.MkdirAll(filepath.Dir(agents), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinked := os.Symlink(linked, agents) == nil

	plan := te.run("uninstall")
	skills := skillsOf(plan)
	for path, want := range map[string]string{cleanSkill: "planned", extraSkill: "skipped", claude: "planned"} {
		if s := skills[path]; s == nil || s["status"] != want {
			t.Fatalf("%s: %v", path, s)
		}
	}
	if skills[claude]["recorded"] != false || skills[cleanSkill]["recorded"] != true {
		t.Fatalf("recorded flags %v", skills)
	}
	if symlinked {
		if s := skills[agents]; s == nil || s["status"] != "skipped" || !strings.Contains(s["reason"].(string), "symbolic link") {
			t.Fatalf("symlink %v", s)
		}
	}

	res := te.run("uninstall", "--yes")
	if res.exit != 0 {
		t.Fatalf("exit %d %s", res.exit, res.stdout)
	}
	if exists(cleanSkill) || exists(claude) {
		t.Fatal("generated skills left")
	}
	if !exists(filepath.Join(extraSkill, "notes.txt")) || !exists(filepath.Join(extraSkill, "SKILL.md")) {
		t.Fatal("a directory with foreign files was touched")
	}
	if symlinked && (!exists(agents) || !exists(filepath.Join(linked, "SKILL.md"))) {
		t.Fatal("a symlinked skill or its target was deleted")
	}
	if exists(te.root) {
		t.Fatal("config directory left")
	}
}

func TestUninstallSkipsForeignSkill(t *testing.T) {
	home := isolateHome(t)
	te := newTestEnv(t, nil)
	cases := map[string]func(dir string){
		"handwritten": func(dir string) {
			_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: gatherdeal\ndescription: mine\n---\n\nMy notes.\n"), 0o644)
		},
		"foreign reference": func(dir string) {
			writeGeneratedSkill(t, dir)
			_ = os.WriteFile(filepath.Join(dir, "references", "crm.md"), []byte("my own notes\n"), 0o644)
		},
		"no SKILL.md": func(dir string) {
			_ = os.MkdirAll(filepath.Join(dir, "references"), 0o755)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(home, ".claude", "skills", "gatherdeal")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			setup(dir)
			before := treeSnapshot(t, dir)
			res := te.run("uninstall", "--yes")
			if s := skillsOf(res)[dir]; res.exit != 0 || s == nil || s["status"] != "skipped" || s["reason"] == "" {
				t.Fatalf("exit %d %v", res.exit, s)
			}
			if after := treeSnapshot(t, dir); !reflect.DeepEqual(before, after) {
				t.Fatal("foreign skill changed")
			}
		})
	}
}

func TestUninstallKeepsForeignConfigEntries(t *testing.T) {
	isolateHome(t)
	_, te := loggedIn(t)
	if err := os.WriteFile(filepath.Join(te.root, "notes.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := te.run("uninstall", "--yes")
	d := res.data()["config_dir"].(map[string]any)
	if res.exit != 0 || d["status"] != "kept" || !reflect.DeepEqual(d["foreign_entries"], []any{"notes.txt"}) {
		t.Fatalf("exit %d %v", res.exit, d)
	}
	entries, _ := os.ReadDir(te.root)
	if len(entries) != 1 || entries[0].Name() != "notes.txt" {
		t.Fatalf("left %v", entries)
	}
}

func TestUninstallRefusesUnknownSkillsSchema(t *testing.T) {
	isolateHome(t)
	_, te := loggedIn(t)
	if err := os.WriteFile(filepath.Join(te.root, "skills.json"), []byte(`{"schema_version":2,"installs":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res := te.run("uninstall", "--yes")
	if res.exit != 9 || res.errorCode() != "CLI_PROFILE_SCHEMA" || len(te.keyring.items) == 0 {
		t.Fatalf("exit %d code %s", res.exit, res.errorCode())
	}
}
