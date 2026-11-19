package output

// Error codes produced by the CLI itself. Server error codes are passed
// through unchanged from the problem document; the codes the published
// contract declares are constants of the generated packages.
const (
	CodeInternal                = "CLI_INTERNAL_ERROR"
	CodeUsage                   = "CLI_USAGE"
	CodeProtocol                = "CLI_PROTOCOL_ERROR"
	CodeRedirect                = "CLI_REDIRECT_REFUSED"
	CodeOriginMismatch          = "CLI_ORIGIN_MISMATCH"
	CodeDiscoveryInvalid        = "CLI_DISCOVERY_INVALID"
	CodeUpgradeRequired         = "CLI_UPGRADE_REQUIRED"
	CodeNetwork                 = "CLI_NETWORK_ERROR"
	CodeUpstream                = "CLI_UPSTREAM_ERROR"
	CodeNotLoggedIn             = "CLI_NOT_LOGGED_IN"
	CodeTokenInvalid            = "CLI_TOKEN_INVALID"
	CodeTokenWithServer         = "CLI_TOKEN_WITH_CUSTOM_SERVER"
	CodeServerRiskNotAccepted   = "CLI_SERVER_RISK_NOT_ACCEPTED"
	CodeServerRiskDeclined      = "CLI_SERVER_RISK_DECLINED"
	CodeCredentialStore         = "CLI_CREDENTIAL_STORE_UNAVAILABLE"
	CodeCredentialStoreInsecure = "CLI_CREDENTIAL_STORE_INSECURE"
	CodeLoginPending            = "CLI_LOGIN_PENDING"
	CodeLoginDenied             = "CLI_LOGIN_DENIED"
	CodeLoginExpired            = "CLI_LOGIN_EXPIRED"
	CodeLoginRestart            = "CLI_LOGIN_RESTART_REQUIRED"
	CodeLoginRejected           = "CLI_LOGIN_REJECTED"
	CodeRateLimited             = "CLI_RATE_LIMITED"
	CodeInterrupted             = "CLI_INTERRUPTED"
	CodeProfileMismatch         = "CLI_PROFILE_SERVER_MISMATCH"
	CodeRevokeFailed            = "CLI_REVOKE_FAILED"
	CodeUnsupportedRestriction  = "CLI_UNSUPPORTED_RESTRICTION_VERSION"
	CodeServiceMissing          = "CLI_SERVICE_NOT_ADVERTISED"
)

// Codes of `gatherdeal update`.
const (
	CodeUpdateActionRequired  = "CLI_UPDATE_ACTION_REQUIRED"
	CodeUpdateVersionNotFound = "CLI_UPDATE_VERSION_NOT_FOUND"
	CodeUpdateVerifyFailed    = "CLI_UPDATE_VERIFY_FAILED"
)

// CodeUninstallIncomplete: `gatherdeal uninstall` stopped after a local
// step failed; data lists what was done and what was not attempted.
const CodeUninstallIncomplete = "CLI_UNINSTALL_INCOMPLETE"

// Codes of `gatherdeal skills install`.
const (
	CodeSkillConflict    = "CLI_SKILL_CONFLICT"
	CodeSkillPathInvalid = "CLI_SKILL_PATH_INVALID"
)

const (
	CodeProfileSchema               = "CLI_PROFILE_SCHEMA"
	CodeProfileOrganizationMismatch = "CLI_PROFILE_ORGANIZATION_MISMATCH"
	CodeProfilePrincipalMismatch    = "CLI_PROFILE_PRINCIPAL_MISMATCH"
)

const (
	CodeWriteOutcomeUnknown       = "CLI_WRITE_OUTCOME_UNKNOWN"
	CodeWriteConfirmationRequired = "CLI_WRITE_CONFIRMATION_REQUIRED"
)

// Codes of WhatsApp agent pairing.
const (
	CodePairingContextMissing = "CLI_PAIRING_CONTEXT_MISSING"
	CodePairingPending        = "CLI_PAIRING_PENDING"
	CodePairingEnded          = "CLI_PAIRING_ENDED"
)

const CodeOutletNotFound = "CLI_OUTLET_NOT_FOUND"

// CodeJQFailed reports a --jq expression that failed on a successful
// result; the error envelope's data holds the unfiltered result.
const CodeJQFailed = "CLI_JQ_FAILED"
