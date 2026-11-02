package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gatherdeal/cli/internal/auth"
	"github.com/gatherdeal/cli/internal/buildinfo"
	"github.com/gatherdeal/cli/internal/httpx"
	"github.com/gatherdeal/cli/internal/lang"
	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/server"
	"github.com/gatherdeal/cli/internal/store"
)

// EnvToken is the only CLI-specific environment variable: a delegated
// access token for the built-in production profile, never persisted.
const EnvToken = "GATHERDEAL_TOKEN"

// expiryWarningWindow is how early the CLI warns about expiry.
const expiryWarningWindow = 7 * 24 * time.Hour

// target is the resolved profile and server of an invocation.
type target struct {
	name       string
	origin     string
	serverFlag bool // --server was given
	production bool // origin is the built-in production server
	builtin    bool // the built-in production profile ("default", no --server)
	disc       server.Discovery
	root       string
	profiles   *store.Profiles
	profile    *store.Profile // nil when the profile does not exist yet
}

// defaultProfileName derives a profile name from a server origin.
func defaultProfileName(origin string) string {
	host := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	host = strings.NewReplacer(":", "-", "[", "", "]", "").Replace(strings.ToLower(host))
	return host
}

// resolveTarget applies the profile/server rules. login allows a missing
// profile and leaves discovery of a new custom server to the caller.
func (inv *Invocation) resolveTarget(login bool) (*target, error) {
	t := &target{}
	name := inv.str("profile")
	if raw := inv.str("server"); raw != "" || inv.isSet("server") {
		origin, err := server.NormalizeOrigin(raw)
		if err != nil {
			return nil, usageError("--server: %v", err)
		}
		t.origin, t.serverFlag = origin, true
		if name == "" {
			if origin == server.ProductionOrigin {
				name = store.DefaultProfile
			} else {
				name = defaultProfileName(origin)
			}
		}
	}
	if name == "" {
		name = store.DefaultProfile
	}
	if !store.ValidProfileName(name) {
		return nil, usageError("--profile %q is not a valid profile name (lower-case letters, digits, '.', '_', '-')", name)
	}
	t.name = name
	inv.Meta.Profile = name
	inv.target = t

	root, err := store.ConfigRoot(inv.Env.Getenv)
	if err != nil {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeInternal, "%v", err)
	}
	t.root = root
	profiles, err := store.LoadProfiles(root)
	if err != nil {
		return nil, storeError(err, t)
	}
	t.profiles = profiles
	t.profile = profiles.Get(name)

	switch {
	case t.origin == "" && t.profile != nil:
		t.origin = t.profile.Server
	case t.origin == "":
		t.origin = server.ProductionOrigin
	}
	if t.profile != nil && t.profile.Server != t.origin {
		return nil, output.Errorf(output.ExitUsage, output.CodeProfileMismatch,
			"profile %q is bound to %s, not %s", name, t.profile.Server, t.origin).
			WithHint("A profile never changes server. Use another --profile name, or `gatherdeal auth logout` that profile first.")
	}
	t.production = t.origin == server.ProductionOrigin
	if name == store.DefaultProfile && !t.production {
		return nil, usageError("the profile name %q is reserved for the built-in production server; choose another --profile", store.DefaultProfile)
	}
	t.builtin = name == store.DefaultProfile && !t.serverFlag
	inv.Meta.Server = t.origin

	// GATHERDEAL_TOKEN belongs to the built-in production profile only.
	if inv.Env.Getenv(EnvToken) != "" && !t.builtin {
		return nil, output.Errorf(output.ExitUsage, output.CodeTokenWithServer,
			"%s is accepted only for the built-in production profile; it cannot be combined with --server or --profile", EnvToken).
			WithHint("Unset " + EnvToken + " or drop --server/--profile.")
	}

	switch {
	case t.production:
		t.disc = server.Production()
	case t.profile != nil && t.profile.Discovery != nil:
		t.disc = *t.profile.Discovery
		if err := t.disc.Validate(t.origin, false); err != nil {
			return nil, output.Errorf(output.ExitUnclassified, output.CodeDiscoveryInvalid, "stored server settings for profile %q are invalid: %v", name, err).
				WithHint("Run `gatherdeal auth logout` for this profile and log in again.")
		}
	case !login:
		return nil, notLoggedIn(t)
	}
	if t.profile != nil && t.disc.Issuer != "" && t.profile.Issuer != t.disc.Issuer {
		return nil, output.Errorf(output.ExitUsage, output.CodeProfileMismatch, "profile %q belongs to issuer %s, not %s", name, t.profile.Issuer, t.disc.Issuer).
			WithHint("Use another --profile name.")
	}

	return t, nil
}

// targetFlagsHint renders the flags that select t in a suggested command.
func (t *target) targetFlagsHint() string {
	var b strings.Builder
	if t.serverFlag && !t.production {
		fmt.Fprintf(&b, " --server %s", t.origin)
	}
	if t.name != store.DefaultProfile && (!t.serverFlag || t.name != defaultProfileName(t.origin)) {
		fmt.Fprintf(&b, " --profile %s", t.name)
	}
	return b.String()
}

func notLoggedIn(t *target) *output.Error {
	return output.Errorf(output.ExitAuth, output.CodeNotLoggedIn, "not logged in to %s (profile %q)", t.origin, t.name).
		WithHint("Run `gatherdeal auth login" + t.targetFlagsHint() + "`.")
}

// authRecovery renders the recovery hint of a credential the server
// rejected. Only the CLI knows how the credential was selected, so the
// hint names the same profile and server as the failed command, or
// GATHERDEAL_TOKEN when that supplied the credential.
func (inv *Invocation) authRecovery(err error) error {
	var rej *auth.CredentialRejectedError
	if !errors.As(err, &rej) || rej.Err.Hint != "" {
		return err
	}
	var why string
	switch rej.Reason {
	case auth.RejectedExpired:
		why = "This GatherDeal access has expired. "
	case auth.RejectedIdle:
		why = "This GatherDeal access stopped working because it was unused for 30 days. "
	case auth.RejectedRevoked:
		why = "This GatherDeal access was revoked. "
	}
	if inv.credentialSource == credentialSourceEnv {
		rej.Err.Hint = why + "The credential came from " + EnvToken + ": set it to a working token, or unset it and run `gatherdeal auth login`."
		return err
	}
	flags := ""
	if inv.target != nil {
		flags = inv.target.targetFlagsHint()
	}
	rej.Err.Hint = why + "Run `gatherdeal auth login" + flags + "` to create a new access."
	return err
}

// secretStore returns the store of kind. The keyring preflight probe uses
// a random account so concurrent logins never race on one probe.
func (inv *Invocation) secretStore(t *target, kind store.Kind) store.SecretStore {
	if kind == store.KindFile {
		return store.NewFileStore(t.root)
	}
	probe := "preflight"
	if g, err := store.NewGeneration(); err == nil {
		probe += "#" + string(g)
	}
	return &store.KeyringStore{Keyring: inv.Env.Keyring, ProbeAccount: probe}
}

// slot is where t's secrets live in the config directory p describes. It
// is always derived from the directory's store id, the validated issuer and
// the profile name, never read from metadata, so neither a tampered
// profiles.json nor another config directory sharing the keyring can point
// this profile at another credential.
func (t *target) slot(p *store.Profiles) (store.Slot, error) {
	s, err := p.Slot(t.disc.Issuer, t.name)
	if err != nil {
		return store.Slot{}, output.Errorf(output.ExitUnclassified, output.CodeInternal, "cannot locate the credentials of profile %q: %v", t.name, err)
	}
	return s, nil
}

// stateTx is one locked change of the local state. Secret writes made
// inside it register how to undo them (run, newest first, if the change
// fails) and what to clean up once profiles.json is committed; both run
// while the lock is still held.
type stateTx struct {
	// slot is the profile's secrets slot in the locked config directory.
	slot     store.Slot
	undo     []func()
	onCommit []func()
}

// Undo registers f to revert a write that actually happened.
func (tx *stateTx) Undo(f func()) { tx.undo = append(tx.undo, f) }

// AfterCommit registers f to run once the metadata is saved.
func (tx *stateTx) AfterCommit(f func()) { tx.onCommit = append(tx.onCommit, f) }

func (tx *stateTx) rollback() {
	for i := len(tx.undo) - 1; i >= 0; i-- {
		tx.undo[i]()
	}
}

// withLock reloads profiles under the interprocess lock, lets fn change
// them (prof is the current profile or nil) and saves the result. If fn or
// the save fails, the registered undos run before the lock is released;
// commit hooks run only after a successful save. If the lock cannot be
// taken, nothing runs and nothing is changed. The config directory gets its
// store id here, on its first locked change.
func (inv *Invocation) withLock(t *target, fn func(tx *stateTx, p *store.Profiles, prof *store.Profile) error) error {
	unlock, err := store.Lock(t.root)
	if err != nil {
		return storeError(err, t)
	}
	defer unlock()
	p, err := store.LoadProfiles(t.root)
	if err != nil {
		return storeError(err, t)
	}
	prof := p.Get(t.name)
	if prof != nil && (prof.Server != t.origin || prof.Issuer != t.disc.Issuer) {
		return output.Errorf(output.ExitUsage, output.CodeProfileMismatch, "profile %q changed to another server while this command ran", t.name)
	}
	if err := p.EnsureStoreID(); err != nil {
		return output.Errorf(output.ExitUnclassified, output.CodeInternal, "cannot create a store id: %v", err)
	}
	slot, err := t.slot(p)
	if err != nil {
		return err
	}
	tx := &stateTx{slot: slot}
	if err := fn(tx, p, prof); err != nil {
		tx.rollback()
		return err
	}
	if err := saveProfiles(p); err != nil {
		tx.rollback()
		return storeError(err, t)
	}
	for _, f := range tx.onCommit {
		f()
	}
	t.profiles, t.profile = p, p.Get(t.name)
	return nil
}

// saveProfiles is a seam for tests that simulate a failing metadata save.
var saveProfiles = func(p *store.Profiles) error { return p.Save() }

// credential is a usable bearer and where it came from.
type credential struct {
	token  string
	source string // keyring | file | environment
}

// credentialSourceEnv is the source of a GATHERDEAL_TOKEN credential.
const credentialSourceEnv = "environment"

// loadCredential returns the credential for t: GATHERDEAL_TOKEN for the
// built-in profile when set, otherwise the stored one. There is never a
// fallback from an invalid environment token to a stored one.
func (inv *Invocation) loadCredential(t *target) (*credential, error) {
	if v := inv.Env.Getenv(EnvToken); v != "" {
		if !t.builtin {
			return nil, output.Errorf(output.ExitUsage, output.CodeTokenWithServer, "%s cannot be combined with --server or --profile", EnvToken)
		}
		inv.credentialSource = credentialSourceEnv
		if !server.ValidToken(v) {
			return nil, output.Errorf(output.ExitUsage, output.CodeTokenInvalid,
				"%s is not a GatherDeal delegated-access token (it must start with %s)", EnvToken, server.TokenPrefix).
				WithHint("Set " + EnvToken + " to a token from `gatherdeal auth login`, or unset it.")
		}
		inv.noteContext(t, false)
		return &credential{token: v, source: credentialSourceEnv}, nil
	}
	secret, ref, err := inv.readStoredCredential(t)
	if err != nil {
		return nil, err
	}
	inv.credentialSource = string(ref.Store)
	inv.Env.addSecret(secret)
	if !server.ValidToken(secret) {
		return nil, output.Errorf(output.ExitAuth, output.CodeTokenInvalid, "the stored credential for profile %q is malformed", t.name).
			WithHint("Run `gatherdeal auth login" + t.targetFlagsHint() + "`.")
	}
	inv.noteContext(t, true)
	inv.noteExpiry(t)
	return &credential{token: secret, source: string(ref.Store)}, nil
}

// readStoredCredential reads the credential generation the committed
// metadata names. Readers do not lock: a renewal commits profiles.json
// before it deletes the previous generation, so a reader that finds its
// generation gone reloads the metadata once and reads the generation that
// replaced it. A secret that no committed metadata names is never read.
func (inv *Invocation) readStoredCredential(t *target) (string, *store.CredentialRef, error) {
	for attempt := 0; ; attempt++ {
		if t.profile == nil || t.profile.Credential == nil {
			return "", nil, notLoggedIn(t)
		}
		ref := t.profile.Credential
		slot, err := t.slot(t.profiles)
		if err != nil {
			return "", nil, err
		}
		secret, err := inv.secretStore(t, ref.Store).Get(slot.CredentialAccount(ref.Generation))
		if err == nil {
			return secret, ref, nil
		}
		if !errors.Is(err, store.ErrNotFound) || attempt > 0 {
			return "", nil, storeError(err, t)
		}
		p, lerr := store.LoadProfiles(t.root)
		if lerr != nil {
			return "", nil, storeError(lerr, t)
		}
		next := p.Get(t.name)
		if next == nil || next.Credential == nil || *next.Credential == *ref ||
			next.Server != t.profile.Server || next.Issuer != t.profile.Issuer {
			return "", nil, storeError(err, t)
		}
		t.profiles, t.profile = p, next
	}
}

// storeError maps credential store failures.
func storeError(err error, t *target) error {
	var schema *store.SchemaError
	var unavailable *store.UnavailableError
	var insecure *store.InsecureError
	switch {
	case errors.As(err, &schema):
		return output.Errorf(output.ExitActionRequired, output.CodeProfileSchema, "%v", schema).WithHint("Use a CLI that supports this " + schema.File + " schema. Do not delete your credentials.").WithDetails(schema)
	case errors.Is(err, store.ErrNotFound):
		return notLoggedIn(t)
	case errors.As(err, &insecure):
		return output.Errorf(output.ExitActionRequired, output.CodeCredentialStoreInsecure, "%v", insecure.Err).
			WithHint("Fix the location or permissions (directory 0700, files 0600, owned by you), then retry.")
	case errors.As(err, &unavailable):
		return output.Errorf(output.ExitActionRequired, output.CodeCredentialStore, "the %s credential store is unavailable: %v", unavailable.Kind, unavailable.Err).
			WithHint("Unlock the OS keyring, or log in with `gatherdeal auth login" + t.targetFlagsHint() + " --credential-store file`.")
	}
	return output.Errorf(output.ExitUnclassified, output.CodeInternal, "credential store error: %v", err)
}

// noteExpiry fills the expiry meta and prints the renewal warning.
func (inv *Invocation) noteExpiry(t *target) {
	if t.profile == nil || t.profile.Access == nil {
		return
	}
	a := t.profile.Access
	inv.Meta.CredentialExpiresAt = output.KnownTime(a.ExpireAt)
	if a.ExpireAt == nil {
		return
	}
	if a.ExpireAt.Sub(inv.Env.now()) <= expiryWarningWindow {
		inv.Meta.CredentialExpiryWarning = true
		org := a.OrganizationName
		if org == "" {
			org = a.OrganizationID
		}
		inv.Env.stderrf("GatherDeal access for %s expires on %s; run `gatherdeal auth login%s` to renew\n",
			org, a.ExpireAt.Local().Format("2006-01-02 15:04 MST"), t.targetFlagsHint())
	}
}

// httpClient returns a client bound to t's origin, with token if non-empty.
// Every request asks for problem text in the invocation's language.
func (inv *Invocation) httpClient(t *target, token string) *http.Client {
	return httpx.New(httpx.Options{
		Origin: t.origin, Token: token, UserAgent: buildinfo.UserAgent(), Base: inv.Env.HTTPBase,
		AcceptLanguage: inv.language(),
	})
}

// language is the Accept-Language value of the invocation: --lang (already
// validated by parse), else the POSIX locale variables, else English.
func (inv *Invocation) language() string {
	var flag string
	if p, ok := inv.strs[flagLang.Name]; ok {
		flag = *p
	}
	loc, err := lang.Resolve(flag, inv.Env.Getenv)
	if err != nil {
		return lang.Default
	}
	return loc
}

// fetchDiscovery downloads and validates a non-default server's discovery
// document.
func (inv *Invocation) fetchDiscovery(ctx context.Context, t *target) (server.Discovery, error) {
	var d server.Discovery
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.origin+server.DiscoveryPath, nil)
	if err != nil {
		return d, output.Errorf(output.ExitUnclassified, output.CodeInternal, "cannot build request").WithCause(err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := inv.httpClient(t, "").Do(req)
	if err != nil {
		return d, httpx.TransportError(ctx, err)
	}
	body, err := httpx.ReadBody(resp)
	if err != nil {
		return d, httpx.TransportError(ctx, err)
	}
	if resp.StatusCode != http.StatusOK {
		exit := output.ExitForStatus(resp.StatusCode)
		if exit == output.ExitNotFound {
			exit = output.ExitUnclassified
		}
		e := output.Errorf(exit, output.CodeDiscoveryInvalid, "%s does not publish a GatherDeal CLI discovery document (HTTP %d)", t.origin, resp.StatusCode)
		e.HTTPStatus, e.Retryable = resp.StatusCode, httpx.GatewayStatus(resp.StatusCode)
		return d, e
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return d, output.Errorf(output.ExitUnclassified, output.CodeDiscoveryInvalid, "the discovery document is not valid: %v", err)
	}
	if err := d.Validate(t.origin, false); err != nil {
		return d, output.Errorf(output.ExitUnclassified, output.CodeDiscoveryInvalid, "the discovery document was rejected: %v", err)
	}
	return d, nil
}

func (inv *Invocation) noteContext(t *target, stored bool) {
	c := &output.Context{Profile: t.name, Server: t.origin, Issuer: t.disc.Issuer,
		Request: output.ContextRequest{OutletIDs: []string{}}}
	// An outlet code is resolved before it becomes a requested UUID selection.
	if outlet := inv.strs["outlet"]; outlet != nil && uuidRE.MatchString(*outlet) {
		c.Request.OutletIDs = []string{*outlet}
	}
	if stored && t.profile != nil && t.profile.Binding != nil {
		b := t.profile.Binding
		c.Binding = &output.ContextBinding{Organization: output.ContextOrganization{ID: b.OrganizationID, Code: b.OrganizationCode, Name: b.OrganizationName}, PrincipalID: b.PrincipalID}
	}
	inv.Meta.Context = c
}
