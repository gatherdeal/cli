package cli

import (
	"os"
	"strings"
	"testing"
)

func TestGroupHelp(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "Read CRM contacts."},
		{nil, "Install or print the GatherDeal agent skill."},
		{[]string{"skills", "--help"}, "install"},
		{[]string{"skills", "install", "--help"}, "--scope"},
		{[]string{"--help"}, "Manage WhatsApp agents and send WhatsApp messages."},
		{[]string{"whatsapp"}, "List, create, pair, update and delete WhatsApp agents."},
		{[]string{"crm"}, "Usage: gatherdeal crm <command>"},
		{[]string{"crm", "--help"}, "contacts"},
		{[]string{"crm", "contacts", "--help"}, "get"},
		{[]string{"crm", "contacts"}, "list"},
		{[]string{"auth", "--help"}, "logout"},
		{[]string{"help", "crm", "contacts", "get"}, "gatherdeal crm contacts get <id>"},
		{[]string{"crm", "contacts", "get", "--help"}, "--with-emails"},
		{[]string{"crm", "contact-tags", "list", "--help"}, "gatherdeal crm contact-tags list"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			res := newTestEnv(t, nil).run(tc.args...)
			if res.exit != 0 || !strings.Contains(res.stdout, tc.want) {
				t.Fatalf("exit %d: %s", res.exit, res.stdout)
			}
		})
	}
}

func TestUnknownGroupChoices(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"pos"}, "unknown app or command"},
		{[]string{"contacts", "list"}, "crm"}, // Resources live under their app only.
		{[]string{"crm", "orders"}, "unknown resource"},
		{[]string{"crm", "orders"}, "valid choices: contact-tags, contacts"},
		{[]string{"crm", "contact_tags", "list"}, `unknown resource "contact_tags"; valid choices: contact-tags, contacts`},
		{[]string{"whatsapp", "chat_messages", "list"}, `unknown resource "chat_messages"`},
		{[]string{"whatsapp", "broadcast_suppressions", "list"}, `unknown resource "broadcast_suppressions"`},
		{[]string{"whatsapp", "message_templates", "list"}, `unknown resource "message_templates"`},
		{[]string{"crm", "contacts", "delete"}, "valid choices: get, list"},
		{[]string{"auth", "unknown"}, "valid choices: login, logout, profiles, status"},
	} {
		res := newTestEnv(t, nil).run(tc.args...)
		if res.exit != 2 || res.errorCode() != "CLI_USAGE" || !strings.Contains(res.env["error"].(map[string]any)["message"].(string), tc.want) {
			t.Fatalf("%v: exit %d: %s", tc.args, res.exit, res.stdout)
		}
	}
}

func TestRegistryGroupsSeparateSharedResources(t *testing.T) {
	crm := &Command{App: "crm", Path: []string{"crm", "orders", "list"}}
	pos := &Command{App: "pos", Path: []string{"pos", "orders", "list"}}
	root := commandTree([]*Command{crm, pos})
	for _, cmd := range []*Command{crm, pos} {
		node, rest, err := root.resolve(append(append([]string{}, cmd.Path...), "--search", "ali"))
		if err != nil || node.command != cmd || strings.Join(rest, " ") != "--search ali" {
			t.Fatalf("resolve %s: %v", cmd.Name(), err)
		}
	}
	if root.children["pos"].summary() != "Commands: orders." {
		t.Fatal("app group not derived from registry")
	}
}

func TestCommandPathSegmentsHaveNoUnderscores(t *testing.T) {
	for _, c := range Commands() {
		for _, segment := range c.Path {
			if strings.Contains(segment, "_") {
				t.Errorf("%s: command path segment %q contains an underscore", c.Name(), segment)
			}
		}
	}
}

func TestEveryCommandHasSummary(t *testing.T) {
	for _, c := range Commands() {
		if c.Summary == "" || !strings.HasSuffix(c.Summary, ".") {
			t.Errorf("%s: summary %q must be a sentence", c.Name(), c.Summary)
		}
	}
}

func TestEveryGroupHasSummary(t *testing.T) {
	groups := map[string]bool{}
	var walk func(n *commandNode)
	walk = func(n *commandNode) {
		if n.command == nil && len(n.path) > 0 {
			name := strings.Join(n.path, " ")
			groups[name] = true
			if _, ok := groupSummaries[name]; !ok {
				t.Errorf("group %q has no entry in groupSummaries", name)
			}
		}
		for _, name := range n.choices() {
			walk(n.children[name])
		}
	}
	walk(commandTree(Commands()))
	for name := range groupSummaries {
		if !groups[name] {
			t.Errorf("groupSummaries has an entry for %q, which is not a group", name)
		}
	}
}

func TestHelpRespectsFlagOrderAndStringValues(t *testing.T) {
	te := newTestEnv(t, nil)
	if r := te.run("crm", "contacts", "list", "--unknown", "--help"); r.exit != 2 || r.errorCode() != "CLI_USAGE" {
		t.Fatal(r.stdout)
	}
	r := te.run("commands", "--search", "--help")
	if r.exit != 0 || r.data() == nil {
		t.Fatal(r.stdout)
	}
	for _, c := range Commands() {
		if c.Operation == "" {
			continue
		}
		r := te.run(append(append([]string{}, c.Path...), "--help")...)
		if r.exit != 0 || !strings.HasPrefix(r.stdout, "gatherdeal "+c.Name()) {
			t.Fatal(c.Name(), r.stdout)
		}
	}
}

func TestLeadingHelpNeverDispatches(t *testing.T) {
	f, te := loggedIn(t)
	t.Chdir(t.TempDir())
	if err := os.WriteFile("f", []byte(`{"type":"text","text":{"body":"hello"},"reply_to_id":"`+waAgentID+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	before := f.requests.Load()
	args := []string{"help", "whatsapp", "messages", "create", "--input", "f", "--send"}
	for _, tc := range []struct {
		flags []string
		exit  int
	}{
		{[]string{"--idempotency-key"}, 2},
		{[]string{"--idempotency-key", "replay-key"}, 0},
		{[]string{"--idempotency-key", "--help"}, 0},
		{[]string{"--help", "--unknown"}, 2},
		{[]string{"--input", "--", "extra", "--unknown"}, 2},
		{[]string{"--", "--unknown"}, 0},
	} {
		r := te.run(append(args, tc.flags...)...)
		if r.exit != tc.exit || f.requests.Load() != before {
			t.Fatalf("%v: exit %d, HTTP calls %d", tc.flags, r.exit, f.requests.Load()-before)
		}
		if tc.exit == 0 && !strings.HasPrefix(r.stdout, "gatherdeal whatsapp messages create") || tc.exit == 2 && r.errorCode() != "CLI_USAGE" {
			t.Fatal(r.stdout)
		}
	}
}
