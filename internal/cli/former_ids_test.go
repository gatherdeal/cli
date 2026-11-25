package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gatherdeal/cli/contracts"
)

func formerIDsFixture(t *testing.T) *contracts.Manifest {
	t.Helper()
	data, err := os.ReadFile("testdata/former_ids.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := contracts.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFormerCommand(t *testing.T) {
	const canonical = "crm contacts get"
	const former = "crm contacts fetch-by-id"
	const id = "55555555-5555-5555-5555-555555555555"
	const warning = "warning: `gatherdeal crm contacts fetch-by-id` is deprecated since 2026-11-25 and will be removed in a later release; use `gatherdeal crm contacts get`.\n"
	deprecation := map[string]any{"since": "2026-11-25", "successor": canonical}

	cmds, err := registerFormerCommands(Commands(), formerIDsFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	fake, te := loggedIn(t)
	te.commands = cmds
	var calls atomic.Int32
	fake.crm = func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/crm/v1/contacts/"+id || r.URL.Query().Get("with_emails") != "true" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		writeJSON(w, http.StatusOK, `{"status":"success","contact":{"id":"`+id+`","name":"Ali"}}`)
	}

	group := te.run("crm", "contacts", "--help")
	for _, verb := range []string{"get ", "fetch-by-id "} {
		if group.exit != 0 || !strings.Contains(group.stdout, "\n  "+verb) {
			t.Fatalf("group help missing %q: %s", verb, group.stdout)
		}
	}
	catalog := te.run("commands", "crm", "contacts")
	names := commandNames(t, catalog)
	for _, name := range []string{canonical, former} {
		if !slices.Contains(names, name) {
			t.Fatalf("catalog missing %q: %v", name, names)
		}
	}
	entries := catalog.data()["commands"].([]any)
	current := entries[slices.Index(names, canonical)].(map[string]any)
	old := entries[slices.Index(names, former)].(map[string]any)
	for _, field := range []string{"operation", "args", "flags", "record_fields"} {
		if !reflect.DeepEqual(old[field], current[field]) {
			t.Errorf("catalog %s differs between commands", field)
		}
	}
	if !reflect.DeepEqual(old["deprecated"], deprecation) || current["deprecated"] != nil {
		t.Fatalf("unexpected catalog deprecation: %v / %v", old["deprecated"], current["deprecated"])
	}

	help := te.run("crm", "contacts", "fetch-by-id", "--help")
	if help.exit != 0 || !strings.Contains(help.stdout, "Deprecated since 2026-11-25; it will be removed in a later release. Use `gatherdeal crm contacts get`.") {
		t.Fatal(help.stdout)
	}
	for _, cmd := range []string{canonical, former} {
		res := te.run(append(strings.Fields(cmd), id, "--with-emails")...)
		if res.exit != 0 || res.data()["id"] != id || res.data()["name"] != "Ali" {
			t.Fatalf("%s: %s", cmd, res.stdout)
		}
		got, deprecated := res.env["meta"].(map[string]any)["deprecation"]
		if cmd == canonical && (deprecated || res.stderr != "") {
			t.Fatalf("canonical command: %s %s", res.stderr, res.stdout)
		}
		if cmd == former {
			if !reflect.DeepEqual(got, deprecation) || res.stderr != warning {
				t.Fatalf("former command: %s %s", res.stderr, res.stdout)
			}
			sample, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%smeta.deprecation = %s", res.stderr, sample)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("got %d route calls, want 2", got)
	}
}

func TestFormerCommandPathConflicts(t *testing.T) {
	for _, tc := range []struct {
		name, path, other string
		twoAliases        bool
	}{
		{name: "duplicate alias", path: "crm contacts fetch-by-id", other: "crm contacts fetch-by-id", twoAliases: true},
		{name: "live command", path: "crm contacts list", other: "crm contacts list"},
		{name: "alternate live path", path: "identity outlets list", other: "identity outlets list"},
		{name: "shadow group", path: "crm contacts", other: "crm contacts list"},
		{name: "under live command", path: "crm contacts get legacy", other: "crm contacts get"},
		{name: "shadow alias group", path: "crm contacts fetch-by-id legacy", other: "crm contacts fetch-by-id", twoAliases: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := formerIDsFixture(t)
			op := &m.Bundles[0].Operations[0]
			if len(op.FormerIDs) != 1 || op.FormerIDs[0].PublicID != "crm.contacts.fetch_by_id" {
				t.Fatalf("unexpected former IDs: %+v", op.FormerIDs)
			}
			former := op.FormerIDs[0]
			former.Command = tc.path
			if tc.twoAliases {
				former.PublicID = "crm.contacts.legacy_get"
				op.FormerIDs = append(op.FormerIDs, former)
			} else {
				op.FormerIDs[0] = former
			}
			cmds, err := registerFormerCommands(Commands(), m)
			if cmds != nil || err == nil || !strings.Contains(err.Error(), `former command "`+tc.path+`" (`+former.PublicID+`) conflicts with command "`+tc.other+`"`) {
				t.Fatalf("registered %d commands, error: %v", len(cmds), err)
			}
		})
	}
}
