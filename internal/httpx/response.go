package httpx

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

// ResponseDoer sends through the origin-bound client. The outlet selector is
// read by the server's authentication stage rather than by the operation,
// so the contract's operation inputs do not carry it. It records the
// response's correlation id, which the generated outputs do not carry, and
// keeps the raw body of a problem document so the CLI can print the
// problem as received.
type ResponseDoer struct {
	Next interface {
		Do(*http.Request) (*http.Response, error)
	}
	OutletID      string
	CorrelationID string
	Problem       []byte
	// RetryAfter is the last response's Retry-After delay, 0 when absent.
	RetryAfter time.Duration
}

// retryAfter parses a Retry-After value: delay seconds or an HTTP date.
func retryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil {
		return time.Duration(max(s, 0)) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

func (d *ResponseDoer) Do(req *http.Request) (*http.Response, error) {
	if d.OutletID != "" {
		req = req.Clone(req.Context())
		q := req.URL.Query()
		q.Set("outlet_id", d.OutletID)
		req.URL.RawQuery = q.Encode()
	}
	resp, err := d.Next.Do(req)
	if err != nil || resp == nil {
		return resp, err
	}
	d.CorrelationID = resp.Header.Get(CorrelationHeader)
	d.RetryAfter = retryAfter(resp.Header.Get("Retry-After"), time.Now())
	d.Problem = nil
	if (resp.StatusCode >= 200 && resp.StatusCode < 300) || MediaType(resp.Header.Get("Content-Type")) != ProblemMediaType {
		return resp, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	d.Problem = body
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// ResultMember preserves the original JSON of a required response member.
func ResultMember(raw []byte, name, correlation string) (json.RawMessage, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope[name]) == 0 || string(envelope[name]) == "null" {
		return nil, ProtocolError(http.StatusOK, correlation, "response has no "+name+" result")
	}
	return envelope[name], nil
}
