package server

import (
	"encoding/json"
	"testing"
)

func TestMultiServiceDiscovery(t *testing.T) {
	d := Production()
	d.Services = map[string]Service{"crm": {BasePath: "/crm/v1"}, "identity": {BasePath: "/identity/v1"}, "whatsapp": {BasePath: "/whatsapp/v1"}}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var parsed Discovery
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if err := parsed.Validate(ProductionOrigin, true); err != nil {
		t.Fatal(err)
	}
	for name, svc := range d.Services {
		got, ok := parsed.ServiceURL(name)
		if !ok || got != ProductionOrigin+svc.BasePath {
			t.Fatal(name, got)
		}
	}
	if _, ok := parsed.ServiceURL("missing"); ok {
		t.Fatal("invented endpoint")
	}
	parsed.Services["identity"] = Service{BasePath: "https://other.invalid/identity/v1"}
	if err := parsed.Validate(ProductionOrigin, true); err == nil {
		t.Fatal("accepted foreign origin")
	}
}
