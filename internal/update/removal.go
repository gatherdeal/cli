package update

import "github.com/gatherdeal/cli/internal/releasemeta"

// Removal says how to remove the running CLI binary. Argv is a command for
// the person (or the agent's operator) to run; the CLI never runs it and
// never deletes its own executable.
type Removal struct {
	InstallMethod Method   `json:"install_method"`
	Executable    string   `json:"executable,omitempty"`
	Argv          []string `json:"argv,omitempty"`
	ProjectDir    string   `json:"project_dir,omitempty"`
	Instructions  string   `json:"instructions"`
}

// PlanRemoval detects how the running CLI was installed, offline, and says
// how to remove it. A binary at the documented release-archive location is
// not verified against its release; its instructions name the path only.
func PlanRemoval(h Host) Removal {
	inst := Detect(h)
	r := Removal{InstallMethod: inst.Method, Executable: inst.Path}
	at := func(prefix string) string {
		if inst.Path == "" {
			return prefix + "the gatherdeal executable."
		}
		return prefix + "the executable at " + inst.Path + "."
	}
	switch {
	case inst.Method == MethodNPMGlobal:
		r.Argv = []string{"npm", "uninstall", "-g", releasemeta.NPMPackage}
		r.Instructions = "Installed with npm (global). Run the command in argv to remove it; the CLI never runs npm itself."
	case inst.Method == MethodNPMLocal:
		r.Argv = []string{"npm", "uninstall", releasemeta.NPMPackage}
		r.ProjectDir = inst.ProjectDir
		r.Instructions = "Installed as a dependency of the project in project_dir. Remove the dependency there, for example with the command in argv."
	case inst.Method == MethodNPX:
		r.Instructions = "Started with npx; there is no installed binary to remove."
	case inst.Method == MethodGoInstall:
		r.Instructions = at("Installed with go install. Delete ")
	case inst.Method == MethodDev:
		r.Instructions = at("This is a development build. Delete ")
	case inst.Method == MethodUnwritable:
		r.Instructions = at("The executable is in a directory you cannot write; ask whoever installed it to remove ") + " The CLI never uses sudo or changes permissions."
	case inst.RawCandidate():
		r.Instructions = at("Installed from a release archive at the documented location. Delete ")
	default:
		r.Instructions = at("The CLI cannot tell how it was installed. Remove it with the tool you installed it with, or delete ")
	}
	return r
}
