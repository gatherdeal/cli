package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

// Kind names a credential store.
type Kind string

const (
	// KindKeyring is the OS keyring: macOS Keychain, Windows Credential
	// Manager or the Linux Secret Service. It is the default.
	KindKeyring Kind = "keyring"
	// KindFile is the explicit owner-only file store.
	KindFile Kind = "file"
)

// ParseKind validates a --credential-store value.
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case KindKeyring, KindFile:
		return Kind(s), nil
	}
	return "", fmt.Errorf("unknown credential store %q (want keyring or file)", s)
}

// UnmarshalText accepts only a known store kind, so metadata cannot name
// another store.
func (k *Kind) UnmarshalText(b []byte) error {
	v, err := ParseKind(string(b))
	if err != nil {
		return err
	}
	*k = v
	return nil
}

// KeyringService is the keyring service name of every CLI secret.
const KeyringService = "gatherdeal-cli"

// ErrNotFound means no secret is stored under the account.
var ErrNotFound = errors.New("no stored credential")

// UnavailableError means the store cannot be used on this machine.
type UnavailableError struct {
	Kind Kind
	Err  error
}

func (e *UnavailableError) Error() string {
	return fmt.Sprintf("%s credential store unavailable: %v", e.Kind, e.Err)
}
func (e *UnavailableError) Unwrap() error { return e.Err }

// InsecureError means the file store location or permissions are unsafe.
type InsecureError struct{ Err error }

func (e *InsecureError) Error() string { return "credential file store refused: " + e.Err.Error() }
func (e *InsecureError) Unwrap() error { return e.Err }

// SecretStore stores one secret per account.
type SecretStore interface {
	Kind() Kind
	// Preflight proves the store works before a login starts.
	Preflight() error
	Set(account, secret string) error
	// Get returns ErrNotFound when nothing is stored.
	Get(account string) (string, error)
	// Delete succeeds when nothing is stored.
	Delete(account string) error
}

// Keyring is the minimal OS keyring API; tests substitute a fake.
type Keyring interface {
	Set(service, user, password string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

// OSKeyring is the real OS keyring.
type OSKeyring struct{}

func (OSKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}
func (OSKeyring) Get(service, user string) (string, error) {
	s, err := keyring.Get(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return s, err
}
func (OSKeyring) Delete(service, user string) error {
	err := keyring.Delete(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// KeyringStore stores secrets in a Keyring under KeyringService.
type KeyringStore struct {
	Keyring Keyring
	// ProbeAccount is used by Preflight.
	ProbeAccount string
}

func (s *KeyringStore) Kind() Kind { return KindKeyring }

func (s *KeyringStore) Preflight() error {
	const probe = "gatherdeal-cli preflight"
	account := s.ProbeAccount
	if account == "" {
		account = "preflight"
	}
	if err := s.Keyring.Set(KeyringService, account, probe); err != nil {
		return &UnavailableError{Kind: KindKeyring, Err: err}
	}
	got, err := s.Keyring.Get(KeyringService, account)
	_ = s.Keyring.Delete(KeyringService, account)
	if err != nil {
		return &UnavailableError{Kind: KindKeyring, Err: err}
	}
	if got != probe {
		return &UnavailableError{Kind: KindKeyring, Err: errors.New("keyring returned a different value than stored")}
	}
	return nil
}

func (s *KeyringStore) Set(account, secret string) error {
	if err := s.Keyring.Set(KeyringService, account, secret); err != nil {
		return &UnavailableError{Kind: KindKeyring, Err: err}
	}
	return nil
}

func (s *KeyringStore) Get(account string) (string, error) {
	v, err := s.Keyring.Get(KeyringService, account)
	if errors.Is(err, ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", &UnavailableError{Kind: KindKeyring, Err: err}
	}
	return v, nil
}

func (s *KeyringStore) Delete(account string) error {
	err := s.Keyring.Delete(KeyringService, account)
	if err == nil || errors.Is(err, ErrNotFound) {
		return nil
	}
	return &UnavailableError{Kind: KindKeyring, Err: err}
}

// FileStore keeps one owner-only JSON file per account in Dir.
type FileStore struct {
	// Dir is <config root>/credentials.
	Dir string
}

// NewFileStore returns the file store under root.
func NewFileStore(root string) *FileStore {
	return &FileStore{Dir: filepath.Join(root, "credentials")}
}

func (s *FileStore) Kind() Kind { return KindFile }

type fileSecret struct {
	SchemaVersion int    `json:"schema_version"`
	Account       string `json:"account"`
	Secret        string `json:"secret"`
}

func (s *FileStore) fileName(account string) string {
	sum := sha256.Sum256([]byte(account))
	return hex.EncodeToString(sum[:16]) + ".json"
}

// prepare refuses git work trees and unsafe permissions and creates the
// directory owner-only.
func (s *FileStore) prepare() error {
	if repo := FindGitWorkTree(s.Dir); repo != "" {
		return &InsecureError{Err: fmt.Errorf("%s is inside the git work tree %s; credentials are never written inside a repository (set XDG_CONFIG_HOME to a directory outside any repository)", s.Dir, repo)}
	}
	if err := ensurePrivateDir(filepath.Dir(s.Dir)); err != nil {
		return &InsecureError{Err: err}
	}
	if err := ensurePrivateDir(s.Dir); err != nil {
		return &InsecureError{Err: err}
	}
	return nil
}

func (s *FileStore) Preflight() error {
	if err := s.prepare(); err != nil {
		return err
	}
	if err := writeFileAtomic(s.Dir, ".preflight", []byte("ok")); err != nil {
		return &UnavailableError{Kind: KindFile, Err: err}
	}
	return removeFile(filepath.Join(s.Dir, ".preflight"))
}

func (s *FileStore) Set(account, secret string) error {
	if err := s.prepare(); err != nil {
		return err
	}
	data, err := json.Marshal(fileSecret{SchemaVersion: 1, Account: account, Secret: secret})
	if err != nil {
		return err
	}
	return writeFileAtomic(s.Dir, s.fileName(account), data)
}

func (s *FileStore) Get(account string) (string, error) {
	path := filepath.Join(s.Dir, s.fileName(account))
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotFound
	}
	if repo := FindGitWorkTree(s.Dir); repo != "" {
		return "", &InsecureError{Err: fmt.Errorf("%s is inside the git work tree %s", s.Dir, repo)}
	}
	if err := ensurePrivateDir(s.Dir); err != nil {
		return "", &InsecureError{Err: err}
	}
	if err := checkPrivateFile(path); err != nil {
		return "", &InsecureError{Err: err}
	}
	data, err := readFile(path)
	if err != nil {
		return "", err
	}
	var fsec fileSecret
	if err := json.Unmarshal(data, &fsec); err != nil || fsec.SchemaVersion != 1 || fsec.Account != account {
		return "", fmt.Errorf("%s is not a valid credential file", path)
	}
	return fsec.Secret, nil
}

func (s *FileStore) Delete(account string) error {
	err := removeFile(filepath.Join(s.Dir, s.fileName(account)))
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
