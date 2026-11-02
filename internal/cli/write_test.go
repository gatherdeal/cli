package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/contracts"
	"github.com/gatherdeal/cli/internal/output"
)

func TestWritePreparation(t *testing.T) {
	op := contracts.Operation{PublicID: "test.messages.create", Effect: contracts.Effect{Mutation: "additive", Interaction: "external"}, Idempotency: contracts.IdempotencyKeyed}
	policy := WritePolicy{Confirmation: ConfirmSend}
	var meta output.Meta
	err := PrepareWrite(op, policy, WriteOptions{}, &meta)
	if output.AsError(err).Exit != output.ExitActionRequired {
		t.Fatal(err)
	}
	if err := PrepareWrite(op, policy, WriteOptions{DryRun: true}, &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.Write.DryRun || !meta.Write.IdempotencyKeyGenerated || len(meta.Write.IdempotencyKey) != 32 {
		t.Fatalf("%+v", meta.Write)
	}
	key := meta.Write.IdempotencyKey
	if err := PrepareWrite(op, policy, WriteOptions{Confirmed: true, IdempotencyKey: key}, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Write.IdempotencyKey != key || meta.Write.IdempotencyKeyGenerated {
		t.Fatalf("%+v", meta.Write)
	}
	if err := PrepareWrite(op, WritePolicy{Confirmation: ConfirmConfig}, WriteOptions{Confirmed: true}, &meta); err == nil {
		t.Fatal("send accepted with config confirmation")
	}
	op.Idempotency = "none"
	if err := PrepareWrite(op, policy, WriteOptions{Confirmed: true, IdempotencyKey: key}, &meta); err == nil {
		t.Fatal("unsupported key accepted")
	}
	op.Effect.Interaction = "closed"
	if err := PrepareWrite(op, WritePolicy{Confirmation: ConfirmConfig}, WriteOptions{Confirmed: true}, &meta); err != nil {
		t.Fatal(err)
	}
}
func TestTypedWriteBody(t *testing.T) {
	type body struct {
		Message string `json:"message"`
	}
	for _, raw := range []string{`null`, `{}`, `{"message":42}`, `{"unknown":true}`, `{"message":"a"} {}`, `{"message":`} {
		got, err := ReadBody[body]("-", strings.NewReader(raw))
		if raw == `{}` {
			if err != nil || got == nil {
				t.Fatal(err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	raw := `{"message":"hello\nworld"}`
	file := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadBody[body](file, nil)
	if err != nil || got.Message != "hello\nworld" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := ReadBody[body]("-", bytes.NewReader(make([]byte, maxInputBytes+1))); err == nil {
		t.Fatal("accepted oversized body")
	}
}
func TestUnknownWriteEnvelope(t *testing.T) {
	cause := errors.New("connection lost")
	err := output.UnknownWriteOutcome(cause)
	meta := output.Meta{Write: &output.WriteMeta{IdempotencyKey: "original-key", IdempotencyKeyGenerated: true}}
	env := output.Failure(err, meta)
	if env.ExitCode != output.ExitUnavailable || env.Error.Code != output.CodeWriteOutcomeUnknown || env.Error.Retryable || !errors.Is(err, cause) {
		t.Fatalf("%+v", env)
	}
	var buf bytes.Buffer
	if err := output.Write(&buf, env, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"idempotency_key":"original-key"`) {
		t.Fatal(buf.String())
	}
}

func TestWriteDispatch(t *testing.T) {
	op := contracts.Operation{PublicID: "test.settings.update", Effect: contracts.Effect{Mutation: "non_additive", Interaction: "closed"}, Idempotency: contracts.IdempotencyKeyed}
	for _, tc := range []struct {
		args             []string
		preview, execute bool
		exit             output.ExitCode
	}{
		{args: []string{"--dry-run"}, preview: true},
		{args: []string{"--dry-run", "--yes"}, preview: true},
		{args: []string{"--yes"}, execute: true},
		{exit: output.ExitActionRequired},
	} {
		previews, writes := 0, 0
		policy := &WritePolicy{Confirmation: ConfirmConfig, Preview: func(context.Context, *Invocation) (any, error) { previews++; return "preview", nil }}
		cmd := &Command{Path: []string{"test"}, Write: policy, Flags: policy.Flags(), Run: func(context.Context, *Invocation) (any, error) { writes++; return "accepted", nil }}
		inv, err := parse(&Env{}, cmd, tc.args)
		if err != nil {
			t.Fatal(err)
		}
		_, err = inv.runWrite(context.Background(), op)
		if tc.exit != 0 {
			if err == nil || output.AsError(err).Exit != tc.exit {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		if (previews == 1) != tc.preview || (writes == 1) != tc.execute {
			t.Fatalf("%v: previews=%d writes=%d", tc.args, previews, writes)
		}
	}
}

func TestInlineWriteBody(t *testing.T) {
	type body struct {
		Message string `json:"message"`
	}
	for _, raw := range []string{`{"message":"hello"}`, " \t\n" + `{"message":"hello"}` + "\n "} {
		got, err := ReadBody[body](raw, strings.NewReader("invalid stdin"))
		if err != nil || got == nil || got.Message != "hello" {
			t.Fatalf("%+v %v", got, err)
		}
	}
	got, err := ReadBody[[]body](" \n"+`[{"message":"hello"}]`, nil)
	if err != nil || got == nil || len(*got) != 1 || (*got)[0].Message != "hello" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, tc := range []struct{ name, raw string }{
		{"malformed", `{"message":`},
		{"unknown field", `{"unknown":true}`},
		{"wrong type", `{"message":42}`},
		{"trailing document", `{} {}`},
		{"malformed array", `[`},
		{"oversized", `{"message":"` + strings.Repeat("a", maxInputBytes) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadBody[body](tc.raw, nil)
			if err == nil || output.AsError(err).Exit != output.ExitUsage || output.AsError(err).Code != output.CodeUsage {
				t.Fatalf("expected usage error, got %v", err)
			}
			if strings.Contains(err.Error(), "cannot open") {
				t.Fatalf("inline JSON treated as a file: %v", err)
			}
		})
	}
}
