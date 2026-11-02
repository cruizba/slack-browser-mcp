// Package platform isolates the few things that differ between Linux, macOS and
// Windows: where configuration and data live. Everything else in the tree is
// portable.
//
// The target system is part of [Env] rather than a build tag, so the paths of all
// three can be tested from any one of them; a build tag would leave two thirds of
// this package unverified on the machine that runs the tests.
package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Env is the environment a path is resolved against, passed in rather than read
// so a test can describe a machine it is not running on.
type Env struct {
	Getenv func(string) string
	// LookupEnv distinguishes "set to the empty string" from "unset", which the
	// nickname needs: empty means stop signing, unset means use the default.
	LookupEnv func(string) (string, bool)
	Home      string
	// OS is a GOOS value. Empty means the one this process was built for.
	OS string
}

// Process is the environment the command was actually started in.
func Process() Env {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}

	return Env{Getenv: os.Getenv, LookupEnv: os.LookupEnv, Home: home}
}

// Lookup reports whether name is set, and to what. Without LookupEnv an empty
// value reads as unset, because a caller that only knows Getenv cannot tell the
// difference and guessing "set" would silently disable the generated default.
func Lookup(env Env, name string) (string, bool) {
	if env.LookupEnv != nil {
		return env.LookupEnv(name)
	}

	if env.Getenv == nil {
		return "", false
	}

	value := env.Getenv(name)

	return value, value != ""
}

// ConfigHome is where this system keeps per-user configuration.
//
// XDG wins everywhere because it is the escape hatch that lets a user keep the
// dotfiles layout they already have, and because the TypeScript build this
// replaces wrote there on every platform. The Go build is the first release that
// runs on macOS and Windows, so there is no native-layout history to preserve.
func ConfigHome(env Env) string {
	if dir := envDir(env, "XDG_CONFIG_HOME"); dir != "" {
		return dir
	}

	switch env.os() {
	case "windows":
		if dir := envDir(env, "APPDATA"); dir != "" {
			return dir
		}

		return filepath.Join(env.Home, "AppData", "Roaming")
	case "darwin":
		return filepath.Join(env.Home, "Library", "Application Support")
	default:
		return filepath.Join(env.Home, ".config")
	}
}

// DataHome is where this system keeps per-user data: the archive, the browser
// profile and the audit log. It is a separate root from [ConfigHome] because
// that data is large and rebuildable while the configuration is small and
// precious.
func DataHome(env Env) string {
	if dir := envDir(env, "XDG_DATA_HOME"); dir != "" {
		return dir
	}

	switch env.os() {
	case "windows":
		if dir := envDir(env, "LOCALAPPDATA"); dir != "" {
			return dir
		}

		return filepath.Join(env.Home, "AppData", "Local")
	case "darwin":
		return filepath.Join(env.Home, "Library", "Application Support")
	default:
		return filepath.Join(env.Home, ".local", "share")
	}
}

func (e Env) os() string {
	if e.OS != "" {
		return e.OS
	}

	return runtime.GOOS
}

func envDir(env Env, name string) string {
	if env.Getenv == nil {
		return ""
	}

	return strings.TrimSpace(env.Getenv(name))
}
