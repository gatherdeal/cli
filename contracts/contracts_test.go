package contracts

import (
	"encoding/json"
	"testing"
)

func TestMultipleBundlesAndSafety(t *testing.T) {
	raw := []byte(`{"format_version":1,"bundles":[{"service":"crm","version":"v1","operations":[{"public_id":"crm.contacts.list","effect":{"mutation":"none","interaction":"closed","risks":[]},"permissions":["crm.contact.read"],"outlet_scope":"filtered","idempotency":"safe"}]},{"service":"identity","version":"v1","operations":[{"public_id":"identity.outlets.list"}]},{"service":"whatsapp","version":"v1","operations":[{"public_id":"whatsapp.messages.create","effect":{"mutation":"additive","interaction":"external","risks":["external_communication"]},"permissions":["whatsapp.message.send"],"outlet_scope":"filtered","idempotency":"keyed"}]}]}`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	read, ok := m.Operation("crm.contacts.list")
	if !ok || !read.ReadOnly() {
		t.Fatal(read)
	}
	write, ok := m.Operation("whatsapp.messages.create")
	if !ok || write.ReadOnly() || write.Idempotency != IdempotencyKeyed || write.OutletScope != "filtered" || write.Permissions[0] != "whatsapp.message.send" || write.Effect.Risks[0] != "external_communication" {
		t.Fatalf("%+v", write)
	}
	if _, ok := m.Operation("identity.outlets.list"); !ok {
		t.Fatal("missing identity")
	}
	m.Bundles = append(m.Bundles, m.Bundles[0])
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(data); err == nil {
		t.Fatal("accepted duplicate bundle")
	}
}
