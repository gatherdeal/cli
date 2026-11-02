package cli

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func commandNames(t *testing.T, res result) []string {
	t.Helper()
	if res.exit != 0 {
		t.Fatal(res.stdout)
	}
	names := []string{}
	for _, c := range res.data()["commands"].([]any) {
		names = append(names, c.(map[string]any)["name"].(string))
	}
	return names
}

func TestCommandsPrefix(t *testing.T) {
	full := newTestEnv(t, nil).run("commands")
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"crm"}, []string{"crm contacts list", "crm contacts get"}},
		{[]string{"crm", "contacts"}, []string{"crm contacts list", "crm contacts get"}},
		{[]string{"whatsapp", "messages"}, []string{"whatsapp messages list", "whatsapp messages get", "whatsapp messages create"}},
		{[]string{"whatsapp", "agents", "create"}, []string{"whatsapp agents create"}},
		{[]string{"auth"}, []string{"auth profiles list", "auth login", "auth status", "auth logout"}},
		{[]string{"version"}, []string{"version"}},
	} {
		res := newTestEnv(t, nil).run(append([]string{"commands"}, tc.args...)...)
		if got := commandNames(t, res); !slices.Equal(got, tc.want) {
			t.Errorf("%v: got %v, want %v", tc.args, got, tc.want)
		}
		// The global sections stay complete.
		for _, key := range []string{"envelope_schema_version", "exit_codes", "environment"} {
			if !reflect.DeepEqual(res.data()[key], full.data()[key]) {
				t.Errorf("%v: %s differs from the full catalog", tc.args, key)
			}
		}
	}
	// Segments match whole words only.
	for _, args := range [][]string{{"cr"}, {"crm", "contact"}} {
		if res := newTestEnv(t, nil).run(append([]string{"commands"}, args...)...); res.exit != 2 {
			t.Errorf("%v: exit %d", args, res.exit)
		}
	}
}

func TestCommandsUnknownPrefix(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"pos"}, `unknown command path "pos"; valid next segments: agent-context, auth, commands, crm, outlets, skills, update, version, whatsapp`},
		{[]string{"crm", "orders"}, `unknown command path "crm orders"; valid next segments: contacts`},
		{[]string{"whatsapp", "agents", "send"}, `unknown command path "whatsapp agents send"; valid next segments: create, delete, get, list, pairing, update`},
		{[]string{"version", "extra"}, `"version" is a command and has no subcommands`},
	} {
		res := newTestEnv(t, nil).run(append([]string{"commands"}, tc.args...)...)
		if res.exit != 2 || res.errorCode() != "CLI_USAGE" || res.env["error"].(map[string]any)["message"] != tc.want {
			t.Errorf("%v: exit %d: %s", tc.args, res.exit, res.stdout)
		}
	}
}

func TestCommandsSearch(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []string
	}{
		// Command path and summary, case-insensitive.
		{[]string{"--search", "PAIRING"}, []string{"whatsapp agents pairing", "whatsapp agents create"}},
		{[]string{"--search", "auth log"}, []string{"auth login", "auth logout"}},
		// Command summary.
		{[]string{"--search", "unlink its device"}, []string{"whatsapp agents delete"}},
		// Resource summary: "List, show and send WhatsApp messages."
		{[]string{"--search", "show and send"}, []string{"whatsapp messages list", "whatsapp messages get", "whatsapp messages create"}},
		// Combined with a prefix.
		{[]string{"whatsapp", "--search", "recipient"}, []string{"whatsapp messages create"}},
		{[]string{"crm", "--search", "outlet"}, []string{"crm contacts list"}},
		// No match is an empty success.
		{[]string{"--search", "no such command"}, []string{}},
		{[]string{"crm", "--search", "whatsapp"}, []string{}},
	} {
		got := commandNames(t, newTestEnv(t, nil).run(append([]string{"commands"}, tc.args...)...))
		if !slices.Equal(got, tc.want) {
			t.Errorf("%v: got %v, want %v", tc.args, got, tc.want)
		}
	}
	res := newTestEnv(t, nil).run("commands", "--search", "no such command")
	if !strings.Contains(res.stdout, `"commands":[]`) {
		t.Fatalf("empty result is not an empty array: %s", res.stdout)
	}
}
