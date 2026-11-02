package buildinfo_test

import (
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/buildinfo"
)

func TestLine(t *testing.T) { //nolint:paralleltest // it mutates the package variables it is checking
	saved := [3]string{buildinfo.Version, buildinfo.Describe, buildinfo.Dirty}

	t.Cleanup(func() {
		buildinfo.Version, buildinfo.Describe, buildinfo.Dirty = saved[0], saved[1], saved[2]
	})

	tests := []struct {
		name     string
		version  string
		describe string
		dirty    string
		want     string
	}{
		{
			name:    "a build with no git to ask prints the version alone",
			version: "0.8.0",
			want:    "0.8.0",
		},
		{
			name:     "a clean checkout between releases",
			version:  "0.8.0",
			describe: "v0.8.0-5-g5d0723c",
			want:     "0.8.0 (v0.8.0-5-g5d0723c)",
		},
		{
			name:     "a release checkout is the tag itself",
			version:  "0.8.0",
			describe: "v0.8.0",
			want:     "0.8.0 (v0.8.0)",
		},
		{
			name:     "uncommitted work says so",
			version:  "0.8.0",
			describe: "v0.8.0-5-g5d0723c",
			dirty:    "1",
			want:     "0.8.0 (v0.8.0-5-g5d0723c, local changes)",
		},
	}

	for _, tt := range tests { //nolint:paralleltest // the subtests share the package variables above
		t.Run(tt.name, func(t *testing.T) {
			buildinfo.Version = tt.version
			buildinfo.Describe = tt.describe
			buildinfo.Dirty = tt.dirty

			if got := buildinfo.Line(); got != tt.want {
				t.Fatalf("Line() = %q, want %q", got, tt.want)
			}
		})
	}
}
