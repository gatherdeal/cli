package httpx

import (
	"bytes"
	"io"
	"net/http"
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
