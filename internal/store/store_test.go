package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const secret = "gd_dat_abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmn"

func newRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if repo := FindGitWorkTree(dir); repo != "" {
		t.Skipf("temp dir %s is inside a git work tree %s", dir, repo)
	}
	return filepath.Join(dir, "gatherdeal")
}

func skipOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission semantics")
	}
}

func TestConfigRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		got, err := ConfigRoot(func(k string) string {
			if k == "APPDATA" {
				return `C:\Users\u\AppData\Roaming`
			}
			return ""
		})
		if err != nil || got != `C:\Users\u\AppData\Roaming\gatherdeal` {
			t.Fatalf("got %q %v", got, err)
		}
		return
	}
	got, err := ConfigRoot(func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return "/x/cfg"
		}
		return ""
	})
	if err != nil || got != "/x/cfg/gatherdeal" {
		t.Fatalf("got %q %v", got, err)
	}
	// A relative XDG_CONFIG_HOME is ignored: the location never depends on
	// the working directory.
	got, err = ConfigRoot(func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return "relative"
		}
		return ""
	})
	if err != nil || !filepath.IsAbs(got) || strings.Contains(got, "relative") {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestFileStoreRoundTripAndModes(t *testing.T) {
	root := newRoot(t)
	fs := NewFileStore(root)
	if err := fs.Preflight(); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Get("acct"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on empty store = %v", err)
	}
	if err := fs.Set("acct", secret); err != nil {
		t.Fatal(err)
	}
	got, err := fs.Get("acct")
	if err != nil || got != secret {
		t.Fatalf("Get = %q, %v", got, err)
	}
	entries, _ := os.ReadDir(fs.Dir)
	if len(entries) != 1 || strings.HasPrefix(entries[0].Name(), ".tmp") {
		t.Fatalf("unexpected files: %v", entries)
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{
			root:                                     0o700,
			fs.Dir:                                   0o700,
			filepath.Join(fs.Dir, entries[0].Name()): 0o600,
		} {
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != want {
				t.Errorf("%s mode %04o, want %04o", path, fi.Mode().Perm(), want)
			}
		}
	}
	if err := fs.Delete("acct"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Delete("acct"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := fs.Get("acct"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v", err)
	}
}

func TestFileStoreRefusesBroadFileMode(t *testing.T) {
	skipOnWindows(t)
	fs := NewFileStore(newRoot(t))
	if err := fs.Set("acct", secret); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fs.Dir, fs.fileName("acct"))
	for _, mode := range []os.FileMode{0o640, 0o604, 0o660} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		_, err := fs.Get("acct")
		var insecure *InsecureError
		if !errors.As(err, &insecure) {
			t.Fatalf("mode %04o: Get = %v, want InsecureError", mode, err)
		}
	}
}

func TestFileStoreRefusesBroadDirMode(t *testing.T) {
	skipOnWindows(t)
	fs := NewFileStore(newRoot(t))
	if err := fs.Set("acct", secret); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fs.Dir, 0o750); err != nil {
		t.Fatal(err)
	}
	var insecure *InsecureError
	if _, err := fs.Get("acct"); !errors.As(err, &insecure) {
		t.Fatalf("Get = %v, want InsecureError", err)
	}
	if err := fs.Set("acct", secret); !errors.As(err, &insecure) {
		t.Fatalf("Set = %v, want InsecureError", err)
	}
	if err := fs.Preflight(); !errors.As(err, &insecure) {
		t.Fatalf("Preflight = %v, want InsecureError", err)
	}
}

func TestFileStoreRefusesSymlink(t *testing.T) {
	skipOnWindows(t)
	fs := NewFileStore(newRoot(t))
	if err := fs.Set("acct", secret); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fs.Dir, fs.fileName("acct"))
	elsewhere := filepath.Join(t.TempDir(), "x.json")
	if err := os.Rename(path, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, path); err != nil {
		t.Fatal(err)
	}
	var insecure *InsecureError
	if _, err := fs.Get("acct"); !errors.As(err, &insecure) {
		t.Fatalf("Get = %v, want InsecureError", err)
	}
}

func TestFileStoreRefusesOtherOwner(t *testing.T) {
	skipOnWindows(t)
	if os.Getuid() != 0 {
		t.Skip("needs root to create a file owned by another user")
	}
	fs := NewFileStore(newRoot(t))
	if err := fs.Set("acct", secret); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fs.Dir, fs.fileName("acct"))
	if err := os.Chown(path, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	var insecure *InsecureError
	if _, err := fs.Get("acct"); !errors.As(err, &insecure) {
		t.Fatalf("Get = %v, want InsecureError", err)
	}
}

func TestFileStoreRefusesGitWorkTree(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "nested", "gatherdeal")
	fs := NewFileStore(root)
	var insecure *InsecureError
	if err := fs.Preflight(); !errors.As(err, &insecure) || !strings.Contains(err.Error(), "git work tree") {
		t.Fatalf("Preflight = %v", err)
	}
	if err := fs.Set("acct", secret); !errors.As(err, &insecure) {
		t.Fatalf("Set = %v", err)
	}
	if _, err := os.Stat(fs.Dir); !os.IsNotExist(err) {
		t.Fatal("credentials directory was created inside a git work tree")
	}
	// A .git file (worktrees, submodules) counts too.
	base2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(base2, ".git"), []byte("gitdir: x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewFileStore(filepath.Join(base2, "g")).Preflight(); !errors.As(err, &insecure) {
		t.Fatalf("Preflight with .git file = %v", err)
	}
}

type fakeKeyring struct {
	items map[string]string
	err   error
}

func (k *fakeKeyring) Set(s, u, p string) error {
	if k.err != nil {
		return k.err
	}
	k.items[s+"|"+u] = p
	return nil
}
func (k *fakeKeyring) Get(s, u string) (string, error) {
	if k.err != nil {
		return "", k.err
	}
	v, ok := k.items[s+"|"+u]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}
func (k *fakeKeyring) Delete(s, u string) error {
	if k.err != nil {
		return k.err
	}
	if _, ok := k.items[s+"|"+u]; !ok {
		return ErrNotFound
	}
	delete(k.items, s+"|"+u)
	return nil
}

func TestKeyringStore(t *testing.T) {
	kr := &fakeKeyring{items: map[string]string{}}
	ks := &KeyringStore{Keyring: kr, ProbeAccount: "probe"}
	if err := ks.Preflight(); err != nil {
		t.Fatal(err)
	}
	if len(kr.items) != 0 {
		t.Fatal("preflight left a probe behind")
	}
	acct := "https://api.example.com/oidc#default"
	if err := ks.Set(acct, secret); err != nil {
		t.Fatal(err)
	}
	if kr.items[KeyringService+"|"+acct] != secret {
		t.Fatal("not stored under the gatherdeal-cli service")
	}
	if v, err := ks.Get(acct); err != nil || v != secret {
		t.Fatalf("Get = %q %v", v, err)
	}
	if err := ks.Delete(acct); err != nil {
		t.Fatal(err)
	}
	if err := ks.Delete(acct); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}

	broken := &KeyringStore{Keyring: &fakeKeyring{err: errors.New("no secret service")}}
	var unavailable *UnavailableError
	if err := broken.Preflight(); !errors.As(err, &unavailable) {
		t.Fatalf("Preflight = %v", err)
	}
}

func TestProfilesRoundTrip(t *testing.T) {
	root := newRoot(t)
	p, err := LoadProfiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if p.Get("default") != nil {
		t.Fatal("empty set has a profile")
	}
	if err := p.EnsureStoreID(); err != nil {
		t.Fatal(err)
	}
	p.Put("default", &Profile{Server: "https://api.example.com", Issuer: "https://api.example.com/oidc",
		Credential: &CredentialRef{Store: KindKeyring, Generation: Generation(strings.Repeat("a", 32))}})
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(root, "profiles.json"))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("profiles.json mode %04o", fi.Mode().Perm())
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, "profiles.json"))
	if strings.Contains(string(data), "gd_dat_") {
		t.Fatal("profiles.json contains a secret")
	}
	p2, err := LoadProfiles(root)
	if err != nil || p2.Get("default").Server != "https://api.example.com" {
		t.Fatalf("reload: %v", err)
	}
	if !ValidProfileName("api.example.com") || ValidProfileName("../x") || ValidProfileName("") || ValidProfileName("Default") {
		t.Fatal("profile name validation")
	}
}
