package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/gatherdeal/cli/contracts"
	"github.com/gatherdeal/cli/internal/output"
)

// Confirmation identifies the explicit execution opt-in. It is not authorization.
type Confirmation string

const (
	ConfirmConfig Confirmation = "yes"
	ConfirmSend   Confirmation = "send"
)

// WritePolicy is attached to every command that writes. The API's
// idempotency policy always comes from that command's manifest operation.
type WritePolicy struct {
	Confirmation Confirmation
	// Preview must validate and render the request without calling a mutation endpoint.
	Preview func(context.Context, *Invocation) (any, error)
}

func (p WritePolicy) Flags() []FlagSpec {
	if p.Confirmation != ConfirmConfig && p.Confirmation != ConfirmSend {
		panic("invalid write confirmation policy")
	}
	return []FlagSpec{
		{Name: "dry-run", Kind: kindBool, Usage: "Preview the typed request without submitting the write."},
		{Name: string(p.Confirmation), Kind: kindBool, Usage: "Explicitly confirm execution of this write."},
		{Name: "idempotency-key", Kind: kindString, Usage: "Stable replay key; preserve it when reconciling an uncertain result."},
		{Name: "input", Kind: kindString, Usage: "JSON body: inline JSON, a file path, or - for standard input"},
	}
}

type WriteOptions struct {
	DryRun         bool
	Confirmed      bool
	IdempotencyKey string
	Input          string
}

func (inv *Invocation) writeOptions(policy WritePolicy) WriteOptions {
	return WriteOptions{DryRun: inv.boolean("dry-run"), Confirmed: inv.boolean(string(policy.Confirmation)), IdempotencyKey: inv.str("idempotency-key"), Input: inv.str("input")}
}

// PrepareWrite does not make a request. Call before executing a write; dry-run
// callers must return a typed preview and never invoke the mutation endpoint.
func PrepareWrite(op contracts.Operation, policy WritePolicy, opts WriteOptions, meta *output.Meta) error {
	if policy.Confirmation != ConfirmConfig && policy.Confirmation != ConfirmSend {
		return usageError("invalid write confirmation policy")
	}
	if op.ReadOnly() || op.Effect.Mutation == "" {
		return usageError("operation %s is not a declared write", op.PublicID)
	}
	if op.Effect.Interaction == "external" && policy.Confirmation != ConfirmSend {
		return usageError("external writes require --send")
	}
	key := opts.IdempotencyKey
	if key != "" && op.Idempotency != contracts.IdempotencyKeyed {
		return usageError("operation %s does not support --idempotency-key", op.PublicID)
	}
	if strings.TrimSpace(key) != key || strings.ContainsAny(key, "\r\n") {
		return usageError("invalid --idempotency-key")
	}
	meta.Write = &output.WriteMeta{DryRun: opts.DryRun, IdempotencyKey: key}
	if !opts.DryRun && !opts.Confirmed {
		return output.Errorf(output.ExitActionRequired, output.CodeWriteConfirmationRequired, "execution requires --%s", policy.Confirmation)
	}
	if key == "" && op.Idempotency == contracts.IdempotencyKeyed {
		var bytes [16]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return output.Errorf(output.ExitUnclassified, output.CodeInternal, "cannot generate idempotency key").WithCause(err)
		}
		meta.Write.IdempotencyKey = hex.EncodeToString(bytes[:])
		meta.Write.IdempotencyKeyGenerated = true
	}
	return nil
}

const maxInputBytes = 32 << 20

// ReadBody decodes exactly one JSON value into a concrete generated input.
// Unknown fields, oversized input and trailing documents fail before dispatch.
func ReadBody[T any](path string, stdin io.Reader) (*T, error) {
	reader := stdin
	if path == "" {
		return nil, usageError("--input is required (inline JSON, a file path, or - for standard input)")
	}
	trimmed := strings.TrimSpace(path)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		reader = strings.NewReader(path)
	} else if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return nil, usageError("cannot open --input: %v", err)
		}
		defer file.Close()
		reader = file
	}
	if reader == nil {
		return nil, usageError("standard input is unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxInputBytes+1))
	if err != nil {
		return nil, usageError("cannot read --input: %v", err)
	}
	if len(data) > maxInputBytes {
		return nil, usageError("--input exceeds %d bytes", maxInputBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var value *T
	if err := decoder.Decode(&value); err != nil {
		return nil, usageError("invalid --input JSON: %v", err)
	}
	if value == nil {
		return nil, usageError("--input must not be null")
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, usageError("--input must contain exactly one JSON value")
	}
	return value, nil
}

func (inv *Invocation) run(ctx context.Context) (any, error) {
	policy := inv.Command.Write
	if policy == nil {
		return inv.Command.Run(ctx, inv)
	}
	return inv.runWrite(ctx, inv.Command.Contract())
}

func (inv *Invocation) runWrite(ctx context.Context, op contracts.Operation) (any, error) {
	policy := inv.Command.Write
	opts := inv.writeOptions(*policy)
	if err := PrepareWrite(op, *policy, opts, &inv.Meta); err != nil {
		return nil, err
	}
	if opts.DryRun {
		if policy.Preview == nil {
			return nil, output.Errorf(output.ExitUnclassified, output.CodeInternal, "write command has no preview handler")
		}
		return policy.Preview(ctx, inv)
	}
	return inv.Command.Run(ctx, inv)
}
