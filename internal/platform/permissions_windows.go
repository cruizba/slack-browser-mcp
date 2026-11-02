package platform

import "os"

// Harden cannot do anything here, and pretending otherwise would be worse than
// saying so: Windows has no mode bits, and Go's OpenFile translates the 0600
// argument into "not read-only", which is not what the caller meant.
//
// The file is still private, by inheritance rather than by this call: the store
// lives under the user's own profile (%APPDATA%, %LOCALAPPDATA%), whose
// directory ACL admits that user, administrators and SYSTEM and nobody else. A
// file created there inherits that ACL.
func Harden(string) error {
	return nil
}

// HardenDir cannot do anything here either; see [Harden].
func HardenDir(string) error {
	return nil
}

// Private reports yes for the same reason: the DACL inherited from the profile
// cannot be read with os.Stat, and refusing every file because the mode bits are
// meaningless would make the store unusable on the one system this exists to
// support. The mode it reports is whatever Windows synthesised, for messages
// only.
func Private(path string) (bool, os.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, 0, err
	}

	return true, info.Mode().Perm(), nil
}

// CurrentUID and OwnerUID report false: Windows has no uid to compare, only a
// DACL, which os.FileInfo does not carry.
func CurrentUID() (int, bool) {
	return 0, false
}

func OwnerUID(os.FileInfo) (int, bool) {
	return 0, false
}

// CheckOwner accepts every file for the reason [Private] explains: the ACL
// inherited from the profile is what keeps other users out, and the uid this
// check would compare does not exist.
func CheckOwner(string, os.FileInfo) error {
	return nil
}
