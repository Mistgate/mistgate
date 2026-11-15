package vendorcheck

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

const (
	xrayPath     = "github.com/xtls/xray-core"
	xrayForkPath = "github.com/Mistgate/Xray-core"
)

// Xray-core is built from our public fork (upstream plus one commit; the fork's README says what and how to move it) through a
// `replace`, which holds whatever version `require` names, so raising the require alone would go on building the old fork without
// a word. The fork's base cannot be read without the network, so go.mod's comment above the replace names the upstream commit it
// is based on, and that commit must be the one the required pseudo-version ends with.
func TestXrayForkIsBasedOnRequiredVersion(t *testing.T) {
	data, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	mod, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		t.Fatal(err)
	}

	var required string
	for _, r := range mod.Require {
		if r.Mod.Path == xrayPath {
			required = r.Mod.Version
		}
	}
	if required == "" {
		t.Fatal(xrayPath + " is not required by go.mod")
	}
	rev, err := module.PseudoVersionRev(required)
	if err != nil {
		t.Fatalf("go.mod requires %s %s, which is not a pseudo-version, so it names no upstream commit to compare the fork with: %v", xrayPath, required, err)
	}

	var rep *modfile.Replace
	for _, r := range mod.Replace {
		if r.Old.Path == xrayPath {
			rep = r
		}
	}
	if rep == nil {
		t.Fatalf("go.mod has no replace for %s: Vision's data race fix lives in the fork %s", xrayPath, xrayForkPath)
	}
	if rep.New.Path != xrayForkPath || rep.New.Version == "" {
		t.Fatalf("go.mod replaces %s with %s %s, want a version of %s (a commit of its branch mistgate)", xrayPath, rep.New.Path, rep.New.Version, xrayForkPath)
	}

	var comment []string
	for _, c := range rep.Syntax.Comments.Before {
		comment = append(comment, c.Token)
	}
	if !strings.Contains(strings.Join(comment, "\n"), rev) {
		t.Fatalf("go.mod requires %s %s (upstream commit %s), but the comment above the replace does not name that commit as the base of the fork. "+
			"If you raised the require: rebase the fork onto the new upstream version, point the replace at the new fork commit and name the new upstream commit in the comment "+
			"(see \"Move to a new upstream version\" in the README of https://%s). If the comment is only out of date, fix the comment", xrayPath, required, rev, xrayForkPath)
	}
}
