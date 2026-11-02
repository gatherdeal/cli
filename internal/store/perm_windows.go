//go:build windows

package store

import (
	"fmt"
	"io/fs"
	"unsafe"

	"golang.org/x/sys/windows"
)

func currentUserSID() (*windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid, nil
}

// ownedByCurrentUser reports whether owner is the current user: the user's
// own SID, or the Administrators group while the process runs elevated as a
// member of it. Windows makes that group the default owner of objects an
// elevated administrator creates; a non-elevated token holds it deny-only,
// so it only counts while the process can act as an administrator anyway.
func ownedByCurrentUser(owner, user *windows.SID) (bool, error) {
	if owner.Equals(user) {
		return true, nil
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, err
	}
	if !owner.Equals(admins) {
		return false, nil
	}
	return windows.Token(0).IsMember(admins)
}

// checkOwnerOnly refuses a path that is not owned by the current user or
// whose DACL grants access to anyone else.
func checkOwnerOnly(path string, _ fs.FileInfo) error {
	sid, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("%s: cannot determine the current user: %w", path, err)
	}
	var sd *windows.SECURITY_DESCRIPTOR
	err = retrySharing(func() (err error) {
		sd, err = windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		return err
	})
	if err != nil {
		return fmt.Errorf("%s: cannot read the security descriptor: %w", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("%s has no owner", path)
	}
	if ok, err := ownedByCurrentUser(owner, sid); err != nil {
		return fmt.Errorf("%s: cannot check the owner: %w", path, err)
	} else if !ok {
		return fmt.Errorf("%s is owned by %s, not the current user (%s)", path, owner.String(), sid.String())
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("%s has no access control list (everyone has access)", path)
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("%s: cannot read the access control list: %w", path, err)
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
		default:
			// Callback and object ACEs can grant access too; only plain
			// entries are understood.
			return fmt.Errorf("%s has an access control entry of type %d; it must be accessible to the current user only", path, ace.Header.AceType)
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart)) //nolint:gosec // documented ACE layout: the SID follows the header
		if !aceSID.Equals(sid) {
			return fmt.Errorf("%s grants access to another account (%s); it must be accessible to the current user only", path, aceSID.String())
		}
	}
	return nil
}

// restrictToOwner replaces the DACL of path with a protected one that grants
// access to the current user only, and makes the current user its owner.
func restrictToOwner(path string, isDir bool) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	inherit := uint32(windows.NO_INHERITANCE)
	if isDir {
		inherit = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inherit,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, nil)
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return err
	}
	// The user owns what the CLI creates even when an elevated process
	// would default the owner to the Administrators group, so elevated and
	// non-elevated runs share the same state. The DACL just set grants the
	// user WRITE_OWNER.
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION, sid, nil, nil, nil)
}

// syncDir is a no-op on Windows; rename is already durable enough there.
func syncDir(string) error { return nil }
