//go:build windows

package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func ownerOf(t *testing.T, path string) *windows.SID {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

// What the store creates is owned by the user's own SID, even in an
// elevated process whose default owner is the Administrators group.
func TestCreatedFilesOwnedByUser(t *testing.T) {
	user, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	fs := NewFileStore(newRoot(t))
	if err := fs.Set("acct", secret); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Dir(fs.Dir), fs.Dir, filepath.Join(fs.Dir, fs.fileName("acct"))} {
		if owner := ownerOf(t, path); !owner.Equals(user) {
			t.Errorf("%s is owned by %s, want %s", path, owner, user)
		}
	}
}

func TestOwnedByCurrentUser(t *testing.T) {
	user, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	elevated, err := windows.Token(0).IsMember(admins)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		owner *windows.SID
		want  bool
	}{{user, true}, {admins, elevated}, {users, false}} {
		if got, err := ownedByCurrentUser(tc.owner, user); err != nil || got != tc.want {
			t.Errorf("owner %s: got %t %v, want %t", tc.owner, got, err, tc.want)
		}
	}
}

// A location whose DACL also grants another account is refused.
func TestSharedDACLRefused(t *testing.T) {
	user, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	root := newRoot(t)
	saveOne(t, root)
	grant := func(sid *windows.SID, access windows.ACCESS_MASK) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: access,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{grant(user, windows.GENERIC_ALL), grant(users, windows.GENERIC_READ)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkOwnerOnly(root, fi); err == nil {
		t.Fatal("a directory readable by another account was accepted")
	}
	var insecure *InsecureError
	if _, err := LoadProfiles(root); !errors.As(err, &insecure) {
		t.Fatalf("LoadProfiles = %v, want InsecureError", err)
	}
	if err := NewFileStore(root).Preflight(); !errors.As(err, &insecure) {
		t.Fatalf("Preflight = %v, want InsecureError", err)
	}
}
