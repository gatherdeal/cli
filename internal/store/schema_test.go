package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUnsupportedSchemaNeverRewritten(t *testing.T) {
	for _, version := range []int{0, 2, 3, 99} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			root := newRoot(t)
			unlock, err := Lock(root)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			p, err := LoadProfiles(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Save(); err != nil {
				t.Fatal(err)
			}
			raw := []byte(fmt.Sprintf(`{"schema_version":%d,"future_security_field":{"must_preserve":true},"profiles":{}}`, version))
			path := filepath.Join(root, profilesFileName)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			var schema *SchemaError
			if _, err := LoadProfiles(root); !errors.As(err, &schema) || schema.Found != version || schema.Supported != 1 {
				t.Fatalf("read: %v", err)
			}
			if err := p.Save(); !errors.As(err, &schema) {
				t.Fatalf("stale writer: %v", err)
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(raw) {
				t.Fatal("future fields dropped")
			}
			p.data.SchemaVersion = version
			if err := p.Save(); !errors.As(err, &schema) {
				t.Fatalf("future in-memory writer: %v", err)
			}

		})
	}
}

func TestInitialSchemaBindingRoundTrip(t *testing.T) {
	root := newRoot(t)
	unlock, err := Lock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	p, err := LoadProfiles(root)
	if err != nil {
		t.Fatal(err)
	}
	want := &Binding{OrganizationID: "org", PrincipalID: "principal"}
	p.Put("acme", &Profile{Binding: want})
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProfiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.data.SchemaVersion != 1 || !reflect.DeepEqual(got.Get("acme").Binding, want) {
		t.Fatal("initial schema binding changed")
	}
}

func TestBindingRequiresBothIDs(t *testing.T) {
	for _, binding := range []*Binding{{}, {OrganizationID: "org"}, {PrincipalID: "principal"}} {
		t.Run(fmt.Sprintf("%+v", binding), func(t *testing.T) {
			root := newRoot(t)
			unlock, err := Lock(root)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			p, err := LoadProfiles(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Save(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, profilesFileName)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			p.Put("acme", &Profile{Binding: binding})
			if err := p.Save(); err == nil {
				t.Fatal("saved incomplete binding")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("invalid save changed file")
			}
			raw, err := json.Marshal(p.data)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProfiles(root); err == nil {
				t.Fatal("loaded incomplete binding")
			}
		})
	}
}
