package update_test

import (
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/update"
)

func TestParseVersion(t *testing.T) {
	t.Parallel()

	version, ok := update.ParseVersion("v1.10.2-rc.1")
	if !ok {
		t.Fatal("a tag with a prerelease did not parse")
	}

	if version.Major != 1 || version.Minor != 10 || version.Patch != 2 {
		t.Fatalf("version = %+v", version)
	}

	if len(version.Prerelease) != 2 || version.Prerelease[0] != "rc" {
		t.Fatalf("prerelease = %v", version.Prerelease)
	}

	// The tag may carry its v or not.
	if _, ok := update.ParseVersion("0.8.0"); !ok {
		t.Fatal("a tag without the v did not parse")
	}

	for _, tag := range []string{"main", "v1", "1.2", "release-1.2.3", ""} {
		if _, ok := update.ParseVersion(tag); ok {
			t.Fatalf("%q parsed as a version", tag)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		left, right string
		want        int
	}{
		{"1.9.0", "1.10.0", -1},
		{"1.10.0", "1.9.0", 1},
		{"1.0.0", "1.0.0", 0},
		{"2.0.0-rc.1", "2.0.0", -1},
		{"2.0.0", "2.0.0-rc.1", 1},
		{"2.0.0-rc.1", "2.0.0-rc.2", -1},
		{"2.0.0-alpha", "2.0.0-beta", -1},
		{"2.0.0-1", "2.0.0-alpha", -1},
	}

	for _, tt := range tests {
		left, _ := update.ParseVersion(tt.left)
		right, _ := update.ParseVersion(tt.right)

		got := update.CompareVersions(left, right)

		switch {
		case tt.want < 0 && got >= 0:
			t.Fatalf("%s vs %s = %d, want negative", tt.left, tt.right, got)
		case tt.want > 0 && got <= 0:
			t.Fatalf("%s vs %s = %d, want positive", tt.left, tt.right, got)
		case tt.want == 0 && got != 0:
			t.Fatalf("%s vs %s = %d, want 0", tt.left, tt.right, got)
		}
	}
}

func TestLatestTag(t *testing.T) {
	t.Parallel()

	tags := []string{"v0.8.0", "v0.9.0-rc.1", "v0.7.2", "not-a-tag", "v0.9.0"}

	if got := update.LatestTag(tags, false); got != "v0.9.0" {
		t.Fatalf("LatestTag = %q", got)
	}

	if got := update.LatestTag(tags, true); got != "v0.9.0" {
		t.Fatalf("LatestTag(prerelease) = %q", got)
	}

	if got := update.LatestTag([]string{"v0.9.0-rc.1"}, false); got != "" {
		t.Fatalf("a prerelease was chosen as stable: %q", got)
	}
}

func TestResolveTarget(t *testing.T) {
	t.Parallel()

	tags := []string{"v0.7.0", "v0.8.0"}

	if got, err := update.ResolveTarget("0.8.0", tags); err != nil || got != "v0.8.0" {
		t.Fatalf("ResolveTarget(0.8.0) = %q, %v", got, err)
	}

	if got, err := update.ResolveTarget("v0.7.0", tags); err != nil || got != "v0.7.0" {
		t.Fatalf("ResolveTarget(v0.7.0) = %q, %v", got, err)
	}

	if _, err := update.ResolveTarget("0.9.0", tags); err == nil {
		t.Fatal("an unknown tag resolved")
	}
}

func TestPlanUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		latest string
		target string
		ahead  int
		behind int
		want   update.PlanKind
	}{
		{name: "no releases", want: update.KindNoReleases},
		{name: "behind the latest", latest: "v0.8.0", behind: 3, want: update.KindUpdate},
		{name: "on the latest", latest: "v0.8.0", want: update.KindUpToDate},
		{name: "ahead of the latest release", latest: "v0.8.0", ahead: 2, want: update.KindAhead},
		{name: "diverged", latest: "v0.8.0", ahead: 1, behind: 1, want: update.KindDiverged},
		{name: "a named target is honoured", latest: "v0.8.0", target: "v0.7.0", behind: 1, want: update.KindUpdate},
		{name: "a named target already here", latest: "v0.8.0", target: "v0.7.0", want: update.KindUpToDate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plan := update.PlanUpdate(tt.latest, tt.target, tt.ahead, tt.behind)
			if plan.Kind != tt.want {
				t.Fatalf("Plan = %+v, want %s", plan, tt.want)
			}
		})
	}
}

func TestDescribeInstalled(t *testing.T) {
	t.Parallel()

	if got := update.DescribeInstalled(update.Installed{Tag: "v0.8.0", Commit: "abc1234"}); got != "v0.8.0 (abc1234)" {
		t.Fatalf("a tag is described as %q", got)
	}

	if got := update.DescribeInstalled(update.Installed{Branch: "main", Commit: "abc1234"}); got != "main @ abc1234" {
		t.Fatalf("a branch is described as %q", got)
	}

	if got := update.DescribeInstalled(update.Installed{Commit: "abc1234"}); got != "abc1234" {
		t.Fatalf("a detached build is described as %q", got)
	}
}

func TestPlanFromVersions(t *testing.T) {
	t.Parallel()

	older, _ := update.ParseVersion("v0.7.0")
	newer, _ := update.ParseVersion("v0.8.0")

	if plan := update.PlanFromVersions(older, newer, ""); plan.Kind != update.KindUpdate {
		t.Fatalf("an older build plans %+v", plan)
	}

	if plan := update.PlanFromVersions(newer, newer, ""); plan.Kind != update.KindUpToDate {
		t.Fatalf("an equal build plans %+v", plan)
	}

	if plan := update.PlanFromVersions(newer, older, ""); plan.Kind != update.KindAhead {
		t.Fatalf("a newer build plans %+v", plan)
	}

	// An explicitly named target is honoured, which is how a downgrade is asked for.
	if plan := update.PlanFromVersions(newer, older, "v0.7.0"); plan.Kind != update.KindUpdate || plan.Target != "v0.7.0" {
		t.Fatalf("a named target plans %+v", plan)
	}
}
