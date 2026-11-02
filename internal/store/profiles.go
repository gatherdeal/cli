package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/gatherdeal/cli/internal/server"
)

// DefaultProfile is the built-in production profile.
const DefaultProfile = "default"

var profileNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidProfileName reports whether name is an acceptable profile name.
func ValidProfileName(name string) bool { return profileNameRE.MatchString(name) }

// Profile is the non-secret metadata of one login.
type Profile struct {
	Binding *Binding `json:"binding,omitempty"`
	// Server is the canonical origin every credential of this profile is
	// bound to.
	Server string `json:"server"`
	// Issuer never changes for a profile; log in under another profile
	// name instead.
	Issuer string `json:"issuer"`
	// Discovery is the validated discovery document of a non-default
	// server. The built-in production profile uses compiled values.
	Discovery *server.Discovery `json:"discovery,omitempty"`
	// Credential names the committed credential. Readers use only the
	// generation it names.
	Credential *CredentialRef `json:"credential,omitempty"`
	// Access summarizes the delegated access the credential presents.
	Access *AccessSummary `json:"delegated_access,omitempty"`
	// Login is the current, unfinished login attempt. Only the attempt
	// named here may commit a credential; logout and a newer login replace
	// or remove it.
	Login *LoginAttempt `json:"login_attempt,omitempty"`
}

// Binding pins immutable IDs; code and name are display metadata only.
type Binding struct {
	OrganizationID   string `json:"organization_id"`
	OrganizationCode string `json:"organization_code,omitempty"`
	OrganizationName string `json:"organization_name,omitempty"`
	PrincipalID      string `json:"principal_id"`
}

// SchemaError refuses unknown semantics without rewriting the file.
type SchemaError struct {
	Found     int `json:"found"`
	Supported int `json:"supported"`
}

func (e *SchemaError) Error() string {
	return fmt.Sprintf("profiles.json schema %d is unsupported (this binary supports %d); use a CLI supporting that schema; do not delete credentials", e.Found, e.Supported)
}

// CredentialRef names where a profile's credential is stored. The account
// inside the store is never read from metadata: it is always derived from
// the config directory's StoreID, the validated issuer, the profile name
// and Generation (Slot.CredentialAccount).
type CredentialRef struct {
	Store      Kind       `json:"store"`
	Generation Generation `json:"generation"`
}

// LoginAttempt is one started login. Its generation also names the
// credential account the attempt writes on success, so a secret left
// behind by a crash before commit is known and cleaned up by the next
// login or logout of the profile.
type LoginAttempt struct {
	Generation Generation `json:"generation"`
	// Store is the credential store the attempt writes to.
	Store Kind `json:"store"`
	// Device is set for `auth login --no-wait`: the started device
	// authorization `--continue` resumes. Its device code sits in Store
	// under Slot.DeviceCodeAccount(Generation).
	Device *PendingDevice `json:"device,omitempty"`
}

// AccessSummary is what the CLI remembers about an access (no secrets).
type AccessSummary struct {
	ID               string     `json:"id"`
	OrganizationID   string     `json:"organization_id"`
	OrganizationCode string     `json:"organization_code,omitempty"`
	OrganizationName string     `json:"organization_name,omitempty"`
	OutletIDs        []string   `json:"outlet_ids"`
	Permissions      []string   `json:"permissions"`
	Lifetime         string     `json:"lifetime"`
	ExpireAt         *time.Time `json:"expire_at"`
	IdleExpireAt     time.Time  `json:"idle_expire_at"`
	TokenHint        string     `json:"token_hint"`
	LoggedInAt       time.Time  `json:"logged_in_at"`
}

// PendingDevice is the non-secret part of a started device authorization.
type PendingDevice struct {
	UserCode                string    `json:"user_code"`
	VerificationURI         string    `json:"verification_uri"`
	VerificationURIComplete string    `json:"verification_uri_complete,omitempty"`
	ExpiresAt               time.Time `json:"expires_at"`
	Interval                int       `json:"interval"`
}

// validate checks required binding IDs, generations and store kinds.
func (p *Profile) validate() error {
	if p == nil {
		return errors.New("empty profile")
	}
	if b := p.Binding; b != nil && (b.OrganizationID == "" || b.PrincipalID == "") {
		return errors.New("binding without organization or principal ID")
	}
	if c := p.Credential; c != nil && (c.Store == "" || c.Generation == "") {
		return errors.New("credential without store or generation")
	}
	if l := p.Login; l != nil && (l.Store == "" || l.Generation == "") {
		return errors.New("login attempt without store or generation")
	}
	return nil
}

// profilesSchemaVersion is the initial public version of profiles.json.
// Persisted-field additions must raise it to prevent older binaries losing data.
const profilesSchemaVersion = 1

type profilesFile struct {
	SchemaVersion int `json:"schema_version"`
	// StoreID namespaces every credential account of this config directory
	// (Slot), so config directories that share one OS keyring never share a
	// credential. It is created once, by the first locked change.
	StoreID  StoreID             `json:"store_id,omitempty"`
	Profiles map[string]*Profile `json:"profiles"`
}

// Profiles is profiles.json under the config root.
type Profiles struct {
	Root string
	data profilesFile
}

const profilesFileName = "profiles.json"

// LoadProfiles reads profiles.json; a missing file is an empty set.
func LoadProfiles(root string) (*Profiles, error) {
	p := &Profiles{Root: root, data: profilesFile{SchemaVersion: profilesSchemaVersion, Profiles: map[string]*Profile{}}}
	path := filepath.Join(root, profilesFileName)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	// Metadata decides which server a credential is sent to, so it gets
	// the same protection as the secrets.
	if err := checkPrivateDir(root); err != nil {
		return nil, &InsecureError{Err: err}
	}
	if err := checkPrivateFile(path); err != nil {
		return nil, &InsecureError{Err: err}
	}
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	// The version is checked first so an unsupported file is reported as
	// such rather than as corrupt.
	var head struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("%s is corrupt: %w", path, err)
	}
	if head.SchemaVersion != profilesSchemaVersion {
		return nil, &SchemaError{Found: head.SchemaVersion, Supported: profilesSchemaVersion}
	}
	if err := json.Unmarshal(data, &p.data); err != nil {
		return nil, fmt.Errorf("%s is corrupt: %w", path, err)
	}
	if p.data.Profiles == nil {
		p.data.Profiles = map[string]*Profile{}
	}
	if len(p.data.Profiles) > 0 && p.data.StoreID == "" {
		return nil, fmt.Errorf("%s is corrupt: profiles without a store_id", path)
	}
	for name, prof := range p.data.Profiles {
		if !ValidProfileName(name) {
			return nil, fmt.Errorf("%s is corrupt: invalid profile name %q", path, name)
		}
		if err := prof.validate(); err != nil {
			return nil, fmt.Errorf("%s is corrupt: profile %q: %w", path, name, err)
		}
	}
	return p, nil
}

// Get returns the named profile or nil.
func (p *Profiles) Get(name string) *Profile { return p.data.Profiles[name] }

// Put stores or replaces a profile (in memory; call Save).
func (p *Profiles) Put(name string, prof *Profile) { p.data.Profiles[name] = prof }

// Delete removes a profile (in memory; call Save).
func (p *Profiles) Delete(name string) { delete(p.data.Profiles, name) }

// Save writes profiles.json owner-only and atomically. Like every change,
// it runs under Lock; a directory saved without a store id gets one, so a
// saved file always loads.
func (p *Profiles) Save() error {
	if p.data.SchemaVersion != profilesSchemaVersion {
		return &SchemaError{Found: p.data.SchemaVersion, Supported: profilesSchemaVersion}
	}
	// Recheck the on-disk boundary even for a previously loaded instance.
	if _, err := LoadProfiles(p.Root); err != nil {
		return err
	}
	for name, prof := range p.data.Profiles {
		if err := prof.validate(); err != nil {
			return fmt.Errorf("profile %q: %w", name, err)
		}
	}
	if err := ensurePrivateDir(p.Root); err != nil {
		return err
	}
	if err := p.EnsureStoreID(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p.data, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(p.Root, profilesFileName, append(data, '\n'))
}

// EnsureStoreID gives the config directory its StoreID if it has none yet
// (in memory; call Save). It must only be called under Lock.
func (p *Profiles) EnsureStoreID() error {
	if p.data.StoreID != "" {
		return nil
	}
	id, err := newRandomID()
	if err != nil {
		return err
	}
	p.data.StoreID = StoreID(id)
	return nil
}

// Names returns profile names in stable order without changing the store.
func (p *Profiles) Names() []string {
	names := make([]string, 0, len(p.data.Profiles))
	for name := range p.data.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
