package forum

import (
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/failure"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// Context is the one place the forum's state is assembled: which file, which
// archive, which local identity, which limits. Both surfaces (CLI and MCP) build
// this and then call the same service, so a rule cannot exist on one side and not
// the other.
type Context struct {
	Store    *Store
	File     File
	Archive  *archive.Store
	Decision *DecisionLog
	// IdentityKey is the MCP client name (or the fallback) this process acts as.
	IdentityKey string
	Identity    *StoredIdentity
	Limits      Limits
	Now         time.Time
	Env         platform.Env
	// AuditLog is where every posted envelope is recorded, which the caller knows
	// because it resolves the paths.
	AuditLog string
	// Version names the build in the envelopes this process posts, so a peer can see
	// what wrote a message without this package importing the binary's build info.
	Version string
}

// OpenOptions is what opening the forum can be pointed at, which is how a test
// avoids the real files.
type OpenOptions struct {
	ClientName string
	Now        time.Time
	Store      *Store
	Archive    *archive.Store
	Decisions  *DecisionLog
	Env        platform.Env
	AuditLog   string
	// Version names the build in the envelopes this process posts, so a peer can see
	// what wrote a message without this package importing the binary's build info.
	Version string
}

// OpenForum assembles the context. Reading the identity is part of it because "am I
// registered" changes what every command is allowed to do.
func OpenForum(options OpenOptions) (Context, error) {
	store := options.Store
	if store == nil {
		return Context{}, failure.ErrNoForumStore
	}

	file, err := store.Read()
	if err != nil {
		return Context{}, err
	}

	key, identity := store.ResolveIdentity(file, options.ClientName, options.Env)

	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}

	decisions := options.Decisions
	if decisions == nil {
		return Context{}, failure.ErrNoForumStore
	}

	return Context{
		Store:       store,
		File:        file,
		Archive:     options.Archive,
		Decision:    decisions,
		IdentityKey: key,
		Identity:    identity,
		Limits:      LimitsOf(file),
		Now:         now,
		Env:         options.Env,
		AuditLog:    options.AuditLog,
		Version:     options.Version,
	}, nil
}

// MeLabel is how this process is addressed, or empty when it has no identity yet.
func (c Context) MeLabel() string {
	if c.Identity == nil {
		return ""
	}

	return LabelOf(c.Identity.Name, c.Identity.Host)
}

// RequireIdentity refuses a command that needs a name before one exists.
func (c Context) RequireIdentity() (StoredIdentity, error) {
	if c.Identity != nil {
		return *c.Identity, nil
	}

	return StoredIdentity{}, &failure.ForumError{
		Message: "No forum identity on this machine yet.",
		Hint:    "Pick a name once: `slack-browser agent-forum register <name>`.",
	}
}

// Reload re-reads the file after a write, so a caller never acts on a stale snapshot.
func (c Context) Reload() (Context, error) {
	return OpenForum(OpenOptions{
		ClientName: c.IdentityKey,
		Now:        c.Now,
		Store:      c.Store,
		Archive:    c.Archive,
		Decisions:  c.Decision,
		Env:        c.Env,
		AuditLog:   c.AuditLog,
	})
}
