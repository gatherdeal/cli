package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/gatherdeal/cli/contracts"
	"github.com/gatherdeal/cli/internal/auth"
	identityv1 "github.com/gatherdeal/cli/internal/generated/identity/v1"
	"github.com/gatherdeal/cli/internal/identity"
	"github.com/gatherdeal/cli/internal/output"
)

func outletCommands() []*Command {
	flags := append([]FlagSpec{}, targetFlags...)
	flags = append(flags, pageFlags(identity.ListOperation, "outlets")...)
	flags = append(flags,
		FlagSpec{Name: "id", Kind: kindStrings, Usage: "Filter by outlet UUID (repeatable)."},
		FlagSpec{Name: "code", Kind: kindStrings, Usage: "Filter by outlet code (repeatable)."},
		FlagSpec{Name: "name", Kind: kindString, Usage: "Filter by outlet name (partial match)."},
		flagSearch,
		FlagSpec{Name: "is-master", Kind: kindBool, Usage: "Filter by master outlet status; use --is-master=false for other outlets."},
		sortFlag(identity.ListOperation),
	)
	for _, param := range contracts.MustOperation(identity.ListOperation).DelegatedExpansions {
		flags = append(flags, FlagSpec{Name: expansionFlagName(param), Kind: kindBool, Usage: "Include the outlet's " + strings.TrimPrefix(param, "with_") + "."})
	}
	getFlags := append([]FlagSpec{}, targetFlags...)
	for _, param := range contracts.MustOperation(identity.GetOperation).DelegatedExpansions {
		getFlags = append(getFlags, FlagSpec{Name: expansionFlagName(param), Kind: kindBool, Usage: "Include the outlet's " + strings.TrimPrefix(param, "with_") + "."})
	}
	return []*Command{
		{Operation: identity.ListOperation, Presentation: &CommandPresentation{Path: []string{"outlets", "list"}}, Summary: "List outlets this access may read.", Flags: flags, Record: identityv1.Outlet{}, Output: "json", Network: true, Run: runOutletsList},
		// A code is resolved with the list's exact code selector first.
		{Operation: identity.GetOperation, Presentation: &CommandPresentation{Path: []string{"outlets", "get"}}, Summary: "Show one outlet this access may read, by id or code.", Args: []ArgSpec{{Name: "id-or-code", Usage: "Outlet UUID, or an outlet code to look up through the outlets list."}}, Flags: getFlags, Record: identityv1.Outlet{}, Output: "json", Network: true, Run: runOutletsGet},
	}
}
func (inv *Invocation) identityClient() (*identity.Client, error) {
	t, err := inv.resolveTarget(false)
	if err != nil {
		return nil, err
	}
	cred, err := inv.loadCredential(t)
	if err != nil {
		return nil, err
	}
	// Lookup runs within the authorized set; a code is never an outlet-id selector.
	inv.Meta.Context.Request = output.ContextRequest{OutletIDs: []string{}}
	base, ok := t.disc.ServiceURL("identity")
	if !ok {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeServiceMissing, "%s does not advertise the identity API", t.origin)
	}
	return identity.New(inv.httpClient(t, cred.token), base)
}
func runOutletsList(ctx context.Context, inv *Invocation) (any, error) {
	if err := validatePaging(inv); err != nil {
		return nil, err
	}
	for _, id := range inv.list("id") {
		if !uuidRE.MatchString(id) {
			return nil, usageError("--id must be an outlet UUID")
		}
	}
	for _, code := range inv.list("code") {
		if strings.TrimSpace(code) == "" {
			return nil, usageError("--code must not be empty")
		}
	}
	in := identityv1.FetchOutletsInput{IDs: inv.list("id"), Codes: inv.list("code"), Name: inv.str("name"), SearchTerm: inv.str("search"), Page: inv.integer("page"), PageSize: inv.integer("page-size"), After: inv.str("after"), Total: identityv1.TotalMode(inv.str("total")), SortBy: inv.str("sort"), WithOwner: inv.boolean("with-owner"), WithOrganization: inv.boolean("with-organization")}
	if inv.isSet("is-master") {
		value := inv.boolean("is-master")
		in.IsMaster = &value
	}
	c, err := inv.identityClient()
	if err != nil {
		return nil, err
	}
	walk, limit := inv.walk()
	items, page, correlation, err := c.FetchOutlets(ctx, in, walk, limit)
	inv.Meta.CorrelationID = correlation
	if err != nil {
		return nil, auth.Explain(err)
	}
	inv.Meta.PageInfo = &page
	return items, nil
}

// resolveOutlet reads an outlet by UUID with outlets.get, or finds it by
// exact code with outlets.list. expand selects the get's expansions.
func (inv *Invocation) resolveOutlet(ctx context.Context, idOrCode string, expand ...string) (json.RawMessage, error) {
	if strings.TrimSpace(idOrCode) == "" {
		return nil, usageError("outlet id or code is required")
	}
	c, err := inv.identityClient()
	if err != nil {
		return nil, err
	}
	withOwner, withOrganization := slices.Contains(expand, "with_owner"), slices.Contains(expand, "with_organization")
	var outlet json.RawMessage
	var correlation string
	if uuidRE.MatchString(idOrCode) {
		outlet, correlation, err = c.GetOutlet(ctx, identityv1.FetchOutletByIDInput{IDOrCode: idOrCode, WithOwner: withOwner, WithOrganization: withOrganization})
	} else {
		outlet, correlation, err = c.FindOutlet(ctx, identityv1.FetchOutletsInput{Codes: []string{idOrCode}, WithOwner: withOwner, WithOrganization: withOrganization})
	}
	inv.Meta.CorrelationID = correlation
	return outlet, auth.Explain(err)
}
func runOutletsGet(ctx context.Context, inv *Invocation) (any, error) {
	return inv.resolveOutlet(ctx, inv.Args[0], inv.expansions()...)
}
