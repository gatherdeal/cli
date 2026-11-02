package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const shapePage = `{"status":"success","contacts":{"data":[` +
	`{"id":"55555555-5555-5555-5555-555555555555","name":"Ali","remark":"line1\nline2\u001b[31m","kind":"personal"},` +
	`{"id":"66666666-6666-6666-6666-666666666666","name":"Bee","kind":"company"}],` +
	`"page_size":2,"has_next_page":false,"has_prev_page":false}}`

func TestJQ(t *testing.T) {
	fake, te := loggedIn(t)
	fake.contactsBody = shapePage
	// Strings print raw, one per line.
	res := te.run("crm", "contacts", "list", "--jq", ".[].name")
	if res.exit != 0 || res.stdout != "Ali\nBee\n" {
		t.Fatalf("names: exit %d %q", res.exit, res.stdout)
	}
	// Other values print as compact JSON; jq sees data, not the envelope.
	res = te.run("crm", "contacts", "list", "--jq=.[1]")
	if res.exit != 0 || res.stdout != `{"id":"66666666-6666-6666-6666-666666666666","kind":"company","name":"Bee"}`+"\n" {
		t.Fatalf("object: exit %d %q", res.exit, res.stdout)
	}
	res = te.run("crm", "contacts", "list", "--jq", "length, (.[0].id | length), type")
	if res.exit != 0 || res.stdout != "2\n36\narray\n" {
		t.Fatalf("numbers: exit %d %q", res.exit, res.stdout)
	}
	// Raw strings keep line feeds but escape terminal controls.
	res = te.run("crm", "contacts", "list", "--jq", ".[0].remark")
	if res.exit != 0 || res.stdout != "line1\nline2\\x1b[31m\n" {
		t.Fatalf("raw: exit %d %q", res.exit, res.stdout)
	}
	// No results print nothing.
	if res = te.run("crm", "contacts", "list", "--jq", "empty"); res.exit != 0 || res.stdout != "" {
		t.Fatalf("empty: exit %d %q", res.exit, res.stdout)
	}
	// The environment is not visible to the expression.
	if res = te.run("crm", "contacts", "list", "--jq", "$ENV | length"); res.exit != 0 || res.stdout != "0\n" {
		t.Fatalf("env: exit %d %q", res.exit, res.stdout)
	}
	// Commands without a server take --jq too.
	if res = te.run("version", "--jq", ".version | type"); res.exit != 0 || res.stdout != "string\n" {
		t.Fatalf("version: exit %d %q", res.exit, res.stdout)
	}
}

func TestJQRedacts(t *testing.T) {
	fake, te := loggedIn(t)
	fake.echoAuth = true
	res := te.run("crm", "contacts", "get", "55555555-5555-5555-5555-555555555555", "--jq", ".remark, .name, .")
	if res.exit != 0 || strings.Contains(res.stdout, fake.lastToken()) || strings.Count(res.stdout, "[REDACTED]") != 4 {
		t.Fatalf("exit %d %q", res.exit, res.stdout)
	}
}

func TestJQInvalidExpressionBeforeNetwork(t *testing.T) {
	fake, te := loggedIn(t)
	fake.contactsBody = shapePage
	for _, expr := range []string{".[", "", "  ", "nosuchfunction(1)", "$undefined"} {
		fake.contactsQuery = nil
		res := te.run("crm", "contacts", "list", "--jq", expr)
		if res.exit != 2 || res.errorCode() != "CLI_USAGE" || res.env["status"] != "error" {
			t.Errorf("%q: exit %d %s", expr, res.exit, res.stdout)
		}
		if fake.contactsQuery != nil {
			t.Errorf("%q: the request was sent", expr)
		}
	}
	// A write is refused before preparing or dispatching it.
	res := te.run("whatsapp", "messages", "create", "--input", `{}`, "--send", "--jq", ".[")
	if res.exit != 2 || res.errorCode() != "CLI_USAGE" || res.env["meta"].(map[string]any)["write"] != nil {
		t.Fatalf("write: exit %d %s", res.exit, res.stdout)
	}
}

func TestJQLeavesErrorsAlone(t *testing.T) {
	fake, te := loggedIn(t)
	fake.contactsStatus = 404
	fake.contactsBody = problemJSON(404, "CRM_CONTACT_NOT_FOUND", "Contact not found.", map[string]any{"contact_id": "x"})
	plain := te.run("crm", "contacts", "get", "55555555-5555-5555-5555-555555555555")
	res := te.run("crm", "contacts", "get", "55555555-5555-5555-5555-555555555555", "--jq", ".name", "--fields", "name")
	if res.exit != 5 || res.exit != plain.exit || res.stdout != plain.stdout || res.errorCode() != "CRM_CONTACT_NOT_FOUND" {
		t.Fatalf("exit %d %s\nwant %s", res.exit, res.stdout, plain.stdout)
	}
	// Usage errors are not filtered either.
	res = te.run("crm", "contacts", "get", "not-a-uuid", "--jq", ".")
	if res.exit != 2 || res.errorCode() != "CLI_USAGE" {
		t.Fatalf("usage: exit %d %s", res.exit, res.stdout)
	}
}

func TestJQRuntimeError(t *testing.T) {
	fake, te := loggedIn(t)
	fake.contactsBody = shapePage
	res := te.run("crm", "contacts", "list", "--jq", ".name")
	if res.exit != 2 || res.errorCode() != "CLI_JQ_FAILED" || len(res.env["data"].([]any)) != 2 || res.env["hint"] == nil {
		t.Fatalf("exit %d %s", res.exit, res.stdout)
	}
	res = te.run("crm", "contacts", "list", "--jq", `error("stop")`)
	if res.exit != 2 || res.errorCode() != "CLI_JQ_FAILED" {
		t.Fatalf("error(): exit %d %s", res.exit, res.stdout)
	}
	// halt ends the output successfully.
	if res = te.run("crm", "contacts", "list", "--jq", ".[0].name, halt, .[1].name"); res.exit != 0 || res.stdout != "Ali\n" {
		t.Fatalf("halt: exit %d %q", res.exit, res.stdout)
	}
}

func TestFields(t *testing.T) {
	fake, te := loggedIn(t)
	fake.contactsBody = shapePage
	// Each element of the data array; the envelope stays.
	res := te.run("crm", "contacts", "list", "--fields", "name, id,name,remark")
	if res.exit != 0 || res.env["meta"].(map[string]any)["page_info"] == nil {
		t.Fatalf("list: exit %d %s", res.exit, res.stdout)
	}
	got, _ := json.Marshal(res.env["data"])
	if string(got) != `[{"id":"55555555-5555-5555-5555-555555555555","name":"Ali","remark":"line1\nline2\u001b[31m"},{"id":"66666666-6666-6666-6666-666666666666","name":"Bee"}]` {
		t.Fatalf("list data %s", got)
	}
	// The data object of a get.
	fake.contactsBody = `{"status":"success","contact":{"id":"55555555-5555-5555-5555-555555555555","name":"Ali","kind":"personal"}}`
	res = te.run("crm", "contacts", "get", "55555555-5555-5555-5555-555555555555", "--fields", "kind")
	if d := res.data(); res.exit != 0 || len(d) != 1 || d["kind"] != "personal" {
		t.Fatalf("get: exit %d %s", res.exit, res.stdout)
	}
	// Fields first, then jq.
	res = te.run("crm", "contacts", "get", "55555555-5555-5555-5555-555555555555", "--fields", "kind", "--jq", "keys")
	if res.exit != 0 || res.stdout != `["kind"]`+"\n" {
		t.Fatalf("fields+jq: exit %d %q", res.exit, res.stdout)
	}
}

func TestFieldsUsage(t *testing.T) {
	fake, te := loggedIn(t)
	for _, fields := range []string{"nam", "id,,name", "", "id,bogus,other"} {
		fake.contactsQuery = nil
		res := te.run("crm", "contacts", "list", "--fields", fields)
		if res.exit != 2 || res.errorCode() != "CLI_USAGE" || fake.contactsQuery != nil {
			t.Errorf("%q: exit %d %s", fields, res.exit, res.stdout)
		}
	}
	res := te.run("crm", "contacts", "list", "--fields", "id,bogus")
	msg := res.env["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "unknown field bogus") || !strings.Contains(msg, "available: id, create_at,") {
		t.Fatalf("message %q", msg)
	}
	// Commands without records have no --fields.
	if res := te.run("auth", "status", "--fields", "id"); res.exit != 2 {
		t.Fatalf("auth status: exit %d", res.exit)
	}
}

// Every JSON command takes --jq; list and get commands take --fields with
// the JSON members of their record type.
func TestShapingFlagsInCatalog(t *testing.T) {
	_, te := loggedIn(t)
	res := te.run("commands")
	if res.exit != 0 {
		t.Fatal(res.stdout)
	}
	var data commandsData
	raw, _ := json.Marshal(res.env["data"])
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	withFields := map[string]bool{}
	for _, c := range data.Commands {
		names := map[string]bool{}
		for _, f := range c.Flags {
			names[f.Name] = true
		}
		if names["jq"] != (c.Output == "json") {
			t.Errorf("%s: --jq present=%v, output %s", c.Name, names["jq"], c.Output)
		}
		if names["fields"] != (len(c.RecordFields) > 0) {
			t.Errorf("%s: --fields present=%v, record_fields %v", c.Name, names["fields"], c.RecordFields)
		}
		if names["fields"] {
			withFields[c.Name] = true
		}
	}
	var got []string
	for name := range withFields {
		got = append(got, name)
	}
	slices.Sort(got)
	want := []string{"crm contacts get", "crm contacts list", "outlets get", "outlets list", "whatsapp agents get", "whatsapp agents list", "whatsapp messages get", "whatsapp messages list"}
	if !slices.Equal(got, want) {
		t.Fatalf("--fields on %v, want %v", got, want)
	}
}

func TestRecordFields(t *testing.T) {
	type inner struct {
		B string `json:"b"`
	}
	type rec struct {
		A string `json:"a,omitempty"`
		*inner
		Skip   string `json:"-"`
		Plain  int
		hidden int
	}
	_ = rec{}.hidden
	if got := recordFields(rec{}); !slices.Equal(got, []string{"a", "b", "Plain"}) {
		t.Fatal(got)
	}
}
