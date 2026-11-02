// Package buildinfo carries what a release build stamps into the binary, so
// `version` needs neither a checkout nor git at run time. The values arrive as
//
//	-ldflags "-X .../internal/buildinfo.Version=0.8.0 \
//	          -X .../internal/buildinfo.Describe=v0.8.0-3-gabc1234 \
//	          -X .../internal/buildinfo.Dirty=1"
package buildinfo

// The three values a release build stamps in; see the package comment.
var (
	Version  = "0.0.0"
	Describe = ""
	Dirty    = ""
)

// Line is what `slack-browser version` prints after the program name. An empty
// Describe means the build was not made where git could answer, which is the one
// case that prints the version alone.
func Line() string {
	switch {
	case Describe == "":
		return Version
	case Dirty == "1":
		return Version + " (" + Describe + ", local changes)"
	default:
		return Version + " (" + Describe + ")"
	}
}
