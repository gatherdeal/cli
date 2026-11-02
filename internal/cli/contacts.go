package cli

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/gatherdeal/cli/contracts"
	"github.com/gatherdeal/cli/internal/auth"
	"github.com/gatherdeal/cli/internal/crm"
	crmv1 "github.com/gatherdeal/cli/internal/generated/crm/v1"
	"github.com/gatherdeal/cli/internal/output"
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ContactsDeps is what a Contacts implementation receives: an HTTP client
// bound to the profile's server origin (credential attached, redirects
// refused) and the CRM base URL from discovery.
type ContactsDeps struct {
	HTTP    *http.Client
	BaseURL string
}

// NewContacts builds the CRM implementation. Tests may replace it.
var NewContacts = func(d ContactsDeps) (crm.Contacts, error) {
	return crm.New(d.HTTP, d.BaseURL)
}

// expansionFlags are the --with-* flags of a command: one per
// delegated_expansions entry of its operation in contracts/manifest.json.
func expansionFlags(publicID string) []FlagSpec {
	var flags []FlagSpec
	for _, param := range contracts.MustOperation(publicID).DelegatedExpansions {
		what := strings.ReplaceAll(strings.TrimPrefix(param, "with_"), "_", " ")
		flags = append(flags, FlagSpec{Name: expansionFlagName(param), Kind: kindBool, Usage: "Include the contact's " + what + "."})
	}
	return flags
}

// expansionFlagName is the flag of an expansion parameter: with_phones is
// --with-phones.
func expansionFlagName(param string) string { return strings.ReplaceAll(param, "_", "-") }

var flagOutlet = FlagSpec{Name: "outlet", Kind: kindString, Usage: "Outlet id (UUID) to read from; it must be one of the outlets this access may read."}

func (inv *Invocation) expansions() crm.Expansions {
	var set crm.Expansions
	for _, param := range contracts.MustOperation(inv.Command.Operation).DelegatedExpansions {
		if inv.boolean(expansionFlagName(param)) {
			set = append(set, param)
		}
	}
	return set
}

// contactsClient resolves the profile and credential and returns the CRM
// implementation bound to the profile's server.
func (inv *Invocation) contactsClient() (crm.Contacts, error) {
	t, err := inv.resolveTarget(false)
	if err != nil {
		return nil, err
	}
	cred, err := inv.loadCredential(t)
	if err != nil {
		return nil, err
	}
	base, ok := t.disc.ServiceURL("crm")
	if !ok {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeServiceMissing, "%s does not advertise the CRM API", t.origin)
	}
	return NewContacts(ContactsDeps{HTTP: inv.httpClient(t, cred.token), BaseURL: base})
}

func validateOutlet(inv *Invocation) error {
	if o := inv.str("outlet"); o != "" && !uuidRE.MatchString(o) {
		return usageError("--outlet must be an outlet id (UUID)")
	}
	return nil
}

func runContactsList(ctx context.Context, inv *Invocation) (any, error) {
	if err := validatePaging(inv); err != nil {
		return nil, err
	}
	if err := validateOutlet(inv); err != nil {
		return nil, err
	}
	for _, id := range inv.list("tag-id") {
		if !uuidRE.MatchString(id) {
			return nil, usageError("--tag-id %q is not a tag id (UUID)", id)
		}
	}
	client, err := inv.contactsClient()
	if err != nil {
		return nil, err
	}
	params := crm.ListContactsParams{
		Page: inv.integer("page"), PageSize: inv.integer("page-size"),
		After: inv.str("after"), Total: crmv1.TotalMode(inv.str("total")),
		Search: inv.str("search"), SortBy: inv.str("sort"), SortOrder: inv.str("order"),
		OutletID: inv.str("outlet"), Expand: inv.expansions(),
		TagIDs: inv.list("tag-id"), TagNames: inv.list("tag"),
	}
	if walk, limit := inv.walk(); walk {
		res, err := client.ListAllContacts(ctx, params, limit)
		if err != nil {
			return nil, auth.Explain(err)
		}
		inv.Meta.CorrelationID = res.CorrelationID
		inv.Meta.PageInfo = &output.PageInfo{HasNextPage: res.Truncated, NextCursor: res.NextCursor, HasPrevPage: params.After != ""}
		return res.Contacts, nil
	}
	res, err := client.ListContacts(ctx, params)
	if err != nil {
		return nil, auth.Explain(err)
	}
	inv.Meta.CorrelationID = res.CorrelationID
	inv.Meta.PageInfo = &res.PageInfo
	return res.Contacts, nil
}

func runContactsGet(ctx context.Context, inv *Invocation) (any, error) {
	id := inv.Args[0]
	if !uuidRE.MatchString(id) {
		return nil, usageError("contact id %q is not a UUID", id)
	}
	if err := validateOutlet(inv); err != nil {
		return nil, err
	}
	client, err := inv.contactsClient()
	if err != nil {
		return nil, err
	}
	res, err := client.GetContact(ctx, crm.GetContactParams{ID: id, OutletID: inv.str("outlet"), Expand: inv.expansions()})
	if err != nil {
		var e *output.Error
		if errors.As(err, &e) && e.Exit == output.ExitNotFound {
			e.Hint = "The contact does not exist or is outside the outlets this access may read."
		}
		return nil, auth.Explain(err)
	}
	inv.Meta.CorrelationID = res.CorrelationID
	return res.Contact, nil
}
