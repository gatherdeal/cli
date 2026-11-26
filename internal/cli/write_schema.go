package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/gatherdeal/cli/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Use the route's request and success schemas, including envelope allOf
// and nested variant refs. The receipt is the schema of the operation's one
// declared 2xx response (200, 201 or 202); a 204 has no receipt schema.
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
			} `json:"schema"`
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
		// A schema is a component reference or, for batch envelopes, written
		// in place; pointer locates the in-place schema in the document.
		compile := func(p payload, pointer string) *jsonschema.Schema {
			m, ok := p.Content["application/json"]
			if !ok {
				return nil
			}
			if m.Schema.Ref != "" {
				return compiler.MustCompile(location + m.Schema.Ref)
			}
			return compiler.MustCompile(location + "#" + pointer + "/content/application~1json/schema")
		}
		for name, path := range routes.Paths {
			for method, route := range path {
				if contracts.MustOperation(route.ID).ReadOnly() {
					continue
				}
				at := "/paths/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(name) + "/" + method
				status := successStatus(route.ID, route.Responses)
				index[route.ID] = writeSchemas{compile(route.RequestBody, at+"/requestBody"), compile(route.Responses[status], at+"/responses/"+status)}
			}
		}
	}
	return index
})

// successStatus returns the status of the operation's single declared 2xx response.
func successStatus[P any](id string, responses map[string]P) string {
	var found []string
	for status := range responses {
		if len(status) == 3 && status[0] == '2' {
			found = append(found, status)
		}
	}
	if len(found) != 1 {
		panic(fmt.Sprintf("contracts: %s declares %d success responses %v, want 1", id, len(found), found))
	}
	return found[0]
}

func validateJSON(raw []byte, schema *jsonschema.Schema) error {
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return schema.Validate(value)
}
