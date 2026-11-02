package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/cruizba/slack-browser-mcp/internal/buildinfo"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
	"github.com/cruizba/slack-browser-mcp/internal/update"
)

// commandUpdate moves this binary to a release. It downloads rather than checking out:
// the Go build is a single file, and the checkout the TypeScript build updated is the
// thing this replaces.
func commandUpdate(ctx Context, parsed ParsedArgs) int {
	client := update.NewClient(platformValue(ctx.Env, "GITHUB_TOKEN"))

	target, _ := parsed.String("to")

	release, err := updateRelease(client, target, parsed.Bool("pre"))
	if err != nil {
		return fail(ctx, err)
	}

	current, known := update.ParseVersion(buildinfo.Version)
	if !known {
		current = update.Version{}
	}

	latest, _ := update.ParseVersion(release.Tag)

	plan := update.PlanFromVersions(current, latest, target)

	emit(ctx.Stdout, "this build: %s\n", buildinfo.Line())
	emit(ctx.Stdout, "latest release: %s\n", release.Tag)

	if parsed.Bool("check") {
		emit(ctx.Stdout, "%s\n", update.Describe(plan))
		emit(ctx.Stderr, "nothing replaced; run `slack-browser update` to install it\n")

		return 0
	}

	if plan.Kind == update.KindUpToDate || plan.Kind == update.KindAhead || plan.Kind == update.KindDiverged {
		emit(ctx.Stdout, "%s\n", update.Describe(plan))

		return 0
	}

	asset, ok := update.AssetFor(release, runtime.GOOS, runtime.GOARCH)
	if !ok {
		return fail(ctx, errNoAsset(runtime.GOOS, runtime.GOARCH, release.Tag))
	}

	digests, err := client.Checksums(context.Background(), release)
	if err != nil {
		return fail(ctx, err)
	}

	body, err := client.Fetch(context.Background(), release, asset, digests)
	if err != nil {
		return fail(ctx, err)
	}

	self, err := os.Executable()
	if err != nil {
		return fail(ctx, err)
	}

	// A symlinked install replaces what the link points at, not the link itself.
	resolved, err := filepath.EvalSymlinks(self)
	if err == nil {
		self = resolved
	}

	if err := update.Replace(self, body); err != nil {
		return fail(ctx, err)
	}

	emit(ctx.Stdout, "installed %s into %s\n", release.Tag, self)
	emit(ctx.Stderr, "restart the agent so its MCP server loads the new build\n")

	return 0
}

// updateRelease answers the release to consider: the one named, or the newest.
func updateRelease(client *update.Client, target string, prerelease bool) (update.Release, error) {
	if target != "" {
		return client.ByTag(context.Background(), target)
	}

	return client.Latest(context.Background(), prerelease)
}

// errNoAsset says which build is missing rather than leaving a silent no-op.
func errNoAsset(goos, goarch, tag string) error {
	return errors.New("release " + tag + " publishes no build for " + goos + "/" + goarch +
		" (looked for " + update.TargetName(goos, goarch) + ")")
}

// platformValue reads one environment variable through the harness's own Env, so a
// test can set it without touching the process.
func platformValue(env platform.Env, name string) string {
	value, _ := platform.Lookup(env, name)

	return value
}
