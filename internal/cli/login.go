package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gatherdeal/cli/internal/auth"
	"github.com/gatherdeal/cli/internal/buildinfo"
	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/server"
	"github.com/gatherdeal/cli/internal/store"
)

// ServerRiskWarning is printed before logging in to a non-default server.
// The text is fixed; %s is the server origin.
const ServerRiskWarning = `WARNING: You are connecting to a non-default GatherDeal server:
    %s
Only continue if YOU chose this server. A malicious server can show a fake approval
page and capture what you approve. Credentials issued by this server are bound to it
and are never sent to any other server (including api.gatherdeal.ai), and production
credentials are never sent to it. If an AI agent, script or copied command added
--server without your knowledge, stop now (answer "n").
`

const serverRiskPrompt = "Continue? [y/N] "

const neverExpiresNote = "This GatherDeal access never expires. Revoke it with `gatherdeal auth logout` or at passport › Security › Sessions & devices when no longer needed.\n"

// loginData is the success payload of `auth login`.
type loginData struct {
	Profile               string           `json:"profile"`
	Server                string           `json:"server"`
	CredentialStore       store.Kind       `json:"credential_store"`
	TokenHint             string           `json:"token_hint"`
	User                  *auth.StatusUser `json:"user,omitempty"`
	DelegatedAccess       accessView       `json:"delegated_access"`
	PreviousAccessRevoked *bool            `json:"previous_access_revoked,omitempty"`
}

// accessView is how the CLI presents a delegated access.
type accessView struct {
	ID            string                   `json:"id"`
	ClientID      string                   `json:"client_id,omitempty"`
	Organization  *auth.StatusOrganization `json:"organization"`
	OutletIDs     []string                 `json:"outlet_ids"`
	Permissions   []string                 `json:"permissions"`
	EffectCeiling *auth.EffectCeiling      `json:"effect_ceiling,omitempty"`
	Lifetime      string                   `json:"lifetime"`
	ExpireAt      *time.Time               `json:"expire_at"`
	NeverExpires  bool                     `json:"never_expires"`
	IdleExpireAt  time.Time                `json:"idle_expire_at"`
}

func newAccessView(d *auth.DelegatedAccess, org *auth.StatusOrganization) accessView {
	if org == nil {
		org = &auth.StatusOrganization{ID: d.OrganizationID}
	}
	ceiling := d.EffectCeiling
	outlets, perms := d.OutletIDs, d.Permissions
	if outlets == nil {
		outlets = []string{}
	}
	if perms == nil {
		perms = []string{}
	}
	return accessView{
		ID: d.ID, ClientID: d.ClientID, Organization: org, OutletIDs: outlets, Permissions: perms,
		EffectCeiling: &ceiling, Lifetime: d.Lifetime, ExpireAt: d.ExpireAt, NeverExpires: d.ExpireAt == nil,
		IdleExpireAt: d.IdleExpireAt,
	}
}

func runLogin(ctx context.Context, inv *Invocation) (any, error) {
	if inv.Env.Getenv(EnvToken) != "" {
		return nil, output.Errorf(output.ExitUsage, output.CodeTokenWithServer, "%s is set; unset it before `gatherdeal auth login`", EnvToken)
	}
	lifetime := inv.str("lifetime")
	if lifetime != "" && !auth.ValidLifetime(lifetime) {
		return nil, usageError("--lifetime must be one of %s", strings.Join(auth.Lifetimes, ", "))
	}
	if inv.isSet("accept-server-risk") && !inv.isSet("server") {
		return nil, usageError("--accept-server-risk is only valid together with --server")
	}
	if inv.boolean("continue") && inv.boolean("no-wait") {
		return nil, usageError("--continue and --no-wait cannot be combined")
	}
	t, err := inv.resolveTarget(true)
	if err != nil {
		return nil, err
	}
	if inv.boolean("continue") {
		return inv.continueLogin(ctx, t)
	}

	if !t.production {
		if err := inv.confirmServerRisk(t); err != nil {
			return nil, err
		}
		disc, err := inv.fetchDiscovery(ctx, t)
		if err != nil {
			return nil, err
		}
		t.disc = disc
		if t.profile != nil && t.profile.Issuer != disc.Issuer {
			return nil, output.Errorf(output.ExitUsage, output.CodeProfileMismatch, "profile %q belongs to issuer %s, not %s", t.name, t.profile.Issuer, disc.Issuer)
		}
	}
	if t.disc.RequiresNewerCLI(buildinfo.CurrentVersion()) {
		return nil, output.Errorf(output.ExitActionRequired, output.CodeUpgradeRequired,
			"%s requires gatherdeal %s or newer (this is %s)", t.origin, t.disc.MinCLIVersion, buildinfo.CurrentVersion()).
			WithHint("Run `gatherdeal update`, then log in again.")
	}

	kind, err := store.ParseKind(inv.str("credential-store"))
	if err != nil {
		return nil, usageError("--credential-store: %v", err)
	}
	secrets, err := inv.preflight(t, kind)
	if err != nil {
		return nil, err
	}

	device := inv.device(t)
	deviceName := inv.str("device-name")
	if deviceName == "" && inv.Env.Hostname != nil {
		if h, err := inv.Env.Hostname(); err == nil && h != "" {
			deviceName = "gatherdeal CLI on " + h
		}
	}
	da, err := device.Authorize(ctx, auth.ScopeHints(lifetime, auth.SanitizeDeviceName(deviceName)))
	if err != nil {
		return nil, err
	}
	inv.Env.addSecret(da.DeviceCode)
	expiresAt := inv.Env.now().Add(time.Duration(da.ExpiresIn) * time.Second)
	inv.printInstructions(t, da.VerificationURI, da.VerificationURIComplete, da.UserCode, expiresAt)

	noWait := inv.boolean("no-wait")
	gen, err := inv.beginLogin(t, secrets, da, expiresAt, noWait)
	if err != nil {
		return nil, err
	}
	if noWait {
		return nil, pendingError(t, da, expiresAt)
	}
	inv.Env.stderrf("Waiting for approval (Ctrl-C to cancel)...\n")
	tr, err := device.Poll(ctx, da.DeviceCode, time.Duration(da.Interval)*time.Second, expiresAt)
	if err != nil {
		// Nobody else can claim this attempt (its device code was never
		// stored), so it ends here whatever the failure.
		inv.dropLogin(t, gen)
		return nil, loginRetryHint(t, err, false)
	}
	return inv.finishLogin(ctx, t, secrets, tr, gen)
}

func (inv *Invocation) device(t *target) *auth.Device {
	return &auth.Device{HTTP: inv.httpClient(t, ""), Discovery: t.disc, Production: t.production, Sleep: inv.Env.Sleep, Now: inv.Env.Now}
}

// confirmServerRisk shows the non-default server warning and requires an
// interactive "y" or an exact --accept-server-risk origin.
func (inv *Invocation) confirmServerRisk(t *target) error {
	inv.Env.stderrf(ServerRiskWarning, t.origin)
	if inv.isSet("accept-server-risk") {
		// Exact string match: no case, whitespace, slash or port
		// normalization of what the user typed.
		if inv.str("accept-server-risk") != t.origin {
			return usageError("--accept-server-risk must be exactly the server origin %s", t.origin)
		}
		inv.Env.stderrf("Accepted with --accept-server-risk %s.\n", t.origin)
		return nil
	}
	if !inv.Env.Interactive {
		return output.Errorf(output.ExitActionRequired, output.CodeServerRiskNotAccepted,
			"logging in to the non-default server %s needs confirmation", t.origin).
			WithHint(fmt.Sprintf("Only if YOU chose this server, run the command again with --accept-server-risk %s.", t.origin)).
			WithDetails(map[string]string{"server": t.origin})
	}
	if !inv.Env.confirm(serverRiskPrompt) {
		return output.Errorf(output.ExitActionRequired, output.CodeServerRiskDeclined, "login to %s cancelled", t.origin)
	}
	return nil
}

// preflight proves the credential store works before the device flow, so
// the user never approves a login whose credential cannot be saved.
func (inv *Invocation) preflight(t *target, kind store.Kind) (store.SecretStore, error) {
	secrets := inv.secretStore(t, kind)
	err := secrets.Preflight()
	if err == nil {
		return secrets, nil
	}
	var unavailable *store.UnavailableError
	if kind == store.KindKeyring && errors.As(err, &unavailable) {
		fileHint := "gatherdeal auth login" + t.targetFlagsHint() + inv.riskFlagHint(t) + " --credential-store file"
		if inv.Env.Interactive {
			dir := store.NewFileStore(t.root).Dir
			inv.Env.stderrf("The OS keyring is unavailable (%v).\n", unavailable.Err)
			if inv.Env.confirm("Store the credential in an owner-only file in %s instead? Same-user programs can read it. [y/N] ", dir) {
				return inv.preflight(t, store.KindFile)
			}
		}
		return nil, output.Errorf(output.ExitActionRequired, output.CodeCredentialStore,
			"the OS keyring is unavailable: %v", unavailable.Err).
			WithHint("Unlock or install an OS keyring, or explicitly choose the file store: `" + fileHint + "`.").
			WithDetails(map[string]string{"file_store_command": fileHint})
	}
	return nil, storeError(err, t)
}

func (inv *Invocation) riskFlagHint(t *target) string {
	if t.production || !t.serverFlag {
		return ""
	}
	return " --accept-server-risk " + t.origin
}

func (inv *Invocation) printInstructions(t *target, uri, complete, code string, expiresAt time.Time) {
	host := uri
	if u, err := url.Parse(uri); err == nil {
		host = u.Host
	}
	inv.Env.stderrf("\nTo approve this login, open the approval page on %s:\n    %s\n", strings.ToUpper(host), uri)
	inv.Env.stderrf("and check that it shows this code:\n    %s\n", code)
	if complete != "" {
		inv.Env.stderrf("(Direct link: %s )\n", complete)
	}
	inv.Env.stderrf("Only approve if you started this login yourself. The code expires at %s.\n",
		expiresAt.Local().Format("15:04 MST"))
	if !t.production {
		inv.Env.stderrf("Server: %s (non-default)\n", t.origin)
	}
}

// beginLogin records a new login attempt for t and returns its generation;
// only this attempt may commit a credential until a logout or a newer
// login replaces it. A --no-wait attempt also stores its device code. The
// attempt it replaces is cleaned up once the new record is saved: its
// device code, and a credential it may have written before a crash.
func (inv *Invocation) beginLogin(t *target, secrets store.SecretStore, da *auth.DeviceAuthorization, expiresAt time.Time, noWait bool) (store.Generation, error) {
	gen, err := store.NewGeneration()
	if err != nil {
		return "", output.Errorf(output.ExitUnclassified, output.CodeInternal, "cannot create a login id: %v", err)
	}
	err = inv.withLock(t, func(tx *stateTx, p *store.Profiles, prof *store.Profile) error {
		if prof == nil {
			prof = &store.Profile{Server: t.origin, Issuer: t.disc.Issuer}
		}
		if !t.production {
			d := t.disc
			prof.Discovery = &d
		}
		attempt := &store.LoginAttempt{Generation: gen, Store: secrets.Kind()}
		if noWait {
			account := tx.slot.DeviceCodeAccount(gen)
			if err := secrets.Set(account, da.DeviceCode); err != nil {
				return storeError(err, t)
			}
			tx.Undo(func() { _ = secrets.Delete(account) })
			attempt.Device = &store.PendingDevice{
				UserCode: da.UserCode, VerificationURI: da.VerificationURI,
				VerificationURIComplete: da.VerificationURIComplete,
				ExpiresAt:               expiresAt.UTC(), Interval: da.Interval,
			}
		}
		inv.discardAttempt(t, tx, prof, prof.Login, true)
		prof.Login = attempt
		p.Put(t.name, prof)
		return nil
	})
	return gen, err
}

// discardAttempt schedules, for after the commit, the removal of every
// secret the replaced attempt old may own: its device code and the
// credential it would have committed (left behind if a process died
// between writing it and saving the metadata). With revoke, such a
// credential is also revoked on the server. The committed credential is
// never touched.
func (inv *Invocation) discardAttempt(t *target, tx *stateTx, prof *store.Profile, old *store.LoginAttempt, revoke bool) {
	if old == nil || (prof.Credential != nil && prof.Credential.Generation == old.Generation) {
		return
	}
	secrets := inv.secretStore(t, old.Store)
	device, cred := tx.slot.DeviceCodeAccount(old.Generation), tx.slot.CredentialAccount(old.Generation)
	tx.AfterCommit(func() {
		_ = secrets.Delete(device)
		// A credential here was issued but never committed: nobody holds
		// it, so it is revoked as well as deleted.
		if orphan, err := secrets.Get(cred); err == nil && revoke {
			inv.Env.addSecret(orphan)
			inv.revokeQuietly(context.Background(), t, orphan)
		}
		_ = secrets.Delete(cred)
	})
}

// pendingDetails is the payload of the "approval pending" envelope.
type pendingDetails struct {
	Server                  string    `json:"server"`
	Profile                 string    `json:"profile"`
	VerificationURI         string    `json:"verification_uri"`
	VerificationURIComplete string    `json:"verification_uri_complete,omitempty"`
	ApprovalHost            string    `json:"approval_host"`
	UserCode                string    `json:"user_code"`
	ExpiresAt               time.Time `json:"expires_at"`
	ContinueCommand         string    `json:"continue_command"`
}

func pendingError(t *target, da *auth.DeviceAuthorization, expiresAt time.Time) error {
	host := da.VerificationURI
	if u, err := url.Parse(da.VerificationURI); err == nil {
		host = u.Host
	}
	cont := "gatherdeal auth login --continue" + t.targetFlagsHint()
	return output.Errorf(output.ExitActionRequired, output.CodeLoginPending,
		"waiting for a person to approve the login code %s at %s", da.UserCode, da.VerificationURI).
		WithHint("Show the person the approval page and the code exactly as given. After they approve, run `" + cont + "`.").
		WithDetails(pendingDetails{
			Server: t.origin, Profile: t.name, VerificationURI: da.VerificationURI,
			VerificationURIComplete: da.VerificationURIComplete, ApprovalHost: host,
			UserCode: da.UserCode, ExpiresAt: expiresAt.UTC(), ContinueCommand: cont,
		})
}

// dropLogin ends the login attempt gen, but only while it is still the
// profile's current one (a newer login or a logout may have replaced it).
// A profile that only held the attempt is removed.
func (inv *Invocation) dropLogin(t *target, gen store.Generation) {
	_ = inv.withLock(t, func(tx *stateTx, p *store.Profiles, prof *store.Profile) error {
		if prof == nil || prof.Login == nil || prof.Login.Generation != gen {
			return nil
		}
		inv.discardAttempt(t, tx, prof, prof.Login, true)
		prof.Login = nil
		if prof.Credential == nil {
			p.Delete(t.name)
		}
		return nil
	})
}

// loginRetryHint words the hint of a failed device login so that retrying
// targets the same profile and server.
func loginRetryHint(t *target, err error, noWait bool) error {
	var e *output.Error
	if !errors.As(err, &e) {
		return err
	}
	cmd := "gatherdeal auth login"
	if noWait {
		cmd += " --no-wait"
	}
	cmd += t.targetFlagsHint()
	switch e.Code {
	case output.CodeLoginExpired:
		e.Hint = "Run `" + cmd + "` again and approve the new code before it expires."
	case output.CodeLoginRestart:
		e.Hint = "Run `" + cmd + "` again."
	case output.CodeLoginDenied:
		e.Hint = "Ask the person who approves logins; run `" + cmd + "` to try again."
	}
	return err
}

func (inv *Invocation) continueLogin(ctx context.Context, t *target) (any, error) {
	if t.profile == nil || t.profile.Login == nil || t.profile.Login.Device == nil {
		return nil, usageError("there is no pending login for profile %q; run `gatherdeal auth login --no-wait%s` first", t.name, t.targetFlagsHint())
	}
	attempt := *t.profile.Login
	pd := *attempt.Device
	if pd.Interval < 0 || pd.Interval > auth.MaxInterval {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeInternal, "the pending login record is malformed").
			WithHint("Run `gatherdeal auth login --no-wait" + t.targetFlagsHint() + "` again.")
	}
	if !inv.Env.now().Before(pd.ExpiresAt) {
		inv.dropLogin(t, attempt.Generation)
		return nil, output.Errorf(output.ExitAuth, output.CodeLoginExpired, "the pending login code expired").
			WithHint("Run `gatherdeal auth login --no-wait" + t.targetFlagsHint() + "` again.")
	}
	slot, err := t.slot(t.profiles)
	if err != nil {
		return nil, err
	}
	secrets := inv.secretStore(t, attempt.Store)
	deviceCode, err := secrets.Get(slot.DeviceCodeAccount(attempt.Generation))
	if err != nil {
		return nil, storeError(err, t)
	}
	inv.Env.addSecret(deviceCode)
	inv.Env.stderrf("Waiting for approval of code %s at %s (Ctrl-C to cancel)...\n", pd.UserCode, pd.VerificationURI)
	tr, err := inv.device(t).Poll(ctx, deviceCode, time.Duration(pd.Interval)*time.Second, pd.ExpiresAt)
	if err != nil {
		var e *output.Error
		if errors.As(err, &e) && e.Exit != output.ExitInterrupted && e.Exit != output.ExitUnavailable {
			inv.dropLogin(t, attempt.Generation)
		}
		return nil, loginRetryHint(t, err, true)
	}
	return inv.finishLogin(ctx, t, secrets, tr, attempt.Generation)
}

// errLoginSuperseded rejects a token whose login attempt is no longer
// current.
func errLoginSuperseded(t *target) *output.Error {
	return output.Errorf(output.ExitAuth, output.CodeLoginRestart,
		"this login was cancelled by a logout or replaced by a newer login for profile %q; the access it received was revoked", t.name).
		WithHint("If you still want to log in and no other login is in progress, run `gatherdeal auth login" + t.targetFlagsHint() + "` again.")
}

// finishLogin commits the credential of the login attempt gen: under the
// lock, and only while gen is still the profile's current attempt, it
// writes the token to a new generation-qualified account, saves metadata
// naming that generation, and then deletes the previous generation. A token
// for an attempt that a logout or newer login replaced is rejected and
// revoked. The previous access is revoked once the new one is committed.
func (inv *Invocation) finishLogin(ctx context.Context, t *target, secrets store.SecretStore, tr *auth.TokenResponse, gen store.Generation) (any, error) {
	inv.Env.addSecret(tr.AccessToken)
	inv.noteContext(t, true)

	// Resolve the principal reliably before committing any credential.
	statusCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	st, statusErr := auth.Status(statusCtx, inv.httpClient(t, tr.AccessToken), t.disc.StatusEndpoint, t.disc.ClientID)
	cancel()
	if statusErr == nil && (st.User == nil || strings.TrimSpace(st.User.ID) == "" || st.Organization == nil ||
		st.Organization.ID != tr.DelegatedAccess.OrganizationID || st.DelegatedAccess.ID != tr.DelegatedAccess.ID ||
		st.DelegatedAccess.OrganizationID != tr.DelegatedAccess.OrganizationID) {
		statusErr = output.Errorf(output.ExitUnclassified, output.CodeProtocol, "login status must identify the principal and match the issued grant and organization")
	}
	if statusErr != nil {
		inv.revokeQuietly(ctx, t, tr.AccessToken)
		inv.dropLogin(t, gen)
		return nil, statusErr
	}
	user, org := st.User, st.Organization

	var oldToken string
	err := inv.withLock(t, func(tx *stateTx, p *store.Profiles, prof *store.Profile) error {
		if prof == nil || prof.Login == nil || prof.Login.Generation != gen {
			return errLoginSuperseded(t)
		}
		if prof.Login.Store != secrets.Kind() {
			return output.Errorf(output.ExitUnclassified, output.CodeInternal, "the login attempt changed credential store")
		}
		// The persisted binding remains with the profile throughout --no-wait;
		// checking it under this lock also covers concurrent --continue calls.
		if prof.Credential != nil && prof.Binding == nil {
			return profileBindingMismatch(t, output.CodeProfilePrincipalMismatch, "", user.ID)
		}
		if binding := prof.Binding; binding != nil {
			if binding.OrganizationID != tr.DelegatedAccess.OrganizationID {
				return profileBindingMismatch(t, output.CodeProfileOrganizationMismatch, binding.OrganizationID, tr.DelegatedAccess.OrganizationID)
			}
			if binding.PrincipalID != user.ID {
				return profileBindingMismatch(t, output.CodeProfilePrincipalMismatch, binding.PrincipalID, user.ID)
			}
		}
		// Read the previous credential (to revoke it once replaced) before
		// anything is written.
		var oldStore store.SecretStore
		var oldAccount string
		if ref := prof.Credential; ref != nil {
			oldStore, oldAccount = inv.secretStore(t, ref.Store), tx.slot.CredentialAccount(ref.Generation)
			v, err := oldStore.Get(oldAccount)
			switch {
			case err == nil:
				oldToken = v
				inv.Env.addSecret(v)
			case !errors.Is(err, store.ErrNotFound):
				// Never replace a credential that could not be read back.
				return storeError(err, t)
			}
		}
		account := tx.slot.CredentialAccount(gen)
		if err := secrets.Set(account, tr.AccessToken); err != nil {
			return storeError(err, t) // nothing was written
		}
		tx.Undo(func() { _ = secrets.Delete(account) })
		// The previous generation goes only once metadata names the new one.
		if oldStore != nil {
			tx.AfterCommit(func() { _ = oldStore.Delete(oldAccount) })
		}
		if prof.Login.Device != nil {
			device := tx.slot.DeviceCodeAccount(gen)
			tx.AfterCommit(func() { _ = secrets.Delete(device) })
		}
		prof.Server, prof.Issuer = t.origin, t.disc.Issuer
		if !t.production {
			d := t.disc
			prof.Discovery = &d
		} else {
			prof.Discovery = nil
		}
		prof.Credential = &store.CredentialRef{Store: secrets.Kind(), Generation: gen}
		prof.Login = nil
		summary := &store.AccessSummary{
			ID: tr.DelegatedAccess.ID, OrganizationID: tr.DelegatedAccess.OrganizationID,
			OutletIDs: tr.DelegatedAccess.OutletIDs, Permissions: tr.DelegatedAccess.Permissions,
			Lifetime: tr.DelegatedAccess.Lifetime, ExpireAt: tr.DelegatedAccess.ExpireAt,
			IdleExpireAt: tr.DelegatedAccess.IdleExpireAt, TokenHint: server.TokenHint(tr.AccessToken),
			LoggedInAt: inv.Env.now().UTC(),
		}
		if org != nil {
			summary.OrganizationCode, summary.OrganizationName = org.Code, org.Name
		}
		prof.Access = summary
		prof.Binding = &store.Binding{OrganizationID: org.ID, OrganizationCode: org.Code, OrganizationName: org.Name, PrincipalID: user.ID}
		p.Put(t.name, prof)
		return nil
	})
	if err != nil {
		// No credential was committed (rolled back under the lock, or never
		// written); nobody holds the new access, so revoke it. The attempt
		// is over: its device code was used up.
		oldToken = ""
		inv.revokeQuietly(ctx, t, tr.AccessToken)
		inv.dropLogin(t, gen)
		var e *output.Error
		if errors.As(err, &e) {
			return nil, e
		}
		return nil, output.Errorf(output.ExitUnclassified, output.CodeInternal, "cannot save the login (the new access was revoked): %v", err)
	}

	inv.noteContext(t, true)
	data := loginData{
		Profile: t.name, Server: t.origin, CredentialStore: secrets.Kind(),
		TokenHint: server.TokenHint(tr.AccessToken), User: user,
		DelegatedAccess: newAccessView(&tr.DelegatedAccess, org),
	}
	// Renewal: revoke the previous access once the new one is stored.
	if oldToken != "" && oldToken != tr.AccessToken {
		revoked := auth.Revoke(ctx, inv.httpClient(t, ""), t.disc.RevocationEndpoint, t.disc.ClientID, oldToken) == nil
		data.PreviousAccessRevoked = &revoked
		if !revoked {
			inv.Env.stderrf("Could not revoke the previous access; revoke it at passport › Security › Sessions & devices.\n")
		}
	}

	orgLabel := tr.DelegatedAccess.OrganizationID
	if org != nil && org.Name != "" {
		orgLabel = org.Name
	}
	inv.Env.stderrf("Logged in to %s for %s (%d outlet(s), %d permission(s), lifetime %s).\n",
		t.origin, orgLabel, len(tr.DelegatedAccess.OutletIDs), len(tr.DelegatedAccess.Permissions), tr.DelegatedAccess.Lifetime)
	if tr.DelegatedAccess.ExpireAt == nil {
		inv.Env.stderrf(neverExpiresNote)
	}
	inv.Meta.CredentialExpiresAt = output.KnownTime(tr.DelegatedAccess.ExpireAt)
	return data, nil
}

func (inv *Invocation) revokeQuietly(ctx context.Context, t *target, token string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = auth.Revoke(rctx, inv.httpClient(t, ""), t.disc.RevocationEndpoint, t.disc.ClientID, token)
}

type bindingMismatchDetails struct {
	Profile    string `json:"profile"`
	ExpectedID string `json:"expected_id"`
	ActualID   string `json:"actual_id"`
}

func profileBindingMismatch(t *target, code, expected, actual string) error {
	return output.Errorf(output.ExitUsage, code, "login does not match the pinned identity of profile %q", t.name).
		WithDetails(bindingMismatchDetails{Profile: t.name, ExpectedID: expected, ActualID: actual}).
		WithHint("Use the original organization and principal, or explicitly retarget with `gatherdeal auth logout" + t.targetFlagsHint() + "` then log in again.")
}
