package cli

import (
	"context"
	"errors"
	"time"

	"github.com/gatherdeal/cli/internal/auth"
	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/server"
	"github.com/gatherdeal/cli/internal/store"
)

// statusData is the payload of `auth status`.
type statusData struct {
	Profile          string           `json:"profile"`
	Server           string           `json:"server"`
	CredentialSource string           `json:"credential_source"`
	TokenHint        string           `json:"token_hint"`
	User             *auth.StatusUser `json:"user,omitempty"`
	DelegatedAccess  accessView       `json:"delegated_access"`
}

func runStatus(ctx context.Context, inv *Invocation) (any, error) {
	t, err := inv.resolveTarget(false)
	if err != nil {
		return nil, err
	}
	cred, err := inv.loadCredential(t)
	if err != nil {
		return nil, err
	}
	st, err := auth.Status(ctx, inv.httpClient(t, cred.token), t.disc.StatusEndpoint, t.disc.ClientID)
	if err != nil {
		return nil, err
	}
	inv.Meta.CorrelationID = st.CorrelationID
	inv.Meta.CredentialExpiresAt = output.KnownTime(st.DelegatedAccess.ExpireAt)
	return statusData{
		Profile: t.name, Server: t.origin, CredentialSource: cred.source,
		TokenHint: server.TokenHint(cred.token), User: st.User,
		DelegatedAccess: newAccessView(st.DelegatedAccess, st.Organization),
	}, nil
}

// logoutData is the payload of `auth logout`.
type logoutData struct {
	Profile           string `json:"profile"`
	Server            string `json:"server"`
	LoggedIn          bool   `json:"logged_in"`
	Revoked           bool   `json:"revoked"`
	CredentialDeleted bool   `json:"credential_deleted"`
	DelegatedAccessID string `json:"delegated_access_id,omitempty"`
	Note              string `json:"note,omitempty"`
}

// revokeFailureDetails accompanies a logout whose revoke failed.
type revokeFailureDetails struct {
	CredentialDeleted bool   `json:"credential_deleted"`
	DelegatedAccessID string `json:"delegated_access_id,omitempty"`
	Cause             any    `json:"cause,omitempty"`
}

func runLogout(ctx context.Context, inv *Invocation) (any, error) {
	t, err := inv.resolveTarget(false)
	var oe *output.Error
	if errors.As(err, &oe) && oe.Code == output.CodeNotLoggedIn {
		return logoutData{Profile: inv.Meta.Profile, Server: inv.Meta.Server, Note: "no stored credential for this profile"}, nil
	}
	if err != nil {
		return nil, err
	}
	inv.noteContext(t, true)
	data := logoutData{Profile: t.name, Server: t.origin}
	if inv.Env.Getenv(EnvToken) != "" {
		data.Note = EnvToken + " is set; logout does not revoke or change it"
	}
	res, lockErr := inv.endAccess(ctx, t, endAccessOptions{})
	data.LoggedIn, data.DelegatedAccessID, data.Revoked = res.loggedIn, res.accessID, res.revoked
	data.Note = joinNote(data.Note, res.note)
	deleted := res.deleteErr == nil
	if lockErr != nil {
		if res.revokeErr == nil {
			return nil, lockErr
		}
		deleted = false
	}
	data.CredentialDeleted = deleted && data.LoggedIn

	if res.revokeErr != nil {
		e := output.AsError(res.revokeErr)
		cause := map[string]any{"code": e.Code, "message": e.Message}
		out := &output.Error{
			Code:       output.CodeRevokeFailed,
			Message:    "the access could not be revoked on the server; the local credential was deleted",
			HTTPStatus: e.HTTPStatus, CorrelationID: e.CorrelationID, Retryable: e.Retryable,
			Exit: e.Exit,
			Details: revokeFailureDetails{
				CredentialDeleted: data.CredentialDeleted, DelegatedAccessID: data.DelegatedAccessID, Cause: cause,
			},
			Hint: "The access may still work until it expires. Revoke it at passport › Security › Sessions & devices.",
		}
		if out.Exit == output.ExitOK || out.Exit == output.ExitAuth {
			out.Exit = output.ExitUnclassified
		}
		return nil, out
	}
	if !deleted {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeInternal, "the access was revoked but the local credential could not be deleted").
			WithDetails(map[string]any{"revoked": data.Revoked})
	}
	if data.LoggedIn {
		inv.Env.stderrf("Logged out of %s (profile %q).\n", t.origin, t.name)
	}
	return data, nil
}

// endAccessOptions selects how endAccess treats the server.
type endAccessOptions struct {
	// localOnly never contacts the server: the access stays valid there.
	localOnly bool
	// failClosed keeps the profile and its credential when the revoke
	// fails, unless the server reports the access already inactive.
	failClosed bool
}

// endAccessResult is what endAccess did.
type endAccessResult struct {
	loggedIn bool
	accessID string
	// revoked: the server revoked the access, or (failClosed) reports it
	// already inactive (alreadyInactive).
	revoked         bool
	alreadyInactive bool
	note            string
	// revokeErr is the failed revoke; without failClosed the profile was
	// deleted anyway.
	revokeErr error
	// deleteErr is a failure to delete the credential once the profile's
	// removal was saved.
	deleteErr error
}

// errKeepProfile aborts a locked change after a failed revoke (failClosed).
var errKeepProfile = errors.New("the access could not be revoked")

// endAccess revokes the stored access of t's profile on its server and
// removes the profile, its credential and any unfinished login attempt,
// all under the lock so a concurrent login cannot interleave. Without
// failClosed the local state is deleted even when the revoke fails. With
// failClosed a failed revoke returns errKeepProfile and changes nothing.
func (inv *Invocation) endAccess(ctx context.Context, t *target, opts endAccessOptions) (endAccessResult, error) {
	var r endAccessResult
	err := inv.withLock(t, func(tx *stateTx, p *store.Profiles, prof *store.Profile) error {
		if prof == nil {
			r.note = "no stored credential for this profile"
			return nil
		}
		if prof.Access != nil {
			r.accessID = prof.Access.ID
		}
		var token string
		var credStore store.SecretStore
		var credAccount string
		if ref := prof.Credential; ref != nil {
			r.loggedIn = true
			credStore, credAccount = inv.secretStore(t, ref.Store), tx.slot.CredentialAccount(ref.Generation)
			if !opts.localOnly {
				secret, err := credStore.Get(credAccount)
				switch {
				case err == nil:
					token = secret
					inv.Env.addSecret(secret)
				case errors.Is(err, store.ErrNotFound):
					r.note = "the stored credential was already gone"
				default:
					return storeError(err, t)
				}
			}
		}
		if token != "" {
			r.revokeErr = auth.Revoke(ctx, inv.httpClient(t, ""), t.disc.RevocationEndpoint, t.disc.ClientID, token)
			if r.revokeErr != nil && opts.failClosed && inv.accessInactive(ctx, t, token) {
				r.revokeErr, r.alreadyInactive = nil, true
			}
			r.revoked = r.revokeErr == nil
			if r.revokeErr != nil && opts.failClosed {
				return errKeepProfile
			}
		}
		// The secrets go once the profile removal is saved, so metadata
		// never points at a deleted credential. Removing the profile also
		// removes its login attempt, so a login still in flight can no
		// longer commit.
		p.Delete(t.name)
		if credStore != nil {
			tx.AfterCommit(func() { r.deleteErr = credStore.Delete(credAccount) })
		}
		inv.discardAttempt(t, tx, prof, prof.Login, !opts.localOnly)
		return nil
	})
	return r, err
}

// accessInactive reports whether the server says token's access is no
// longer active (revoked, expired or unused for too long).
func (inv *Invocation) accessInactive(ctx context.Context, t *target, token string) bool {
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := auth.Status(sctx, inv.httpClient(t, token), t.disc.StatusEndpoint, t.disc.ClientID)
	var rej *auth.CredentialRejectedError
	return errors.As(err, &rej) && rej.Reason != auth.RejectedUnknown
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
