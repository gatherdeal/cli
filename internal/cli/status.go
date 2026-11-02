package cli

import (
	"context"
	"errors"

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
	// The whole logout runs under the lock so a concurrent login cannot
	// interleave with it.
	var revokeErr error
	deleted := true
	lockErr := inv.withLock(t, func(tx *stateTx, p *store.Profiles, prof *store.Profile) error {
		if prof == nil {
			data.Note = joinNote(data.Note, "no stored credential for this profile")
			return nil
		}
		if prof.Access != nil {
			data.DelegatedAccessID = prof.Access.ID
		}
		var token string
		var credStore store.SecretStore
		var credAccount string
		if ref := prof.Credential; ref != nil {
			data.LoggedIn = true
			credStore, credAccount = inv.secretStore(t, ref.Store), tx.slot.CredentialAccount(ref.Generation)
			secret, err := credStore.Get(credAccount)
			switch {
			case err == nil:
				token = secret
				inv.Env.addSecret(secret)
			case errors.Is(err, store.ErrNotFound):
				data.Note = joinNote(data.Note, "the stored credential was already gone")
			default:
				return storeError(err, t)
			}
		}
		if token != "" {
			revokeErr = auth.Revoke(ctx, inv.httpClient(t, ""), t.disc.RevocationEndpoint, t.disc.ClientID, token)
			data.Revoked = revokeErr == nil
		}
		// Delete locally even when the revoke failed; the secrets go once
		// the profile removal is saved, so metadata never points at a
		// deleted credential. Removing the profile also removes its login
		// attempt, so a login still in flight can no longer commit.
		p.Delete(t.name)
		if credStore != nil {
			tx.AfterCommit(func() { deleted = credStore.Delete(credAccount) == nil })
		}
		inv.discardAttempt(t, tx, prof, prof.Login)
		return nil
	})
	if lockErr != nil {
		if revokeErr == nil {
			return nil, lockErr
		}
		deleted = false
	}
	data.CredentialDeleted = deleted && data.LoggedIn

	if revokeErr != nil {
		e := output.AsError(revokeErr)
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

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
