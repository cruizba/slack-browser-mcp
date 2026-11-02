// Package update moves the checkout a build runs from to a release: it compares
// semver tags, says what it would do, and only then touches anything.
package update

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/cruizba/slack-browser-mcp/internal/failure"
)

var tagPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// Version is a release tag, split into the parts precedence compares.
type Version struct {
	Major      int
	Minor      int
	Patch      int
	Prerelease []string
}

// ParseVersion reads a tag, or answers nothing when it is not semver.
func ParseVersion(tag string) (Version, bool) {
	match := tagPattern.FindStringSubmatch(strings.TrimSpace(tag))
	if match == nil {
		return Version{}, false
	}

	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch, _ := strconv.Atoi(match[3])

	version := Version{Major: major, Minor: minor, Patch: patch}

	if match[4] != "" {
		version.Prerelease = strings.Split(match[4], ".")
	}

	return version, true
}

func compareIdentifiers(left, right string) int {
	leftNumber, leftErr := strconv.Atoi(left)
	rightNumber, rightErr := strconv.Atoi(right)

	switch {
	case leftErr == nil && rightErr == nil:
		return leftNumber - rightNumber
	case leftErr == nil:
		return -1
	case rightErr == nil:
		return 1
	default:
		return strings.Compare(left, right)
	}
}

// CompareVersions is semver precedence: 1.9.0 < 1.10.0, 2.0.0-rc.1 < 2.0.0.
func CompareVersions(left, right Version) int {
	for _, pair := range [][2]int{
		{left.Major, right.Major},
		{left.Minor, right.Minor},
		{left.Patch, right.Patch},
	} {
		if difference := pair[0] - pair[1]; difference != 0 {
			return difference
		}
	}

	switch {
	case len(left.Prerelease) == 0 && len(right.Prerelease) == 0:
		return 0
	case len(left.Prerelease) == 0:
		return 1
	case len(right.Prerelease) == 0:
		return -1
	}

	for index := range max(len(left.Prerelease), len(right.Prerelease)) {
		if index >= len(left.Prerelease) {
			return -1
		}

		if index >= len(right.Prerelease) {
			return 1
		}

		if difference := compareIdentifiers(left.Prerelease[index], right.Prerelease[index]); difference != 0 {
			return difference
		}
	}

	return 0
}

// LatestTag answers the newest release among the tags, prereleases excluded unless
// asked for.
func LatestTag(tags []string, prerelease bool) string {
	best := ""

	var bestVersion Version

	for _, tag := range tags {
		version, ok := ParseVersion(tag)
		if !ok {
			continue
		}

		if !prerelease && len(version.Prerelease) > 0 {
			continue
		}

		if best == "" || CompareVersions(version, bestVersion) > 0 {
			best = tag
			bestVersion = version
		}
	}

	return best
}

// ResolveTarget is the tag as the user typed it: `0.2.0` and `v0.2.0` both resolve.
func ResolveTarget(target string, tags []string) (string, error) {
	wanted := strings.TrimSpace(target)

	if slices.Contains(tags, wanted) {
		return wanted, nil
	}

	if !strings.HasPrefix(wanted, "v") && slices.Contains(tags, "v"+wanted) {
		return "v" + wanted, nil
	}

	available := strings.Join(tags, ", ")
	if available == "" {
		available = "none"
	}

	return "", &failure.StoreError{Message: "No release tag called " + wanted + ". Available: " + available + "."}
}

// Installed is what the checkout this build runs from says about itself.
type Installed struct {
	Commit string
	// Describe is `git describe --tags --always`: a tag, `v0.1.0-3-gabc1234`, or a sha.
	Describe string
	// Tag is the tag HEAD stands on exactly, when it stands on one.
	Tag string
	// Branch is absent on a detached checkout, which is where a release lives.
	Branch string
	Dirty  bool
}

// DescribeInstalled is how a person reads where the checkout stands.
func DescribeInstalled(installed Installed) string {
	if installed.Tag != "" {
		return installed.Tag + " (" + installed.Commit + ")"
	}

	if installed.Branch != "" {
		return installed.Branch + " @ " + installed.Commit
	}

	return installed.Commit
}

// PlanKind is what an update would do.
type PlanKind string

// The five answers an update can give.
const (
	KindNoReleases PlanKind = "no-releases"
	KindUpToDate   PlanKind = "up-to-date"
	KindUpdate     PlanKind = "update"
	KindAhead      PlanKind = "ahead"
	KindDiverged   PlanKind = "diverged"
)

// Plan is what an update would do, worked out with no side effects. Behind counts
// the commits the release has that HEAD does not: only a checkout that is strictly
// behind can move to the tag without rewriting anything.
type Plan struct {
	Kind   PlanKind
	Target string
	Ahead  int
	Behind int
}

// PlanUpdate decides what to do. A target the user named is always honoured except
// when there is nothing to move; a release the checkout is already past is left
// alone, which is what keeps `main` from being dragged backwards.
func PlanUpdate(latest, target string, ahead, behind int) Plan {
	chosen := target
	if chosen == "" {
		chosen = latest
	}

	if chosen == "" {
		return Plan{Kind: KindNoReleases}
	}

	if target != "" {
		if ahead == 0 && behind == 0 {
			return Plan{Kind: KindUpToDate, Target: chosen}
		}

		return Plan{Kind: KindUpdate, Target: chosen, Ahead: ahead, Behind: behind}
	}

	switch {
	case ahead == 0 && behind == 0:
		return Plan{Kind: KindUpToDate, Target: chosen}
	case ahead == 0:
		return Plan{Kind: KindUpdate, Target: chosen, Behind: behind}
	case behind == 0:
		return Plan{Kind: KindAhead, Target: chosen, Ahead: ahead}
	default:
		return Plan{Kind: KindDiverged, Target: chosen, Ahead: ahead, Behind: behind}
	}
}

// PlanFromVersions is the binary updater's decision: a version comparison, not a
// commit count, because a release artifact has no checkout to walk. A target the user
// named is honoured even when this build is newer, which is how a downgrade is asked
// for on purpose.
func PlanFromVersions(current, latest Version, target string) Plan {
	order := CompareVersions(current, latest)

	if target != "" {
		if order == 0 {
			return Plan{Kind: KindUpToDate, Target: target}
		}

		return Plan{Kind: KindUpdate, Target: target}
	}

	switch {
	case order < 0:
		return Plan{Kind: KindUpdate, Target: latestTagOf(latest)}
	case order > 0:
		return Plan{Kind: KindAhead, Target: latestTagOf(latest)}
	default:
		return Plan{Kind: KindUpToDate, Target: latestTagOf(latest)}
	}
}

// latestTagOf renders a version back as a tag, which is the shape a message prints.
func latestTagOf(version Version) string {
	tag := "v" + strconv.Itoa(version.Major) + "." + strconv.Itoa(version.Minor) + "." + strconv.Itoa(version.Patch)

	if len(version.Prerelease) > 0 {
		tag += "-" + strings.Join(version.Prerelease, ".")
	}

	return tag
}

// ErrNotCheckout is a copy that cannot update itself: there is no git to ask.
var ErrNotCheckout = errors.New("this copy is not a git checkout")
