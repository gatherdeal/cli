package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/gatherdeal/cli/contracts"
	"github.com/gatherdeal/cli/internal/auth"
	"github.com/gatherdeal/cli/internal/output"
)

// Batch commands (`<app> <resource> batch-<verb> --file`) send their items
// in chunks of the operation's limit. Each chunk is one atomic request keyed
// "<key>.<n>"; chunks are independent, so nothing is atomic across them.

// batchSpec is the generated batch declaration of an operation.
type batchSpec struct {
	MaxItems int
}

// Item states of a batch command's output.
const (
	batchSucceeded   = "succeeded"
	batchErrored     = "errored"
	batchUncommitted = "uncommitted"
	batchUnknown     = "unknown"
)

// Chunk states reported on standard error and in error.details.chunks.
const (
	chunkCommitted = "committed"
	chunkRefused   = "refused"
	chunkUnknown   = "unknown"
	chunkNotSent   = "not_sent"
)

// batchMaxAttempts bounds the sends of one chunk whose outcome is unknown.
// Every resend repeats the identical request and key.
const batchMaxAttempts = 3

const (
	batchBaseDelay = 500 * time.Millisecond
	batchMaxDelay  = 10 * time.Second
)

// batchRow is the outcome of one input item, by its index in the input.
type batchRow struct {
	Index   int             `json:"index"`
	State   string          `json:"state"`
	Data    json.RawMessage `json:"data,omitempty"`
	Problem json.RawMessage `json:"problem,omitempty"`
}

// batchChunk is one frozen request of the plan.
type batchChunk struct {
	n     int
	first int
	items []jsontext.Value
	key   string
	input any
	body  jsontext.Value
}

func (c *batchChunk) last() int { return c.first + len(c.items) - 1 }

// chunkReport summarizes one chunk in error.details.chunks.
type chunkReport struct {
	Chunk          int          `json:"chunk"`
	FirstIndex     int          `json:"first_index"`
	LastIndex      int          `json:"last_index"`
	IdempotencyKey string       `json:"idempotency_key"`
	State          string       `json:"state"`
	Attempts       int          `json:"attempts"`
	Complete       *bool        `json:"complete,omitempty"`
	Error          *chunkFailed `json:"error,omitempty"`

	conflict *output.Error
}

const codeKeyConflict = "IDEMPOTENCY_KEY_CONFLICT"

func isKeyConflict(e *output.Error) bool {
	return e.HTTPStatus == http.StatusConflict && e.Code == codeKeyConflict
}

type chunkFailed struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	HTTPStatus    int    `json:"http_status,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
}

type batchSummary struct {
	Succeeded   int           `json:"succeeded"`
	Errored     int           `json:"errored"`
	Uncommitted int           `json:"uncommitted"`
	Unknown     int           `json:"unknown"`
	Chunks      []chunkReport `json:"chunks"`
}

var flagStopOnError = FlagSpec{Name: "stop-on-error", Kind: kindBool, Usage: "Stop after the first chunk that does not commit and report the items of the chunks not sent as uncommitted. By default every chunk is sent."}

// batchCommand turns a generated command into the batch form.
func batchCommand(c *Command, op contracts.Operation, spec batchSpec, params []commandParameter, bind bindInput, connect func(*Invocation) (invokeInput, error)) *Command {
	if op.ReadOnly() || op.Idempotency != contracts.IdempotencyKeyed || spec.MaxItems < 1 {
		panic(op.PublicID + ": a batch operation must be a keyed write with a positive item limit")
	}
	c.Summary = strings.TrimSuffix(c.Summary, ".") + fmt.Sprintf(" from --file in chunks of up to %d items; each chunk is atomic, the chunks are independent.", spec.MaxItems)
	run := func(ctx context.Context, inv *Invocation) (any, error) {
		return runBatch(ctx, inv, op, spec, params, bind, connect)
	}
	c.Run = run
	confirmation := ConfirmConfig
	if op.Effect.Interaction == "external" {
		confirmation = ConfirmSend
	}
	c.Write = &WritePolicy{Confirmation: confirmation, Preview: run}
	c.Flags = append(c.Flags, c.Write.flags(op)...)
	c.Flags = append(c.Flags,
		FlagSpec{Name: "file", Kind: kindString, Required: true, Usage: fmt.Sprintf("Items to write: a JSON Lines file (one item per line) or a file holding one JSON array of items; - reads standard input. Each item is one request item of the operation; the items are sent in input order, %d per request.", spec.MaxItems)},
		flagStopOnError,
	)
	return c
}

func runBatch(ctx context.Context, inv *Invocation, op contracts.Operation, spec batchSpec, params []commandParameter, bind bindInput, connect func(*Invocation) (invokeInput, error)) (any, error) {
	values, err := inv.bindingArguments(params)
	if err != nil {
		return nil, err
	}
	if inv.isSet("outlet") {
		if err := validateOutlet(inv); err != nil {
			return nil, err
		}
	}
	items, err := readBatchItems(inv.str("file"), inv.Env.Stdin)
	if err != nil {
		return nil, err
	}
	schemas := writeContracts()[op.PublicID]
	plan, err := planBatch(items, spec.MaxItems, inv.Meta.Write.IdempotencyKey, values, bind, schemas.request)
	if err != nil {
		return nil, err
	}
	if inv.Meta.Write.DryRun {
		if _, err := inv.resolveTarget(false); err != nil {
			return nil, err
		}
		return batchPreview(plan, values, selectedOutlet(inv)), nil
	}
	invoke, err := connect(inv)
	if err != nil {
		return nil, err
	}
	if err := inv.recordWriteKey(); err != nil {
		return nil, err
	}
	base := inv.Meta.Write.IdempotencyKey
	inv.Env.stderrf("Sending %d items in %d chunks; idempotency key %s (chunk n is sent as %s.<n>).\n", len(items), len(plan), base, base)

	rows := make([]batchRow, len(items))
	for i := range rows {
		rows[i] = batchRow{Index: i, State: batchUncommitted}
	}
	reports := make([]chunkReport, len(plan))
	for i, c := range plan {
		reports[i] = chunkReport{Chunk: c.n, FirstIndex: c.first, LastIndex: c.last(), IdempotencyKey: c.key, State: chunkNotSent}
	}
	var firstFailure error
	for i, c := range plan {
		res := inv.sendChunk(ctx, invoke, c, schemas.receipt)
		reports[i].Attempts = res.attempts
		reports[i].State = res.state
		applyChunk(rows, c, res, &reports[i])
		inv.Env.stderrf("chunk %d (items %d-%d, key %s): %s\n", c.n, c.first, c.last(), c.key, res.state)
		if res.state == chunkCommitted {
			continue
		}
		if firstFailure == nil {
			firstFailure = res.err
		}
		if ctx.Err() != nil {
			break
		}
		if inv.boolean("stop-on-error") && i+1 < len(plan) {
			inv.Env.stderrf("Stopped after chunk %d (--stop-on-error); chunks %d-%d were not sent and their items are uncommitted.\n", c.n, c.n+1, plan[len(plan)-1].n)
			break
		}
	}
	return rows, batchOutcome(rows, reports, firstFailure)
}

// readBatchItems reads JSON Lines or one JSON array of objects.
func readBatchItems(path string, stdin io.Reader) ([]jsontext.Value, error) {
	if path == "" {
		return nil, usageError("--file is required (a JSON Lines file, a JSON array file, or - for standard input)")
	}
	reader := stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return nil, usageError("cannot open --file: %v", err)
		}
		defer file.Close()
		reader = file
	}
	if reader == nil {
		return nil, usageError("standard input is unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxInputBytes+1))
	if err != nil {
		return nil, usageError("cannot read --file: %v", err)
	}
	if len(data) > maxInputBytes {
		return nil, usageError("--file exceeds %d bytes", maxInputBytes)
	}
	var items []jsontext.Value
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '[' {
		dec := jsontext.NewDecoder(bytes.NewReader(trimmed))
		var array []jsontext.Value
		value, err := dec.ReadValue()
		if err == nil {
			err = json.Unmarshal(value, &array)
		}
		if err != nil {
			return nil, usageError("invalid --file JSON array: %v", err)
		}
		if _, err := dec.ReadValue(); !errors.Is(err, io.EOF) {
			return nil, usageError("--file must hold one JSON array or one JSON value per line")
		}
		for i, item := range array {
			if item.Kind() != '{' {
				return nil, usageError("--file item %d must be a JSON object", i)
			}
		}
		items = array
	} else {
		for n, line := range bytes.Split(data, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			value := jsontext.Value(line)
			if !value.IsValid() || value.Kind() != '{' {
				return nil, usageError("--file line %d (item %d) must be one JSON object", n+1, len(items))
			}
			items = append(items, bytes.Clone(value))
		}
	}
	if len(items) == 0 {
		return nil, usageError("--file holds no items")
	}
	return items, nil
}

// planBatch freezes the chunks: their items, keys and bound requests. Every
// item is checked against the operation's request schema before anything is
// sent; the server stays authoritative for everything else.
func planBatch(items []jsontext.Value, size int, base string, values map[string]jsontext.Value, bind bindInput, schema *jsonschema.Schema) ([]*batchChunk, error) {
	if schema == nil {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeInternal, "batch operation has no request schema")
	}
	for i, item := range items {
		if err := validateJSON(batchBody(item), schema); err != nil {
			return nil, usageError("invalid --file item %d: %v", i, err)
		}
	}
	var plan []*batchChunk
	for first := 0; first < len(items); first += size {
		c := &batchChunk{n: len(plan), first: first, items: items[first:min(first+size, len(items))]}
		if base != "" {
			c.key = base + "." + strconv.Itoa(c.n)
			if !idempotencyKeyRE.MatchString(c.key) {
				return nil, usageError("--idempotency-key is too long: chunk keys %s.<n> must stay within 128 characters", base)
			}
		}
		input, err := bind(values, batchBody(c.items...))
		if err != nil {
			return nil, usageError("invalid --file items %d-%d: %v", c.first, c.last(), err)
		}
		body, err := json.Marshal(input)
		if err != nil {
			return nil, usageError("cannot encode --file items %d-%d: %v", c.first, c.last(), err)
		}
		if err := validateJSON(body, schema); err != nil {
			return nil, usageError("invalid encoded --file items %d-%d: %v", c.first, c.last(), err)
		}
		c.input, c.body = input, body
		plan = append(plan, c)
	}
	return plan, nil
}

func batchBody(items ...jsontext.Value) jsontext.Value {
	var b bytes.Buffer
	b.WriteString(`{"items":[`)
	for i, item := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(item)
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

func batchPreview(plan []*batchChunk, values map[string]jsontext.Value, outlet string) any {
	type chunk struct {
		Chunk          int            `json:"chunk"`
		FirstIndex     int            `json:"first_index"`
		LastIndex      int            `json:"last_index"`
		IdempotencyKey string         `json:"idempotency_key"`
		Body           jsontext.Value `json:"body"`
	}
	chunks := make([]chunk, len(plan))
	for i, c := range plan {
		chunks[i] = chunk{c.n, c.first, c.last(), c.key, c.body}
	}
	return struct {
		Arguments map[string]jsontext.Value `json:"arguments,omitempty"`
		Outlet    string                    `json:"outlet,omitempty"`
		Chunks    []chunk                   `json:"chunks"`
	}{values, outlet, chunks}
}

type chunkResult struct {
	state    string
	attempts int
	results  []json.RawMessage
	err      error
	// keyConflict is a key conflict answered while the key was clean: the
	// key holds another input, possibly committed by an earlier run.
	keyConflict bool
}

// sendChunk sends one frozen chunk, always as the same request with the
// same key, at most batchMaxAttempts times. A 429 on a clean key is resent
// after its retry delay and the key stays clean; when the attempts run out
// the chunk is refused (nothing committed). Any other 4xx on a clean key
// refuses it at once. An unknown outcome (network error, timeout, 5xx,
// unusable receipt) is resent; a key conflict, and any failure other than
// a 5xx after an unknown outcome, end the chunk as unknown.
func (inv *Invocation) sendChunk(ctx context.Context, invoke invokeInput, c *batchChunk, receipt *jsonschema.Schema) chunkResult {
	clean := true
	for attempt := 1; ; attempt++ {
		raw, corr, err := invoke(ctx, c.input, c.key)
		inv.Meta.CorrelationID = corr
		if err == nil {
			results, rerr := batchResults(raw, receipt, len(c.items), corr)
			if rerr == nil {
				return chunkResult{state: chunkCommitted, attempts: attempt, results: results}
			}
			err = output.UnknownWriteOutcome(rerr)
		}
		err = auth.Explain(err)
		e := output.AsError(err)
		resend := false
		switch {
		case ctx.Err() != nil:
		case e.Code == output.CodeWriteOutcomeUnknown || e.HTTPStatus >= 500:
			resend = true
		case isKeyConflict(e):
			if clean {
				return chunkResult{state: chunkUnknown, attempts: attempt, err: err, keyConflict: true}
			}
		case clean && e.HTTPStatus == http.StatusTooManyRequests:
			if attempt >= batchMaxAttempts || inv.sleep(ctx, batchDelay(attempt, e)) != nil {
				return chunkResult{state: chunkRefused, attempts: attempt, err: err}
			}
			continue
		case clean && e.HTTPStatus >= 400 && e.HTTPStatus < 500:
			return chunkResult{state: chunkRefused, attempts: attempt, err: err}
		}
		clean = false
		if !resend || attempt >= batchMaxAttempts {
			return chunkResult{state: chunkUnknown, attempts: attempt, err: err}
		}
		if inv.sleep(ctx, batchDelay(attempt, e)) != nil {
			return chunkResult{state: chunkUnknown, attempts: attempt, err: err}
		}
	}
}

func batchDelay(attempt int, e *output.Error) time.Duration {
	delay := batchBaseDelay << (attempt - 1)
	var p struct {
		RetryAfterMS int64 `json:"retry_after_ms"`
	}
	if json.Unmarshal(e.Problem, &p) == nil {
		delay = max(delay, time.Duration(p.RetryAfterMS)*time.Millisecond)
	}
	delay = max(delay, e.RetryAfter)
	return min(delay, batchMaxDelay)
}

func (inv *Invocation) sleep(ctx context.Context, d time.Duration) error {
	if inv.Env.Sleep != nil {
		return inv.Env.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// batchResults checks the receipt and returns one result per item.
func batchResults(raw jsontext.Value, schema *jsonschema.Schema, n int, corr string) ([]json.RawMessage, error) {
	if schema != nil {
		if err := validateJSON(raw, schema); err != nil {
			return nil, err
		}
	}
	var envelope struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Results) != n {
		return nil, fmt.Errorf("batch receipt has %d results for %d items (correlation id %s)", len(envelope.Results), n, corr)
	}
	return envelope.Results, nil
}

// applyChunk records a chunk's outcome on its rows.
func applyChunk(rows []batchRow, c *batchChunk, res chunkResult, report *chunkReport) {
	switch res.state {
	case chunkCommitted:
		for i, data := range res.results {
			rows[c.first+i] = batchRow{Index: c.first + i, State: batchSucceeded, Data: data}
		}
		return
	case chunkUnknown:
		// A key conflict is answered for the whole chunk; each item carries it.
		var problem json.RawMessage
		if res.keyConflict {
			problem = output.AsError(res.err).Problem
		}
		for i := range c.items {
			rows[c.first+i].State = batchUnknown
			rows[c.first+i].Problem = problem
		}
	case chunkRefused:
		if complete, problems, ok := itemProblems(res.err, len(c.items)); ok {
			report.Complete = &complete
			for i, p := range problems {
				rows[c.first+i] = batchRow{Index: c.first + i, State: batchErrored, Problem: p}
			}
		}
	}
	e := output.AsError(res.err)
	if res.keyConflict {
		report.conflict = e
	}
	report.Error = &chunkFailed{Code: e.Code, Message: e.Message, HTTPStatus: e.HTTPStatus, CorrelationID: e.CorrelationID}
}

// itemProblems decodes a BATCH_REJECTED problem into item problems by index
// within the chunk. A malformed rejection lists nothing: every item of the
// chunk stays uncommitted.
func itemProblems(err error, n int) (bool, map[int]json.RawMessage, bool) {
	e := output.AsError(err)
	if e.Code != "BATCH_REJECTED" || e.HTTPStatus != http.StatusUnprocessableEntity || len(e.Problem) == 0 {
		return false, nil, false
	}
	var p struct {
		Details struct {
			Complete bool `json:"complete"`
		} `json:"details"`
		Errors []json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(e.Problem, &p) != nil {
		return false, nil, false
	}
	problems := make(map[int]json.RawMessage, len(p.Errors))
	for _, raw := range p.Errors {
		var item struct {
			Index *int   `json:"index"`
			Code  string `json:"code"`
		}
		if json.Unmarshal(raw, &item) != nil || item.Index == nil || *item.Index < 0 || *item.Index >= n || item.Code == "" {
			return false, nil, false
		}
		if _, dup := problems[*item.Index]; dup {
			return false, nil, false
		}
		problems[*item.Index] = raw
	}
	return p.Details.Complete, problems, true
}

// batchOutcome is nil when every item succeeded. An unknown chunk makes the
// whole outcome unknown; otherwise a batch that committed some chunks is
// partial. When nothing committed, the first failed chunk's own error is
// the command's error.
func batchOutcome(rows []batchRow, reports []chunkReport, first error) error {
	s := batchSummary{Chunks: reports}
	// conflict is the first key conflict when every unknown chunk ended in
	// one: the server wrote nothing for this input, but an earlier run
	// under the key may have.
	var conflict *output.Error
	for _, r := range reports {
		if r.State != chunkUnknown {
			continue
		}
		if r.conflict == nil {
			conflict = nil
			break
		}
		if conflict == nil {
			conflict = r.conflict
		}
	}
	for _, r := range rows {
		switch r.State {
		case batchSucceeded:
			s.Succeeded++
		case batchErrored:
			s.Errored++
		case batchUncommitted:
			s.Uncommitted++
		case batchUnknown:
			s.Unknown++
		}
	}
	switch {
	case s.Succeeded == len(rows):
		return nil
	case s.Unknown > 0 && conflict != nil:
		// Same exit code and error code as a single write's key conflict.
		e := *conflict
		e.Cause = conflict
		return e.WithDetails(s).
			WithHint("The --idempotency-key was already used with a different input, so the server wrote nothing for this one. Items marked unknown may have been written by that earlier run: to finish it, resend its original file, unedited, with this key. For a new import, use a new --idempotency-key.")
	case s.Unknown > 0:
		return output.UnknownWriteOutcome(first).WithDetails(s).
			WithHint("Items in unknown state may have been written. Rerun with the same --file and --idempotency-key to replay committed chunks and resolve unknown ones, or check them through a read command; never edit or reorder the file before such a rerun.")
	case s.Succeeded > 0:
		return output.Errorf(output.ExitPartial, output.CodeBatchIncomplete, "%d of %d items were not written; data lists the state of each item", len(rows)-s.Succeeded, len(rows)).
			WithDetails(s).WithCause(first).
			WithHint("Succeeded items are committed. Fix or drop errored items and send the remaining items again as a new batch (a new --idempotency-key).")
	case first != nil:
		return first
	}
	return output.Errorf(output.ExitUnclassified, output.CodeInternal, "batch ended without a result")
}
