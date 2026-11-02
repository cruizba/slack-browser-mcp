package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestChromeArgsKeepTheProfileHonest(t *testing.T) {
	args := ChromeArgs("/tmp/profile", 9222, false)
	want := []string{
		"--user-data-dir=/tmp/profile",
		"--remote-debugging-port=9222",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-sync",
		"--disable-extensions",
		"--disable-features=Translate,MediaRouter",
		"about:blank",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("args = %q, want %q", args, want)
	}
	for _, forbidden := range []string{"--enable-automation", "--headless"} {
		if slices.ContainsFunc(args, func(arg string) bool { return len(arg) >= len(forbidden) && arg[:len(forbidden)] == forbidden }) {
			t.Fatalf("headful launch passes %s, which makes Slack refuse the sign-in", forbidden)
		}
	}
	if headless := ChromeArgs("/tmp/profile", 9222, true); !slices.Contains(headless, "--headless=new") {
		t.Fatalf("headless launch does not pass --headless=new: %q", headless)
	}
}

func TestFindChromeHonoursChromePath(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHROME_PATH", fake)
	found, err := FindChrome()
	if err != nil {
		t.Fatal(err)
	}
	if found != fake {
		t.Fatalf("found %q, want CHROME_PATH %q", found, fake)
	}
}

func TestFindChromeSkipsFilesThatAreNotExecutable(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHROME_PATH", fake)
	found, _ := FindChrome()
	if found == fake {
		t.Fatalf("a non-executable CHROME_PATH was accepted")
	}
}

func TestParseBootTeamsReadsBothKeysInOrder(t *testing.T) {
	v2 := `{"teams":{"T0B":{"id":"T0B","token":"xoxc-b"},"T0A":{"id":"T0A","token":"xoxc-a","name":"A"}},"slackUrl":"x"}`
	v1 := `{"teams":{"T0C":{"id":"T0C","token":"xoxc-c"},"T0A":{"id":"T0A","token":"xoxc-a"}}}`
	empty := ""
	raw := map[string]*string{"localConfig_v2": &v2, "localConfig_v1": &v1, "localConfig_v3": &empty}

	teams := ParseBootTeams(raw)
	var ids []string
	for _, team := range teams {
		ids = append(ids, team.ID)
	}
	if want := []string{"T0A", "T0B", "T0C"}; !slices.Equal(ids, want) {
		t.Fatalf("teams = %v, want %v", ids, want)
	}
	if teams[0].Name != "A" || teams[0].Token != "xoxc-a" {
		t.Fatalf("v2 wins over v1 for the same team id, got %+v", teams[0])
	}
}

func TestParseBootTeamsIgnoresUnusableValues(t *testing.T) {
	broken := `{"teams":`
	noToken := `{"teams":{"T0A":{"id":"T0A"}}}`
	raw := map[string]*string{"localConfig_v2": &broken, "localConfig_v1": &noToken}
	if teams := ParseBootTeams(raw); len(teams) != 0 {
		t.Fatalf("parsed %+v, want no teams", teams)
	}
}

func TestPickSessionCookieUsesTheNameSlackSet(t *testing.T) {
	cookies := []Cookie{{Name: "d-s", Value: "xoxd-secure"}}
	picked, ok := PickSessionCookie(cookies)
	if !ok || picked.Name != "d-s" || picked.Value != "xoxd-secure" {
		t.Fatalf("picked %+v/%t, want the d-s cookie", picked, ok)
	}

	cookies = []Cookie{{Name: "d", Value: "xoxd-plain"}, {Name: "d-s", Value: "xoxd-secure"}}
	if picked, _ := PickSessionCookie(cookies); picked.Name != "d" {
		t.Fatalf("picked %q, want d to win the preference order", picked.Name)
	}

	cookies = []Cookie{{Name: "d", Value: ""}, {Name: "x", Value: "y"}}
	if _, ok := PickSessionCookie(cookies); ok {
		t.Fatalf("an empty d was accepted")
	}
}

func TestCookieHostMatchesHonoursDomainRules(t *testing.T) {
	cases := []struct {
		domain, host string
		want         bool
	}{
		{".slack.com", "app.slack.com", true},
		{"slack.com", "slack.com", true},
		{".slack.com", "slack.com", true},
		{".slack.com", "notslack.com", false},
		{".slack.com", "app.slack.com.evil.test", false},
	}
	for _, tc := range cases {
		if got := CookieHostMatches(tc.domain, tc.host); got != tc.want {
			t.Fatalf("CookieHostMatches(%q, %q) = %t, want %t", tc.domain, tc.host, got, tc.want)
		}
	}
}
