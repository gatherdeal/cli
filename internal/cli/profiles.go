package cli

import (
	"context"
	"time"

	"github.com/gatherdeal/cli/internal/store"
)

type profileListEntry struct {
	Profile         string               `json:"profile"`
	Server          string               `json:"server"`
	Issuer          string               `json:"issuer"`
	Binding         *store.Binding       `json:"binding,omitempty"`
	CachedGrant     *store.AccessSummary `json:"cached_grant,omitempty"`
	CachedAt        *time.Time           `json:"cached_at,omitempty"`
	CacheAgeSeconds *int64               `json:"cache_age_seconds,omitempty"`
}

func runProfilesList(_ context.Context, inv *Invocation) (any, error) {
	root, err := store.ConfigRoot(inv.Env.Getenv)
	if err != nil {
		return nil, err
	}
	profiles, err := store.LoadProfiles(root)
	if err != nil {
		return nil, storeError(err, &target{root: root})
	}
	entries := make([]profileListEntry, 0, len(profiles.Names()))
	for _, name := range profiles.Names() {
		p := profiles.Get(name)
		entry := profileListEntry{Profile: name, Server: p.Server, Issuer: p.Issuer, Binding: p.Binding, CachedGrant: p.Access}
		if p.Access != nil && !p.Access.LoggedInAt.IsZero() {
			entry.CachedAt = &p.Access.LoggedInAt
			age := max(int64(0), int64(inv.Env.now().Sub(p.Access.LoggedInAt).Seconds()))
			entry.CacheAgeSeconds = &age
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
