package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gatherdeal/cli/internal/buildinfo"
	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/store"
)

// Paths are relative to either the project directory or the user's home.
// "agents" is the shared agent-skills directory read by Codex and other agents.
var skillTargets = map[string]string{"claude": ".claude/skills", "agents": ".agents/skills"}

func skillTargetNames() []string {
	names := make([]string, 0, len(skillTargets))
	for name := range skillTargets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func skillsInstallCommand() *Command {
	return &Command{
		Path: []string{"skills", "install"}, Summary: "Install the GatherDeal agent skill at an explicit location.",
		Output: "json", Run: runSkillsInstall,
		Flags: []FlagSpec{
			{Name: "dir", Kind: kindString, Usage: "Parent directory in which to install gatherdeal/; ancestor symlinks are followed. Cannot be combined with --target or --scope."},
			{Name: "target", Kind: kindString, Values: skillTargetNames(), Usage: "Agent tool to install for: claude (.claude/skills) or agents (.agents/skills, read by Codex and other agents); requires --scope. Cannot be combined with --dir."},
			{Name: "scope", Kind: kindString, Values: []string{"project", "user"}, Usage: "Install relative to the current directory (project) or home directory (user); requires --target."},
			{Name: "force", Kind: kindBool, Usage: "Overwrite skill files whose content differs."},
		},
	}
}

// skillFile is one file of the generated skill, relative to its directory.
type skillFile struct{ Path, Content string }

// skillFiles is the generated skill: SKILL.md with the shared guidance, the
// app index and the core commands, plus one resource index per app.
func skillFiles() []skillFile {
	root := commandTree(Commands())
	files := []skillFile{{Path: "SKILL.md", Content: skillMarkdownFor(root)}}
	for _, app := range skillApps(root) {
		files = append(files, skillFile{Path: appReferencePath(app), Content: appReference(app)})
	}
	return files
}

// skillApps returns the top-level groups whose commands call an app API.
func skillApps(root *commandNode) []*commandNode {
	var apps []*commandNode
	for _, name := range root.choices() {
		if n := root.children[name]; n.app != "" {
			apps = append(apps, n)
		}
	}
	return apps
}

func appReferencePath(app *commandNode) string { return "references/" + app.path[0] + ".md" }

func skillMarkdown() string { return skillMarkdownFor(commandTree(Commands())) }

func skillMarkdownFor(root *commandNode) string {
	apps := skillApps(root)
	names := make([]string, len(apps))
	for i, app := range apps {
		names[i] = app.path[0]
	}
	return fmt.Sprintf("---\nname: gatherdeal\ndescription: Reads and manages GatherDeal business data through the gatherdeal CLI, within the organization and outlets a person approved, in the apps %s. It is used when a person asks about their GatherDeal data, wants to change it, or wants to log the CLI in.\nmetadata:\n  cli_version: %q\n  skill_schema: 1\n---\n\n", strings.Join(names, ", "), buildinfo.CurrentVersion()) +
		agentContext(root)
}

func runSkillsShow(context.Context, *Invocation) (any, error) { return skillMarkdown(), nil }

type skillFileResult struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

type skillInstallResult struct {
	Dir        string            `json:"dir"`
	CLIVersion string            `json:"cli_version"`
	Files      []skillFileResult `json:"files"`
}

func skillDirectory(inv *Invocation) (string, error) {
	if inv.isSet("dir") == inv.isSet("target") {
		return "", usageError("exactly one of --dir or --target is required")
	}
	base := inv.str("dir")
	if inv.isSet("dir") {
		if inv.isSet("scope") {
			return "", usageError("--scope cannot be combined with --dir")
		}
		if base == "" {
			return "", usageError("--dir must not be empty")
		}
	} else {
		if !inv.isSet("scope") {
			return "", usageError("--scope is required with --target")
		}
		root, err := os.Getwd()
		if inv.str("scope") == "user" {
			root, err = os.UserHomeDir()
		}
		if err != nil {
			return "", err
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return "", err
		}
		root, err = filepath.Abs(root)
		if err != nil {
			return "", err
		}
		dest := filepath.Join(root, skillTargets[inv.str("target")], "gatherdeal")
		if err := checkSkillDirectory(dest); err != nil {
			return "", err
		}
		resolved, err := resolveSkillAncestor(dest)
		if err != nil {
			return "", output.Errorf(output.ExitUsage, output.CodeSkillPathInvalid, "cannot resolve skill path %q", dest).
				WithDetails(skillPathDetails{Path: dest}).WithCause(err)
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return "", output.Errorf(output.ExitUsage, output.CodeSkillPathInvalid, "skill path %q resolves outside its scope", dest).
				WithDetails(skillPathDetails{Path: dest})
		}
		return resolved, nil
	}
	return filepath.Abs(filepath.Join(base, "gatherdeal"))
}

// resolveSkillAncestor resolves the longest existing ancestor, then appends
// missing components. Lstat lets EvalSymlinks reject dangling links and loops.
func resolveSkillAncestor(path string) (string, error) {
	if _, err := os.Lstat(path); err == nil {
		return filepath.EvalSymlinks(path)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent, err := resolveSkillAncestor(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

type skillConflictDetails struct {
	Paths []string `json:"paths"`
	Dir   string   `json:"dir"`
}

type skillPathDetails struct {
	Path string `json:"path"`
}

func checkSkillDirectory(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return output.Errorf(output.ExitUsage, output.CodeSkillPathInvalid, "skill directory %q must be a directory, not a symlink or other file", path).
			WithDetails(skillPathDetails{Path: path})
	}
	return nil
}

func runSkillsInstall(_ context.Context, inv *Invocation) (any, error) {
	dir, err := skillDirectory(inv)
	if err != nil {
		return nil, err
	}
	for _, path := range []string{dir, filepath.Join(dir, "references")} {
		if err := checkSkillDirectory(path); err != nil {
			return nil, err
		}
	}
	result := skillInstallResult{Dir: dir, CLIVersion: buildinfo.CurrentVersion()}
	files := skillFiles()
	var conflicts []string
	// Preflight every file before creating directories or writing any content.
	for _, file := range files {
		rel := file.Path
		path := filepath.Join(dir, filepath.FromSlash(rel))
		action := "created"
		info, err := os.Lstat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return nil, output.Errorf(output.ExitUsage, output.CodeSkillPathInvalid, "skill file %q must be a regular file, not a symlink or other file", path).
					WithDetails(skillPathDetails{Path: path})
			}
			old, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			action = "unchanged"
			if string(old) != file.Content {
				if !inv.boolean("force") {
					conflicts = append(conflicts, rel)
				}
				action = "updated"
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		result.Files = append(result.Files, skillFileResult{Path: rel, Action: action})
	}
	if len(conflicts) > 0 {
		return nil, output.Errorf(output.ExitConflict, output.CodeSkillConflict, "skill files have different content").
			WithDetails(skillConflictDetails{Paths: conflicts, Dir: dir}).
			WithHint("Re-run with --force to overwrite.")
	}
	// Record the location before writing, so uninstall finds it even if a
	// write fails part way.
	if err := inv.recordSkillInstall(dir); err != nil {
		return nil, err
	}
	for i, file := range result.Files {
		if file.Action == "unchanged" {
			continue
		}
		path := filepath.Join(dir, filepath.FromSlash(file.Path))
		// Skill documentation is public and intentionally readable by other users.
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		} // #nosec G301
		if err := writeSkillFile(path, files[i].Content); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// recordSkillInstall adds dir, resolved, to the skill locations kept in the
// config directory (skills.json) for `gatherdeal uninstall`.
func (inv *Invocation) recordSkillInstall(dir string) error {
	resolved, err := resolveSkillAncestor(dir)
	if err != nil {
		return output.Errorf(output.ExitUsage, output.CodeSkillPathInvalid, "cannot resolve skill path %q", dir).
			WithDetails(skillPathDetails{Path: dir}).WithCause(err)
	}
	in := store.SkillInstall{Path: filepath.Clean(resolved)}
	if inv.isSet("target") {
		in.Target, in.Scope = inv.str("target"), inv.str("scope")
	}
	root, err := store.ConfigRoot(inv.Env.Getenv)
	if err != nil {
		return output.Errorf(output.ExitUnclassified, output.CodeInternal, "%v", err)
	}
	t := &target{root: root}
	unlock, err := store.Lock(root)
	if err != nil {
		return storeError(err, t)
	}
	defer unlock()
	installs, err := store.LoadSkillInstalls(root)
	if err != nil {
		return storeError(err, t)
	}
	if err := installs.Add(in); err != nil {
		return output.Errorf(output.ExitUnclassified, output.CodeInternal, "cannot record the skill location: %v", err)
	}
	if err := installs.Save(); err != nil {
		return storeError(err, t)
	}
	return nil
}

func writeSkillFile(path, content string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".gatherdeal-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return err
	}
	// Skill documentation contains no secrets.
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return err
	} // #nosec G302
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
