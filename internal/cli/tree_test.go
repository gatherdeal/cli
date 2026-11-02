package cli

import (
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
		{[]string{"crm", "orders"}, "valid choices: contacts"},
		{[]string{"crm", "contacts", "delete"}, "valid choices: get, list"},
		{[]string{"auth", "unknown"}, "valid choices: login, logout, profiles, status"},
	} {
		res := newTestEnv(t, nil).run(tc.args...)
		if res.exit != 2 || res.errorCode() != "CLI_USAGE" || !strings.Contains(res.stdout, tc.want) {
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
