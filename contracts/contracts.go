// Package contracts embeds the public API contract bundle the CLI is built
// against: manifest.json and each service's OpenAPI document, committed
// exactly as the contract generator wrote them. The reader and its tests
// are hand-written; generated artifacts are never edited.
//
// Command flags that mirror contract declarations (for example the
// delegated expansions of an operation) are derived from the manifest at
// start-up, so the CLI never keeps a second copy of those lists.
package contracts

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// ManifestJSON is contracts/manifest.json.
//
//go:embed manifest.json
var ManifestJSON []byte

// Documents embeds all generated service/version documents. Adding a bundle
// requires no new embed variable or handwritten operation registry.
//
//go:embed */*/openapi.json
var Documents embed.FS

func (b Bundle) OpenAPI() ([]byte, error) {
	return Documents.ReadFile(strings.TrimPrefix(b.Contract, "contracts/"))
}

// Manifest is the subset of manifest.json the CLI reads. Unknown fields are
// ignored so a newer generator can add some.
type Manifest struct {
	FormatVersion int      `json:"format_version"`
	Bundles       []Bundle `json:"bundles"`
}

// Bundle is one service version's contract.
type Bundle struct {
	Service     string      `json:"service"`
	Version     string      `json:"version"`
	Server      string      `json:"server"`
	ServicePath string      `json:"service_path"`
	Contract    string      `json:"contract"`
	GoPackage   string      `json:"go_package"`
	Digest      string      `json:"digest"`
	Operations  []Operation `json:"operations"`
}

// Operation is one published operation.
type Operation struct {
	Effect               Effect       `json:"effect"`
	Permissions          []string     `json:"permissions"`
	OutletScope          OutletPolicy `json:"outlet_scope"`
	Idempotency          Idempotency  `json:"idempotency"`
	PublicID             string       `json:"public_id"`
	Command              string       `json:"command"`
	Method               string       `json:"method"`
	Path                 string       `json:"path"`
	DelegatedEdgeFilters []string     `json:"delegated_edge_filters"`
	// DelegatedExpansions are the with_* query parameters a delegated
	// access may set; the server refuses every other expansion.
	DelegatedExpansions []string `json:"delegated_expansions"`
	// Pagination is set on list operations that return a ListPage.
	Pagination *Pagination `json:"pagination"`
	// Errors are the problem codes the operation declares.
	Errors     []OperationError `json:"errors"`
	GoMethod   string           `json:"go_method"`
	InputType  string           `json:"input_type"`
	OutputType string           `json:"output_type"`
}

// PaginationAdaptive is the only list style this CLI speaks: page numbers
// and cursors on the same route.
const PaginationAdaptive = "adaptive"

// Pagination describes a list operation's paging parameters and limits.
type Pagination struct {
	Style           string `json:"style"`
	PageParam       string `json:"page_param"`
	PageSizeParam   string `json:"page_size_param"`
	AfterParam      string `json:"after_param"`
	TotalParam      string `json:"total_param"`
	DefaultPageSize int    `json:"default_page_size"`
	MaxPageSize     int    `json:"max_page_size"`
	MaxOffset       int    `json:"max_offset"`
}

// OperationError is one problem code an operation may answer.
type OperationError struct {
	Code      string `json:"code"`
	Status    int    `json:"status"`
	Retryable bool   `json:"retryable"`
}

// SupportedFormatVersion is the manifest format this package reads.
const SupportedFormatVersion = 1

var load = sync.OnceValues(func() (*Manifest, error) { return Parse(ManifestJSON) })

// Parse validates one generated inventory containing any number of bundles.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("contracts: manifest.json: %w", err)
	}
	if m.FormatVersion != SupportedFormatVersion {
		return nil, fmt.Errorf("contracts: manifest.json format_version %d, want %d", m.FormatVersion, SupportedFormatVersion)
	}
	seen := map[string]bool{}
	bundles := map[string]bool{}
	for _, b := range m.Bundles {
		key := b.Service + "/" + b.Version
		if bundles[key] {
			return nil, fmt.Errorf("contracts: duplicate bundle %s", key)
		}
		bundles[key] = true
		for _, op := range b.Operations {
			if seen[op.PublicID] || !strings.HasPrefix(op.PublicID, b.Service+".") {
				return nil, fmt.Errorf("contracts: duplicate or misowned operation %s", op.PublicID)
			}
			seen[op.PublicID] = true
		}
	}
	return &m, nil
}

// Load returns the parsed manifest.
func Load() (*Manifest, error) { return load() }

// Operation returns the operation with the given fully qualified public id
// (for example "crm.contacts.list").
func (m *Manifest) Operation(publicID string) (Operation, bool) {
	for _, b := range m.Bundles {
		for _, op := range b.Operations {
			if op.PublicID == publicID {
				return op, true
			}
		}
	}
	return Operation{}, false
}

// MustOperation is Load followed by Operation. It panics when the embedded
// manifest is unreadable or lacks the operation: both are build defects the
// contract check tests catch.
func MustOperation(publicID string) Operation {
	m, err := Load()
	if err != nil {
		panic(err)
	}
	op, ok := m.Operation(publicID)
	if !ok {
		panic(fmt.Sprintf("contracts: manifest.json has no operation %q", publicID))
	}
	return op
}

// Effect retains the producer's safety declaration, independent of HTTP method.
type Effect struct {
	Mutation    Mutation    `json:"mutation"`
	Interaction Interaction `json:"interaction"`
	Risks       []Risk      `json:"risks"`
}
type Idempotency string

const (
	IdempotencySafe  Idempotency = "safe"
	IdempotencyKeyed Idempotency = "keyed"
)

func (op Operation) ReadOnly() bool { return op.Effect.Mutation == "none" }

type Mutation string
type Interaction string
type Risk string
type OutletPolicy string
