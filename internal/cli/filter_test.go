package cli

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func TestFilterOperatorsFailBeforeRequests(t *testing.T) {
	f := newFake(t, "")
	te := newTestEnv(t, f)
	for _, tc := range []struct {
		name   string
		args   []string
		filter string
		want   string
	}{
		{
			name:   "contact logs",
			args:   []string{"contact-logs", "list", "--contact-id", "01234567-89ab-4cde-8fab-0123456789ab"},
			filter: `{"rules":[{"field":"create_at","op":"EQ","value":"2026-11-19T00:00:00Z"}]}`,
			want:   `--filter operator "EQ" is invalid for field "create_at". Allowed operators are GTE, LTE.`,
		},
		{
			name:   "nested pipeline rule",
			args:   []string{"sales-pipelines", "list"},
			filter: `{"groups":[{"rules":[{"field":"name","op":"EQ","value":"Retail"}]}]}`,
			want:   `--filter operator "EQ" is invalid for field "name". Allowed operators are EQ_FOLD, CONTAINS_FOLD.`,
		},
		{
			name:   "deal stage",
			args:   []string{"deals", "list"},
			filter: `{"rules":[{"field":"stage_id","op":"NEQ","value":"55555555-5555-4555-8555-555555555555"}]}`,
			want:   `--filter operator "NEQ" is invalid for field "stage_id". Allowed operators are EQ, IN.`,
		},
		{
			name:   "malformed JSON",
			args:   []string{"deals", "list"},
			filter: `{"rules":`,
			want:   "invalid --filter JSON",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"crm"}, tc.args...)
			r := te.run(append(args, "--filter", tc.filter)...)
			if r.exit != 2 || r.errorCode() != "CLI_USAGE" || !strings.Contains(r.stderr, tc.want) || f.requests.Load() != 0 {
				t.Fatalf("stdout %s, stderr %s, requests %d", r.stdout, r.stderr, f.requests.Load())
			}
			if tc.name == "contact logs" {
				golden(t, "contact_logs_filter_operator.golden.json", r.stdout)
			}
		})
	}
}

func TestFilterOperatorsPreserveServerValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter string
	}{
		{"operand", `{"rules":[{"field":"id","op":"EQ","value":"not-a-uuid"}]}`},
		{"field outside the map", `{"rules":[{"field":"unknown","op":"EQ","value":"x"}]}`},
		{"edge field scope", `{"groups":[{"edge":"example","quantifier":"SOME","groups":[{"rules":[{"field":"id","op":"NEQ","value":"x"}]}]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, te := loggedIn(t)
			f.crm = func(w http.ResponseWriter, r *http.Request) {
				raw, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("filter"))
				if err != nil || len(raw) == 0 {
					t.Errorf("missing encoded filter: %s, %v", raw, err)
				}
				writeJSON(w, http.StatusUnprocessableEntity, problemJSON(http.StatusUnprocessableEntity,
					"VALIDATION_FAILED", "Invalid filter.", nil))
			}
			r := te.run("crm", "deals", "list", "--filter", tc.filter)
			if r.exit != 6 || r.errorCode() != "VALIDATION_FAILED" || !strings.Contains(r.stdout, `"http_status":422`) {
				t.Fatal(r.stdout, r.stderr)
			}
		})
	}
}
