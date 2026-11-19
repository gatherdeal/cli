package cli

import (
	"bytes"
	"encoding/json"
	"sync"

	"github.com/gatherdeal/cli/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Use the route's request and success schemas, including envelope allOf
// and nested variant refs. A 204 has no receipt schema.
type writeSchemas struct{ request, receipt *jsonschema.Schema }

var writeContracts = sync.OnceValue(func() map[string]writeSchemas {
	index := map[string]writeSchemas{}
	m, err := contracts.Load()
	if err != nil {
		panic(err)
	}
	for _, bundle := range m.Bundles {
		data, err := bundle.OpenAPI()
		if err != nil {
			panic(err)
		}
		type media struct {
			Schema struct {
				Ref string `json:"$ref"`
			}
		}
		type payload struct{ Content map[string]media }
		var routes struct {
			Paths map[string]map[string]struct {
				ID          string `json:"x-gd-public-id"`
				RequestBody payload
				Responses   map[string]payload
			}
		}
		if err := json.Unmarshal(data, &routes); err != nil {
			panic(err)
		}
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		compiler.AssertFormat()
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			panic(err)
		}
		location := "file:///" + bundle.Contract
		if err := compiler.AddResource(location, doc); err != nil {
			panic(err)
		}
		compile := func(p payload) *jsonschema.Schema {
			ref := p.Content["application/json"].Schema.Ref
			if ref == "" {
				return nil
			}
			return compiler.MustCompile(location + ref)
		}
		for _, path := range routes.Paths {
			for _, route := range path {
				if contracts.MustOperation(route.ID).ReadOnly() {
					continue
				}
				index[route.ID] = writeSchemas{compile(route.RequestBody), compile(route.Responses["200"])}
			}
		}
	}
	return index
})

func validateJSON(raw []byte, schema *jsonschema.Schema) error {
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return schema.Validate(value)
}
