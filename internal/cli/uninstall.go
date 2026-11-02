package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/gatherdeal/cli/internal/output"
	"github.com/gatherdeal/cli/internal/server"
	"github.com/gatherdeal/cli/internal/store"
	"github.com/gatherdeal/cli/internal/update"
)

func uninstallCommand() *Command {
	return &Command{
		Path:    []string{"uninstall"},
		Summary: "Revoke every profile's access, then delete the stored credentials, the installed agent skills and the config directory; without --yes it only shows the plan.",
		Flags: []FlagSpec{
			{Name: "dry-run", Kind: kindBool, Usage: "Show the plan and change nothing (exit 0)."},
			{Name: "yes", Kind: kindBool, Usage: "Carry out the plan. Without it the plan is shown and nothing changes (exit 9)."},
			{Name: "local-only", Kind: kindBool, Usage: "Do not contact any server: delete the local state, and leave every access valid on its server until it expires or is revoked at passport › Security › Sessions & devices."},
		},
		Output: "json", Network: true, ReadOnly: false, Run: runUninstall,
	}
}

// uninstallStatus is the state of one uninstall step. A plan holds planned,
// skipped, absent and no_credential steps; a run replaces planned with the
// outcome.
type uninstallStatus string

const (
	uninstallPlanned uninstallStatus = "planned"
	// uninstallRevoked: the server revoked the access.
	uninstallRevoked uninstallStatus = "revoked"
	// uninstallAlreadyInactive: the revoke failed, but the server reports
	// the access already revoked, expired or unused for too long.
	uninstallAlreadyInactive uninstallStatus = "already_inactive"
	// uninstallNoCredential: the profile holds no credential to revoke.
	uninstallNoCredential uninstallStatus = "no_credential"
	uninstallDeleted      uninstallStatus = "deleted"
	// uninstallKept: the config directory stays because it holds entries
	// the CLI did not create (foreign_entries); the CLI's own files in it
	// were deleted.
	uninstallKept    uninstallStatus = "kept"
	uninstallSkipped uninstallStatus = "skipped"
	uninstallAbsent  uninstallStatus = "absent"
	uninstallFailed  uninstallStatus = "failed"
	// uninstallNotAttempted: an earlier step failed, so this one did not run.
	uninstallNotAttempted uninstallStatus = "not_attempted"
)

type uninstallStep struct {
	Status uninstallStatus `json:"status"`
	Reason string          `json:"reason,omitempty"`
}

// uninstallCredential is where a profile's credential is stored.
type uninstallCredential struct {
	Store          store.Kind `json:"store"`
	KeyringService string     `json:"keyring_service,omitempty"`
	KeyringAccount string     `json:"keyring_account,omitempty"`
	File           string     `json:"file,omitempty"`
}

type uninstallProfile struct {
	Profile           string               `json:"profile"`
	Server            string               `json:"server"`
	Binding           *store.Binding       `json:"binding,omitempty"`
	DelegatedAccessID string               `json:"delegated_access_id,omitempty"`
	Credential        *uninstallCredential `json:"credential,omitempty"`
	// Revoke is the server step; Delete removes the profile, its
	// credential and any unfinished login.
	Revoke uninstallStep `json:"revoke"`
	Delete uninstallStep `json:"delete"`
}

type uninstallSkill struct {
	Path   string `json:"path"`
	Target string `json:"target,omitempty"`
	Scope  string `json:"scope,omitempty"`
	// Recorded: skills install recorded this location. Unrecorded ones are
	// the default user-scope locations.
	Recorded bool `json:"recorded"`
	uninstallStep
	files []string
}

type uninstallConfigDir struct {
	Path string `json:"path"`
	uninstallStep
	ForeignEntries []string `json:"foreign_entries,omitempty"`
}

// validAccess is an access that stays valid on its server.
type validAccess struct {
	Profile           string `json:"profile"`
	Server            string `json:"server"`
	DelegatedAccessID string `json:"delegated_access_id,omitempty"`
}

// uninstallData is the plan and, after --yes, the result of `uninstall`.
type uninstallData struct {
	LocalOnly bool               `json:"local_only"`
	Profiles  []uninstallProfile `json:"profiles"`
	Skills    []uninstallSkill   `json:"skills"`
	ConfigDir uninstallConfigDir `json:"config_dir"`
	// AccessesStillValid lists the accesses that stay (or would stay)
	// valid on their servers.
	AccessesStillValid []validAccess  `json:"accesses_still_valid"`
	Binary             update.Removal `json:"binary"`
	Notes              []string       `json:"notes,omitempty"`
}

const revokeElsewhere = "revoke them at passport › Security › Sessions & devices"

func runUninstall(ctx context.Context, inv *Invocation) (any, error) {
	dryRun, localOnly := inv.boolean("dry-run"), inv.boolean("local-only")
	inv.Meta.Write = &output.WriteMeta{DryRun: dryRun}
	root, err := store.ConfigRoot(inv.Env.Getenv)
	if err != nil {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeInternal, "%v", err)
	}
	t := &target{root: root}
	profiles, err := store.LoadProfiles(root)
	if err != nil {
		return nil, storeError(err, t)
	}
	installs, err := store.LoadSkillInstalls(root)
	if err != nil {
		return nil, storeError(err, t)
	}
	contents, err := store.InspectRoot(root)
	if err != nil {
		return nil, storeError(&store.InsecureError{Err: err}, t)
	}
	data := inv.planUninstall(root, profiles, installs.List(), contents, localOnly)
	if dryRun {
		inv.describeUninstall(data, false)
		return data, nil
	}
	if !inv.boolean("yes") {
		inv.describeUninstall(data, false)
		cmd := "gatherdeal uninstall --yes"
		if localOnly {
			cmd += " --local-only"
		}
		return data, output.Errorf(output.ExitActionRequired, output.CodeWriteConfirmationRequired, "uninstall requires --yes; nothing was changed").
			WithHint("Review the plan in data. Only if the person asked to remove the CLI, run `" + cmd + "`.")
	}
	err = inv.executeUninstall(ctx, root, profiles, data)
	inv.describeUninstall(data, true)
	if err != nil {
		return data, err
	}
	return data, nil
}

// planUninstall lists every step; it changes nothing.
func (inv *Invocation) planUninstall(root string, profiles *store.Profiles, recorded []store.SkillInstall, contents store.RootContents, localOnly bool) *uninstallData {
	data := &uninstallData{
		LocalOnly: localOnly, Profiles: []uninstallProfile{}, Skills: []uninstallSkill{},
		AccessesStillValid: []validAccess{}, Binary: update.PlanRemoval(inv.Env.updateHost()),
	}
	for _, name := range profiles.Names() {
		prof := profiles.Get(name)
		e := uninstallProfile{Profile: name, Server: prof.Server, Binding: prof.Binding,
			Revoke: uninstallStep{Status: uninstallPlanned}, Delete: uninstallStep{Status: uninstallPlanned}}
		if prof.Access != nil {
			e.DelegatedAccessID = prof.Access.ID
		}
		if ref := prof.Credential; ref != nil {
			e.Credential = &uninstallCredential{Store: ref.Store}
			if slot, err := profiles.Slot(prof.Issuer, name); err == nil {
				account := slot.CredentialAccount(ref.Generation)
				if ref.Store == store.KindFile {
					e.Credential.File = store.NewFileStore(root).Path(account)
				} else {
					e.Credential.KeyringService, e.Credential.KeyringAccount = store.KeyringService, account
				}
			}
		}
		switch {
		case e.Credential == nil:
			e.Revoke.Status = uninstallNoCredential
		case localOnly:
			e.Revoke = uninstallStep{Status: uninstallSkipped, Reason: "--local-only: the access stays valid on the server"}
			data.AccessesStillValid = append(data.AccessesStillValid, e.validAccess())
		}
		data.Profiles = append(data.Profiles, e)
	}
	data.Skills = skillCandidates(recorded)
	data.ConfigDir = uninstallConfigDir{Path: root, uninstallStep: uninstallStep{Status: uninstallAbsent}}
	if contents.Exists {
		data.ConfigDir.Status = uninstallPlanned
		data.ConfigDir.ForeignEntries = contents.Foreign
		if len(contents.Foreign) > 0 {
			data.ConfigDir.Reason = "it holds entries the CLI did not create (foreign_entries); only the CLI's own files are deleted and the directory stays"
		}
	}
	if len(data.AccessesStillValid) > 0 {
		data.Notes = append(data.Notes, "The accesses in accesses_still_valid stay valid on their servers until they expire; "+revokeElsewhere+".")
	}
	if inv.Env.Getenv(EnvToken) != "" {
		data.Notes = append(data.Notes, EnvToken+" is set; uninstall does not revoke it. Revoke that access at passport › Security › Sessions & devices and unset the variable.")
	}
	data.Notes = append(data.Notes, "This command never deletes its own executable; binary.instructions says how to remove it.")
	return data
}

func (e *uninstallProfile) validAccess() validAccess {
	return validAccess{Profile: e.Profile, Server: e.Server, DelegatedAccessID: e.DelegatedAccessID}
}

// executeUninstall runs the plan in order: every profile (revoke, then
// delete), every skill directory, then the config directory. It stops at
// the first failure; every later step is reported as not attempted.
func (inv *Invocation) executeUninstall(ctx context.Context, root string, profiles *store.Profiles, data *uninstallData) error {
	var failure error
	for i := range data.Profiles {
		e := &data.Profiles[i]
		if failure != nil {
			e.Revoke, e.Delete = notAttempted(e.Revoke), notAttempted(e.Delete)
			continue
		}
		failure = inv.uninstallProfile(ctx, root, profiles, e, data.LocalOnly)
	}
	for i := range data.Skills {
		s := &data.Skills[i]
		if failure != nil {
			s.uninstallStep = notAttempted(s.uninstallStep)
			continue
		}
		if s.Status != uninstallPlanned {
			continue
		}
		failure = removeSkillDir(s)
	}
	if failure != nil {
		data.ConfigDir.uninstallStep = notAttempted(data.ConfigDir.uninstallStep)
	} else if data.ConfigDir.Status == uninstallPlanned {
		failure = removeConfigDir(&data.ConfigDir)
	}
	data.AccessesStillValid = []validAccess{}
	for i := range data.Profiles {
		e := &data.Profiles[i]
		switch e.Revoke.Status {
		case uninstallSkipped, uninstallFailed, uninstallNotAttempted:
			if e.Credential != nil {
				data.AccessesStillValid = append(data.AccessesStillValid, e.validAccess())
			}
		}
	}
	return failure
}

// notAttempted marks a planned step that did not run; skipped, absent and
// no_credential steps keep their status.
func notAttempted(s uninstallStep) uninstallStep {
	if s.Status == uninstallPlanned {
		return uninstallStep{Status: uninstallNotAttempted}
	}
	return s
}

// uninstallProfile revokes and deletes one profile, failing closed: when
// the revoke fails, the profile and its credential stay.
func (inv *Invocation) uninstallProfile(ctx context.Context, root string, profiles *store.Profiles, e *uninstallProfile, localOnly bool) error {
	t, err := storedTarget(root, profiles, e.Profile, localOnly)
	if err != nil {
		e.Revoke = uninstallStep{Status: uninstallFailed, Reason: output.AsError(err).Message}
		e.Delete = uninstallStep{Status: uninstallNotAttempted}
		return err
	}
	res, err := inv.endAccess(ctx, t, endAccessOptions{localOnly: localOnly, failClosed: true})
	switch {
	case localOnly:
		// The plan already says skipped.
	case res.alreadyInactive:
		e.Revoke = uninstallStep{Status: uninstallAlreadyInactive}
	case res.revoked:
		e.Revoke = uninstallStep{Status: uninstallRevoked}
	case errors.Is(err, errKeepProfile):
		e.Revoke = uninstallStep{Status: uninstallFailed, Reason: output.AsError(res.revokeErr).Message}
	case err != nil && e.Credential != nil:
		e.Revoke = uninstallStep{Status: uninstallFailed, Reason: output.AsError(err).Message}
	default:
		e.Revoke = uninstallStep{Status: uninstallNoCredential, Reason: res.note}
	}
	switch {
	case errors.Is(err, errKeepProfile):
		e.Delete = uninstallStep{Status: uninstallNotAttempted}
		return revokeKeptError(e, res.revokeErr)
	case err != nil:
		e.Delete = uninstallStep{Status: uninstallFailed, Reason: output.AsError(err).Message}
		return err
	case res.deleteErr != nil:
		e.Delete = uninstallStep{Status: uninstallFailed, Reason: "the profile was removed but its credential could not be deleted: " + res.deleteErr.Error()}
		return output.Errorf(output.ExitUnclassified, output.CodeUninstallIncomplete,
			"the credential of profile %q could not be deleted; nothing after it was changed", e.Profile).
			WithHint("Delete the credential named in data.profiles[].credential yourself, then run `gatherdeal uninstall --yes` again.")
	}
	e.Delete = uninstallStep{Status: uninstallDeleted}
	inv.Env.stderrf("Removed profile %q (%s).\n", e.Profile, e.Server)
	return nil
}

// revokeKeptError reports a failed revoke that stopped the uninstall.
func revokeKeptError(e *uninstallProfile, revokeErr error) error {
	cause := output.AsError(revokeErr)
	out := &output.Error{
		Code: output.CodeRevokeFailed,
		Message: fmt.Sprintf("the access of profile %q could not be revoked on %s; it is still valid, and it and everything after it were kept",
			e.Profile, e.Server),
		HTTPStatus: cause.HTTPStatus, CorrelationID: cause.CorrelationID, Retryable: cause.Retryable, Exit: cause.Exit,
		Details: revokeKeptDetails{
			Profile: e.Profile, Server: e.Server, DelegatedAccessID: e.DelegatedAccessID,
			Cause: map[string]any{"code": cause.Code, "message": cause.Message},
		},
		Hint: "Run `gatherdeal uninstall --yes` again once the server is reachable. To remove the CLI anyway, revoke the access at passport › Security › Sessions & devices, then run `gatherdeal uninstall --yes --local-only`.",
	}
	if out.Exit == output.ExitOK || out.Exit == output.ExitAuth {
		out.Exit = output.ExitUnclassified
	}
	return out
}

type revokeKeptDetails struct {
	Profile           string `json:"profile"`
	Server            string `json:"server"`
	DelegatedAccessID string `json:"delegated_access_id,omitempty"`
	Cause             any    `json:"cause"`
}

// storedTarget is the target of a stored profile, as resolveTarget would
// build it for `--profile name`. localOnly accepts invalid stored server
// settings: nothing is sent to the server.
func storedTarget(root string, profiles *store.Profiles, name string, localOnly bool) (*target, error) {
	prof := profiles.Get(name)
	if prof == nil {
		return nil, output.Errorf(output.ExitUnclassified, output.CodeInternal, "profile %q is missing", name)
	}
	t := &target{name: name, origin: prof.Server, root: root, profiles: profiles, profile: prof}
	t.production = t.origin == server.ProductionOrigin
	t.serverFlag = !t.production
	switch {
	case t.production:
		t.disc = server.Production()
	case prof.Discovery != nil && prof.Discovery.Validate(t.origin, false) == nil:
		t.disc = *prof.Discovery
	case !localOnly:
		return nil, output.Errorf(output.ExitUnclassified, output.CodeDiscoveryInvalid,
			"the stored server settings of profile %q are invalid, so its access cannot be revoked", name).
			WithHint("Revoke the access at passport › Security › Sessions & devices, then run `gatherdeal uninstall --yes --local-only`.")
	}
	if t.disc.Issuer != prof.Issuer {
		if !localOnly {
			return nil, output.Errorf(output.ExitUsage, output.CodeProfileMismatch, "profile %q belongs to issuer %s, not %s", name, prof.Issuer, t.disc.Issuer)
		}
		t.disc.Issuer = prof.Issuer
	}
	return t, nil
}

// skillCandidates lists the recorded skill locations, then the default
// user-scope locations that exist and were not recorded.
func skillCandidates(recorded []store.SkillInstall) []uninstallSkill {
	out := []uninstallSkill{}
	seen := func(path string) bool {
		for _, s := range out {
			if samePath(s.Path, path) {
				return true
			}
		}
		return false
	}
	for _, in := range recorded {
		if seen(in.Path) {
			continue
		}
		s := uninstallSkill{Path: in.Path, Target: in.Target, Scope: in.Scope, Recorded: true}
		s.files, s.uninstallStep = inspectSkillDir(in.Path)
		out = append(out, s)
	}
	home, err := os.UserHomeDir()
	if err == nil {
		home, err = filepath.EvalSymlinks(home)
	}
	if err != nil {
		return out
	}
	for _, name := range skillTargetNames() {
		path := filepath.Join(home, skillTargets[name], "gatherdeal")
		if seen(path) {
			continue
		}
		s := uninstallSkill{Path: path, Target: name, Scope: "user"}
		if s.files, s.uninstallStep = inspectSkillDir(path); s.Status != uninstallAbsent {
			out = append(out, s)
		}
	}
	return out
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// maxSkillFileBytes bounds what inspectSkillDir reads of one file.
const maxSkillFileBytes = 1 << 20

var (
	skillReferenceRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\.md$`)
	skillCLIVersionRE = regexp.MustCompile(`(?m)^  cli_version: "[^"\n]+"$`)
	skillSchemaRE     = regexp.MustCompile(`(?m)^  skill_schema: [0-9]+$`)
)

// inspectSkillDir checks that dir holds only a skill this CLI generated
// (see skillFiles): SKILL.md with the generator's frontmatter (name
// gatherdeal, metadata cli_version and skill_schema) and references/<app>.md
// files that start with the generator's heading, all regular files, no
// symlinks anywhere, and no other entries. It returns the paths to delete
// (files, then directories, dir last) and a planned step, or why dir is
// skipped (or absent).
func inspectSkillDir(dir string) ([]string, uninstallStep) {
	skip := func(format string, args ...any) ([]string, uninstallStep) {
		return nil, uninstallStep{Status: uninstallSkipped, Reason: fmt.Sprintf(format, args...)}
	}
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, uninstallStep{Status: uninstallAbsent}
	}
	switch {
	case err != nil:
		return skip("cannot inspect it: %v", err)
	case fi.Mode()&fs.ModeSymlink != 0:
		return skip("it is a symbolic link; links are never followed")
	case !fi.IsDir():
		return skip("it is not a directory")
	}
	if resolved, err := filepath.EvalSymlinks(dir); err != nil || !samePath(resolved, dir) {
		return skip("its path goes through a symbolic link; links are never followed")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return skip("cannot list it: %v", err)
	}
	var files, dirs []string
	hasSkill := false
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		switch {
		case e.Name() == "SKILL.md" && e.Type().IsRegular():
			if content, ok := readSkillFile(path); !ok || !isGeneratedSkill(content) {
				return skip("SKILL.md was not generated by the gatherdeal CLI")
			}
			hasSkill = true
			files = append(files, path)
		case e.Name() == "references" && e.IsDir():
			refs, err := os.ReadDir(path)
			if err != nil {
				return skip("cannot list references: %v", err)
			}
			for _, r := range refs {
				rp := filepath.Join(path, r.Name())
				if !r.Type().IsRegular() || !skillReferenceRE.MatchString(r.Name()) {
					return skip("it holds references/%s, which the CLI does not create", r.Name())
				}
				if content, ok := readSkillFile(rp); !ok || !strings.HasPrefix(content, "# gatherdeal ") {
					return skip("references/%s was not generated by the gatherdeal CLI", r.Name())
				}
				files = append(files, rp)
			}
			dirs = append(dirs, path)
		default:
			return skip("it holds %s, which the CLI does not create", e.Name())
		}
	}
	if !hasSkill {
		return skip("it has no SKILL.md")
	}
	return append(append(files, dirs...), dir), uninstallStep{Status: uninstallPlanned}
}

func readSkillFile(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSkillFileBytes+1))
	if err != nil || len(b) > maxSkillFileBytes {
		return "", false
	}
	return string(b), true
}

// isGeneratedSkill matches the frontmatter skillMarkdownFor writes.
func isGeneratedSkill(content string) bool {
	const head = "---\nname: gatherdeal\n"
	if !strings.HasPrefix(content, head) {
		return false
	}
	end := strings.Index(content[3:], "\n---\n")
	if end < 0 {
		return false
	}
	front := content[3 : 3+end+1]
	return strings.Contains(front, "\nmetadata:\n") && skillCLIVersionRE.MatchString(front) && skillSchemaRE.MatchString(front)
}

// removeSkillDir checks the directory again and deletes exactly the files
// it found, never recursively, so an entry added since is not deleted.
func removeSkillDir(s *uninstallSkill) error {
	files, step := inspectSkillDir(s.Path)
	if step.Status != uninstallPlanned {
		if step.Status == uninstallAbsent {
			step.Reason = "it disappeared while uninstall ran"
		}
		s.uninstallStep = step
		return nil
	}
	for _, path := range files {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.uninstallStep = uninstallStep{Status: uninstallFailed, Reason: err.Error()}
			return output.Errorf(output.ExitUnclassified, output.CodeUninstallIncomplete,
				"the skill directory %s could not be deleted; nothing after it was changed", s.Path).WithCause(err).
				WithHint("Fix the cause, then run `gatherdeal uninstall --yes` again.")
		}
	}
	s.uninstallStep = uninstallStep{Status: uninstallDeleted}
	return nil
}

func removeConfigDir(d *uninstallConfigDir) error {
	foreign, err := store.RemoveRoot(d.Path)
	d.ForeignEntries = foreign
	switch {
	case errors.Is(err, store.ErrProfilesRemain):
		d.uninstallStep = uninstallStep{Status: uninstallFailed, Reason: "a profile was added while uninstall ran"}
		return output.Errorf(output.ExitConflict, output.CodeUninstallIncomplete, "a profile was added while uninstall ran; the config directory was kept").
			WithHint("Run `gatherdeal uninstall` again to see the new plan.")
	case err != nil:
		d.uninstallStep = uninstallStep{Status: uninstallFailed, Reason: err.Error()}
		return output.Errorf(output.ExitUnclassified, output.CodeUninstallIncomplete, "the config directory %s could not be deleted", d.Path).WithCause(err).
			WithHint("Fix the cause, then run `gatherdeal uninstall --yes` again.")
	case len(foreign) > 0:
		d.uninstallStep = uninstallStep{Status: uninstallKept, Reason: "it holds entries the CLI did not create (foreign_entries); the CLI's own files were deleted"}
	default:
		d.uninstallStep = uninstallStep{Status: uninstallDeleted}
	}
	return nil
}

// describeUninstall prints the plan or the result for a person on stderr.
func (inv *Invocation) describeUninstall(d *uninstallData, executed bool) {
	if executed {
		inv.Env.stderrf("Uninstall result:\n")
	} else {
		inv.Env.stderrf("Uninstall plan (nothing was changed):\n")
	}
	for _, p := range d.Profiles {
		inv.Env.stderrf("  profile %q on %s: revoke %s, delete %s\n", p.Profile, p.Server, p.Revoke.Status, p.Delete.Status)
	}
	for _, s := range d.Skills {
		inv.Env.stderrf("  skill %s: %s%s\n", s.Path, s.Status, reasonSuffix(s.Reason))
	}
	inv.Env.stderrf("  config directory %s: %s%s\n", d.ConfigDir.Path, d.ConfigDir.Status, reasonSuffix(d.ConfigDir.Reason))
	for _, a := range d.AccessesStillValid {
		inv.Env.stderrf("Still valid on the server: profile %q on %s.\n", a.Profile, a.Server)
	}
	if len(d.AccessesStillValid) > 0 {
		inv.Env.stderrf("Revoke those accesses at passport › Security › Sessions & devices.\n")
	}
	inv.Env.stderrf("Then remove the binary: %s\n", d.Binary.Instructions)
	if len(d.Binary.Argv) > 0 {
		inv.Env.stderrf("    %s\n", strings.Join(d.Binary.Argv, " "))
	}
}

func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}
