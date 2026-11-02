package update

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gatherdeal/cli/internal/buildinfo"
)

func TestPlanRemoval(t *testing.T) {
	const pkg = "/node_modules/@gatherdeal/cli/node_modules/@gatherdeal/cli-linux-x64/bin/gatherdeal"
	goInstall := testHost("0.1.0", "/home/u/go/bin/gatherdeal")
	goInstall.Source = buildinfo.SourceModule
	dev := testHost("dev", "/src/cli/gatherdeal")
	dev.Source = buildinfo.SourceDev
	unwritable := testHost("0.1.0", "/usr/local/bin/gatherdeal")
	unwritable.Writable = func(string) bool { return false }
	cases := []struct {
		name    string
		host    Host
		method  Method
		argv    []string
		project string
		text    string
	}{
		{"npm global", withLauncher(testHost("0.1.0", "/usr/local/lib"+pkg)), MethodNPMGlobal, []string{"npm", "uninstall", "-g", "@gatherdeal/cli"}, "", "never runs npm"},
		{"npm local", withLauncher(testHost("0.1.0", "/work/shop"+pkg)), MethodNPMLocal, []string{"npm", "uninstall", "@gatherdeal/cli"}, filepath.FromSlash("/work/shop"), "project_dir"},
		{"npx", withLauncher(testHost("0.1.0", "/home/u/.npm/_npx/1a2b"+pkg)), MethodNPX, nil, "", "no installed binary"},
		{"go install", goInstall, MethodGoInstall, nil, "", "Delete the executable at /home/u/go/bin/gatherdeal."},
		{"dev", dev, MethodDev, nil, "", "Delete the executable at /src/cli/gatherdeal."},
		{"release archive", testHost("0.1.0", "/home/u/.local/bin/gatherdeal"), MethodUnknown, nil, "", "release archive at the documented location. Delete the executable at /home/u/.local/bin/gatherdeal."},
		{"unwritable", unwritable, MethodUnwritable, nil, "", "never uses sudo"},
		{"unknown", testHost("0.1.0", "/opt/homebrew/bin/gatherdeal"), MethodUnknown, nil, "", "cannot tell how it was installed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := PlanRemoval(c.host)
			if r.InstallMethod != c.method || !reflect.DeepEqual(r.Argv, c.argv) || r.ProjectDir != c.project || !strings.Contains(r.Instructions, c.text) {
				t.Fatalf("got %+v", r)
			}
		})
	}
}
