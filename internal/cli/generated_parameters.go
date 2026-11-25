package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/gatherdeal/cli/contracts"
)

type commandParameter struct {
	bindingArgument
	flag                    FlagSpec
	shared, uuid, jsonValue bool
}

type parameterSchema struct {
	Ref    string            `json:"$ref"`
	Type   json.RawMessage   `json:"type"`
	Format string            `json:"format"`
	Enum   []string          `json:"enum"`
	OneOf  []parameterSchema `json:"oneOf"`
}

type wireParameter struct {
	Name, In, Description string
	Required              bool
	Schema                parameterSchema
}

// Generated argument descriptors name the parameter. Its scalar type, enum
// and help text live in the same bundle's embedded OpenAPI document, as do
// the request headers an operation requires (see requiresIfMatch).
var parameterIndex = sync.OnceValue(func() map[string]map[string]wireParameter {
	index := make(map[string]map[string]wireParameter)
	m, err := contracts.Load()
	if err != nil {
		panic(err)
	}
	for _, b := range m.Bundles {
		data, err := b.OpenAPI()
		if err != nil {
			panic(err)
		}
		var doc struct {
			Paths map[string]map[string]struct {
				ID         string `json:"x-gd-public-id"`
				Parameters []wireParameter
			}
			Components struct{ Schemas map[string]parameterSchema }
		}
		if err = json.Unmarshal(data, &doc); err != nil {
			panic(err)
		}
		for _, path := range doc.Paths {
			for _, op := range path {
				params := map[string]wireParameter{}
				for _, p := range op.Parameters {
					if p.In != "query" && p.In != "path" && p.In != "header" {
						continue
					}
					s := p.Schema
					for _, alternative := range s.OneOf {
						if string(alternative.Type) != `"null"` {
							s = alternative
							break
						}
					}
					if s.Ref != "" {
						s = doc.Components.Schemas[strings.TrimPrefix(s.Ref, "#/components/schemas/")]
					}
					p.Schema = s
					params[p.In+"/"+p.Name] = p
				}
				index[op.ID] = params
			}
		}
	}
	return index
})

func parameterFor(op contracts.Operation, a bindingArgument) commandParameter {
	wire, ok := parameterIndex()[op.PublicID][a.In+"/"+a.WireName]
	if !ok {
		panic(fmt.Sprintf("%s: missing %s parameter %s", op.PublicID, a.In, a.WireName))
	}
	p := commandParameter{bindingArgument: a, uuid: wire.Schema.Format == "uuid", flag: FlagSpec{Name: a.Name, Kind: kindString, Usage: wire.Description, Values: wire.Schema.Enum}}
	if p.flag.Usage == "" {
		p.flag.Usage = strings.ReplaceAll(a.WireName, "_", " ") + "."
	}
	switch string(wire.Schema.Type) {
	case `"integer"`:
		p.flag.Kind = kindInt
	case `"boolean"`:
		p.flag.Kind = kindBool
	case `"object"`, `"array"`:
		p.jsonValue = true
		p.flag.Usage = "JSON value for " + a.WireName + "."
	}
	if a.In != "query" {
		return p
	}
	if pg := op.Pagination; pg != nil {
		switch a.WireName {
		case pg.PageParam:
			p.flag.Name, p.shared = "page", true
		case pg.PageSizeParam:
			p.flag.Name, p.shared = "page-size", true
		case pg.AfterParam, pg.TotalParam:
			p.shared = true
		}
	}
	switch {
	case p.shared:
	case a.WireName == "q":
		p.flag = flagSearch
	case a.WireName == "sort" && len(op.SortableFields) > 0:
		p.flag = sortFlag(op.PublicID)
	case a.WireName == "limit":
		p.flag.Name = "count" // Single-poll record count. --limit means a cursor walk.
	}
	p.flag.Required = a.Required
	return p
}

// requiresIfMatch reports whether the operation's contract requires an
// If-Match header. Such a write takes a required --if-match flag (see
// WritePolicy.flags), and the binding sends its value unchanged.
func requiresIfMatch(publicID string) bool {
	return parameterIndex()[publicID]["header/If-Match"].Required
}
