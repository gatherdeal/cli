package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/semver"
	"github.com/gatherdeal/cli/internal/update"
)

var updateFlags = []FlagSpec{
	{Name: "check", Kind: kindBool, Usage: "Only report whether a newer release exists and how to install it. Read-only; changes nothing."},
	{Name: "version", Kind: kindString, Usage: "Exact release to check or show installation instructions for (pre-releases allowed). Default: the latest stable release."},
	{Name: "allow-downgrade", Kind: kindBool, Usage: "Allow --version to name a release older than this CLI."},
}

func updateCommand() *Command {
	return &Command{
		Path:    []string{"update"},
		Summary: "Check for a newer CLI release (--check) or show how to install it. Never runs automatically.",
		Flags:   updateFlags, Output: "json", Network: true, ReadOnly: true, Run: runUpdate,
	}
}

// updateVerifier returns the release verifier and its trust-root source.
func (e *Env) updateVerifier(ctx context.Context) (update.ManifestVerifier, string, error) {
	if e.UpdateVerifier != nil {
		return e.UpdateVerifier(ctx)
	}
	tr, src, err := update.TrustedMaterial(ctx, e.HTTPBase)
	if err != nil {
		return nil, "", err
	}
	return update.NewVerifier(tr), src, nil
}

func (e *Env) updateHost() update.Host {
	if e.UpdateHost != nil {
		return *e.UpdateHost
	}
	return update.DefaultHost(e.Getenv)
}

func runUpdate(ctx context.Context, inv *Invocation) (any, error) {
	requested := strings.TrimSpace(inv.str("version"))
	if requested != "" {
		v, err := semver.Parse(requested)
		if err != nil || v.Build != "" {
			return nil, usageError("--version %q is not a release version such as 1.2.3 or 1.3.0-rc.1", requested)
		}
		requested = v.String()
	}
	check := inv.boolean("check")
	if inv.boolean("allow-downgrade") && requested == "" {
		return nil, usageError("--allow-downgrade needs --version")
	}

	verifier, trust, err := inv.Env.updateVerifier(ctx)
	if err != nil {
		return nil, updateError(ctx, err)
	}
	checker := &update.Checker{
		Host:        inv.Env.updateHost(),
		Releases:    update.NewReleases(inv.Env.HTTPBase),
		Verifier:    verifier,
		TrustedRoot: trust,
	}
	plan, err := checker.Check(ctx, requested)
	if err != nil {
		return nil, updateError(ctx, err)
	}
	if check {
		return plan, nil
	}
	return applyPlan(plan, inv.boolean("allow-downgrade"))
}

// applyPlan reports the plan. This version never replaces itself:
// package-managed, go install, development and raw release installs all
// get the exact command or download to run (exit 9).
func applyPlan(plan *update.Plan, allowDowngrade bool) (any, error) {
	switch {
	case plan.State == update.StateAheadOfLatest:
		return nil, output.Errorf(output.ExitActionRequired, output.CodeUpdateActionRequired,
			"the latest release %s is older than this CLI (%s); nothing was changed", plan.TargetVersion, plan.CurrentVersion).
			WithDetails(plan).WithHint("To install that release anyway, pass --version " + plan.TargetVersion + " --allow-downgrade.")
	case plan.State == update.StateDowngrade && !allowDowngrade:
		return nil, output.Errorf(output.ExitActionRequired, output.CodeUpdateActionRequired,
			"%s is older than this CLI (%s); nothing was changed", plan.TargetVersion, plan.CurrentVersion).
			WithDetails(plan).WithHint("Pass --allow-downgrade to install an older release.")
	case plan.Action.Kind == update.ActionNone:
		return plan, nil
	}
	return nil, output.Errorf(output.ExitActionRequired, output.CodeUpdateActionRequired,
		"gatherdeal %s is available; it must be installed outside this command (%s)", plan.TargetVersion, plan.InstallMethod).
		WithDetails(plan).WithHint(plan.Action.Instructions)
}

// updateError maps a check failure to the CLI error contract.
func updateError(ctx context.Context, err error) error {
	var netErr *update.NetworkError
	switch {
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled):
		return output.Errorf(output.ExitInterrupted, output.CodeInterrupted, "interrupted").WithCause(err)
	case errors.Is(err, update.ErrVerify):
		return output.Errorf(output.ExitUnclassified, output.CodeUpdateVerifyFailed,
			"the release metadata failed verification; nothing was changed: %v", err).WithCause(err).
			WithHint("Do not install this release. Try again later; if it persists, report it at https://github.com/gatherdeal/cli/issues.")
	case errors.Is(err, update.ErrReleaseNotFound):
		return output.Errorf(output.ExitNotFound, output.CodeUpdateVersionNotFound, "no such release").WithCause(err).
			WithHint("See " + update.ReleasesURL + " for the published versions.")
	case errors.Is(err, update.ErrLatestIsPrerelease):
		return output.Errorf(output.ExitActionRequired, output.CodeUpdateActionRequired,
			"the latest release is a pre-release, which is never selected implicitly").WithCause(err).
			WithHint("Pass --version with the exact release you want.")
	case errors.As(err, &netErr):
		e := output.Errorf(output.ExitUnavailable, output.CodeNetwork, "could not fetch the release metadata from GitHub").WithCause(err)
		e.Retryable = true
		return e
	}
	return output.AsError(err)
}
