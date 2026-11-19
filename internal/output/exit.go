// Package output defines the CLI's machine-readable contract: the single
// JSON envelope written to stdout for every command (success or failure),
// the typed CLI error, and the process exit codes.
package output

import "net/http"

// ExitCode is the process exit status. The values are part of the public
// contract; never renumber them.
type ExitCode int

const (
	// ExitOK: the command succeeded.
	ExitOK ExitCode = 0
	// ExitUnclassified: unclassified failure, protocol violation (including
	// an HTTP redirect) or internal CLI failure.
	ExitUnclassified ExitCode = 1
	// ExitUsage: invalid command, flag or input.
	ExitUsage ExitCode = 2
	// ExitAuth: authentication required, expired, idle-expired or revoked.
	ExitAuth ExitCode = 3
	// ExitDenied: permission, scope or entitlement denied.
	ExitDenied ExitCode = 4
	// ExitNotFound: the requested resource does not exist or is out of scope.
	ExitNotFound ExitCode = 5
	// ExitConflict: conflict, failed precondition or other domain error.
	ExitConflict ExitCode = 6
	// ExitRateLimited: the server asked the client to slow down.
	ExitRateLimited ExitCode = 7
	// ExitUnavailable: network, TLS, DNS or timeout failure, or a 5xx.
	ExitUnavailable ExitCode = 8
	// ExitActionRequired: a human must act first (approve a login, accept
	// a server risk, choose a credential store).
	ExitActionRequired ExitCode = 9
	// ExitPartial: the command completed only partly. Reserved: no command
	// produces it.
	ExitPartial ExitCode = 10
	// ExitInterrupted: interrupted by a signal.
	ExitInterrupted ExitCode = 130
)

// ExitCodeInfo documents one exit code for `gatherdeal commands`.
type ExitCodeInfo struct {
	Code    ExitCode `json:"code"`
	Name    string   `json:"name"`
	Meaning string   `json:"meaning"`
}

// ExitCodes lists every exit code in ascending order.
func ExitCodes() []ExitCodeInfo {
	return []ExitCodeInfo{
		{ExitOK, "ok", "Success."},
		{ExitUnclassified, "unclassified", "Unclassified failure, protocol violation (including any HTTP redirect) or internal CLI failure."},
		{ExitUsage, "usage", "Invalid command, flag or input, including CLI_SKILL_PATH_INVALID (error.details.path is the absolute invalid skill path) and CLI_JQ_FAILED (a --jq expression failed on a successful result; data holds the unfiltered result, and the command, including any write, already succeeded)."},
		{ExitAuth, "auth", "Authentication required, expired, unused for too long or revoked. Run `gatherdeal auth login`."},
		{ExitDenied, "denied", "Permission, scope or entitlement denied. Retrying does not help."},
		{ExitNotFound, "not_found", "Not found, or outside the outlets this access may read."},
		{ExitConflict, "conflict", "Conflict, failed precondition or other domain error, including CLI_SKILL_CONFLICT (error.details.paths lists conflicting files relative to error.details.dir)."},
		{ExitRateLimited, "rate_limited", "Rate limited. retryable means the failure condition is transient, never that repeating a command that changes state is safe."},
		{ExitUnavailable, "unavailable", "Network, TLS, DNS or timeout failure, or a server error (5xx). retryable means the failure condition is transient, never that repeating a command that changes state is safe."},
		{ExitActionRequired, "action_required", "A human must act first (approve the login, accept a server risk, choose a credential store)."},
		{ExitPartial, "partial", "Partial completion (reserved)."},
		{ExitInterrupted, "interrupted", "Interrupted by a signal."},
	}
}

// ExitForStatus is the exit code of a server failure with HTTP status
// status: 400 → 2, 401 → 3, 403 → 4, 404 → 5, 409/412/422 and every other
// 4xx → 6, 429 → 7, 5xx → 8. Anything else (a 2xx or 3xx that is not a
// success) is 1. A problem's code overrides it only through the short
// platform table of the HTTP layer (CLIENT_UPGRADE_REQUIRED → 9).
func ExitForStatus(status int) ExitCode {
	switch {
	case status == http.StatusBadRequest:
		return ExitUsage
	case status == http.StatusUnauthorized:
		return ExitAuth
	case status == http.StatusForbidden:
		return ExitDenied
	case status == http.StatusNotFound:
		return ExitNotFound
	case status == http.StatusTooManyRequests:
		return ExitRateLimited
	case status >= 500:
		return ExitUnavailable
	case status > 400 && status < 500:
		// 409, 412, 422 and every other client error: a domain failure.
		return ExitConflict
	}
	return ExitUnclassified
}
