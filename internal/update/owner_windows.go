//go:build windows

package update

import "golang.org/x/sys/windows"

// ownedByUser reports whether path is owned by the current user.
func ownedByUser(path string) bool {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	owner, _, err := sd.Owner()
	return err == nil && owner != nil && owner.Equals(u.User.Sid)
}

// writableDir is not decided on Windows: a directory the user cannot write
// is classified as unknown, which also gets manual instructions.
func writableDir(string) bool { return true }
