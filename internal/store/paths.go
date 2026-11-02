// Package store keeps profile metadata and credentials on the local
// machine: profiles.json (no secrets) and either the OS keyring or an
// explicit, owner-only file store.
package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// Getenv reads an environment variable; tests inject their own.
type Getenv func(string) string

// ConfigRoot returns the CLI's configuration directory:
// %APPDATA%\gatherdeal on Windows, otherwise $XDG_CONFIG_HOME/gatherdeal
// (an absolute XDG_CONFIG_HOME only) or ~/.config/gatherdeal. It never
// depends on the working directory.
func ConfigRoot(getenv Getenv) (string, error) {
	if runtime.GOOS == "windows" {
		appData := getenv("APPDATA")
		if appData == "" || !filepath.IsAbs(appData) {
			return "", errors.New("APPDATA is not set to an absolute path")
		}
		return filepath.Join(appData, "gatherdeal"), nil
	}
	if xdg := getenv("XDG_CONFIG_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "gatherdeal"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", errors.New("cannot determine the home directory; set XDG_CONFIG_HOME to an absolute path")
	}
	return filepath.Join(home, ".config", "gatherdeal"), nil
}

// FindGitWorkTree returns the first ancestor of dir (or dir itself) that
// contains a .git entry, or "" when there is none. It checks both the path
// as written and the path with symlinks resolved, so a symlinked
// XDG_CONFIG_HOME pointing into a repository is caught.
func FindGitWorkTree(dir string) string {
	p, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	if repo := findGit(p); repo != "" {
		return repo
	}
	if resolved := resolveExisting(p); resolved != p {
		return findGit(resolved)
	}
	return ""
}

// resolveExisting resolves symlinks in the deepest existing ancestor of p
// and re-appends the components that do not exist yet.
func resolveExisting(p string) string {
	rest := ""
	cur := p
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func findGit(p string) string {
	for {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		p = parent
	}
}
