package contracts

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestMultipleBundlesAndSafety(t *testing.T) {
	raw := []byte(`{"format_version":2,"bundles":[{"service":"crm","version":"v1","operations":[{"public_id":"crm.contacts.list","effect":{"mutation":"none","interaction":"closed","risks":[]},"permissions":{"allOf":["crm.contact.read"]},"outlet_scope":"filtered","idempotency":"safe"}]},{"service":"identity","version":"v1","operations":[{"public_id":"identity.outlets.list","permissions":{"allOf":[]}}]},{"service":"whatsapp","version":"v1","operations":[{"public_id":"whatsapp.messages.create","effect":{"mutation":"additive","interaction":"external","risks":["external_communication"]},"permissions":{"anyOf":["whatsapp.message.send","whatsapp.message.create","whatsapp.message.send"]},"outlet_scope":"filtered","idempotency":"keyed"}]}]}`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	read, ok := m.Operation("crm.contacts.list")
	if !ok || !read.ReadOnly() || read.Permissions.IsAnyOf() || !slices.Equal(read.Permissions.Keys(), []string{"crm.contact.read"}) {
		t.Fatalf("%+v", read)
	}
	write, ok := m.Operation("whatsapp.messages.create")
	if !ok || write.ReadOnly() || write.Idempotency != IdempotencyKeyed || write.OutletScope != "filtered" ||
		!write.Permissions.IsAnyOf() || !slices.Equal(write.Permissions.Keys(), []string{"whatsapp.message.create", "whatsapp.message.send"}) ||
		write.Effect.Risks[0] != "external_communication" {
		t.Fatalf("%+v", write)
	}
	if outlets, ok := m.Operation("identity.outlets.list"); !ok || outlets.Permissions.Keys() != nil {
		t.Fatal("missing identity")
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := again.Operation("whatsapp.messages.create"); !got.Permissions.IsAnyOf() || !slices.Equal(got.Permissions.Keys(), write.Permissions.Keys()) {
		t.Fatalf("round trip lost the requirement: %+v", got)
	}
	m.Bundles = append(m.Bundles, m.Bundles[0])
	if data, err = json.Marshal(m); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(data); err == nil {
		t.Fatal("accepted duplicate bundle")
	}
}

func TestLegacyFormatReadsKeyArraysAsAllOf(t *testing.T) {
	m, err := Parse([]byte(`{"format_version":1,"bundles":[{"service":"crm","version":"v1","digest":"sha256:x","operations":[{"public_id":"crm.contacts.list","permissions":["crm.contact.read","crm.contact.export"],"outlet_scope":"filtered"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	op, ok := m.Operation("crm.contacts.list")
	if !ok || op.Permissions.IsAnyOf() || !slices.Equal(op.Permissions.Keys(), []string{"crm.contact.export", "crm.contact.read"}) ||
		op.OutletScope != "filtered" || m.Bundles[0].Digest != "sha256:x" {
		t.Fatalf("%+v", m)
	}
	for _, raw := range []string{
		`{"format_version":1,"bundles":[{"service":"crm","version":"v1","operations":[{"public_id":"crm.contacts.list","permissions":{"allOf":["crm.contact.read"]}}]}]}`,
		`{"format_version":1,"bundles":[{"service":"crm","version":"v1","operations":[{"public_id":"crm.contacts.list","permissions":["CRM.contact.read"]}]}]}`,
		`{"format_version":2,"bundles":[{"service":"crm","version":"v1","operations":[{"public_id":"crm.contacts.list","permissions":["crm.contact.read"]}]}]}`,
		`{"format_version":2,"bundles":[{"service":"crm","operations":[{"public_id":"crm.contacts.list"}]}]}`,
		`{"format_version":3,"bundles":[]}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestKeyRequirementStrictDecode(t *testing.T) {
	for _, raw := range []string{
		`null`,
		`[]`,
		`["crm.contact.read"]`,
		`{}`,
		`{"allOf":null}`,
		`{"anyOf":null}`,
		`{"allOf":[],"anyOf":null}`,
		`{"allOf":[],"allOf":[]}`,
		`{"AllOf":[]}`,
		`{"allOf":["crm.contact.read"],"anyOf":["crm.contact.read","crm.contact.export"]}`,
		`{"allOf":["crm.contact.read"],"keys":[]}`,
		`{"anyOf":[]}`,
		`{"anyOf":["crm.contact.read"]}`,
		`{"anyOf":["crm.contact.read","crm.contact.read"]}`,
		`{"anyOf":["crm.contact.read","crm.contact"]}`,
		`{"allOf":["crm.contact.read",""]}`,
		`{"allOf":["crm.Contact.read"]}`,
		`{"allOf":[null]}`,
		`{"allOf":[1]}`,
	} {
		var r KeyRequirement
		if err := json.Unmarshal([]byte(raw), &r); err == nil {
			t.Errorf("accepted %s as %+v", raw, r)
		}
	}
	var r KeyRequirement
	if err := json.Unmarshal([]byte(`{"allOf":[]}`), &r); err != nil || r.IsAnyOf() || r.Keys() != nil {
		t.Fatalf("keyless allOf: %v %+v", err, r)
	}
	if err := json.Unmarshal([]byte(`{"anyOf":["platform.identity.administer","crm.contact.read"]}`), &r); err != nil ||
		!r.IsAnyOf() || !slices.Equal(r.Keys(), []string{"crm.contact.read", "platform.identity.administer"}) {
		t.Fatalf("anyOf: %v %+v", err, r)
	}
	for _, c := range []struct {
		req  func() (KeyRequirement, error)
		want string
	}{
		{func() (KeyRequirement, error) { return KeyRequirement{}, nil }, `{"allOf":[]}`},
		{func() (KeyRequirement, error) { return AllOf("crm.contact.read", "crm.contact.export") }, `{"allOf":["crm.contact.export","crm.contact.read"]}`},
		{func() (KeyRequirement, error) { return AnyOf("crm.contact.read", "crm.contact.export") }, `{"anyOf":["crm.contact.export","crm.contact.read"]}`},
	} {
		req, err := c.req()
		if err != nil {
			t.Fatal(err)
		}
		if got, err := json.Marshal(req); err != nil || string(got) != c.want {
			t.Errorf("marshal = %s, %v; want %s", got, err, c.want)
		}
	}
}

func TestEmbeddedManifestLoads(t *testing.T) {
	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	op, ok := m.Operation("crm.contacts.list")
	if !ok || !slices.Equal(op.Permissions.Keys(), []string{"crm.contact.read"}) {
		t.Fatalf("%+v", op)
	}
}
