// The two reads the login depends on: localConfig from the page and the session
// cookie from the store, with the TypeScript semantics.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// StorageKeys is read in this order; localConfig_v2 wins over v1.
var StorageKeys = []string{"localConfig_v2", "localConfig_v1"}

// SessionCookieNames is the preference order. Slack now sets d and d-s, and a
// hard-coded "d" breaks accounts that only have the secure variant.
var SessionCookieNames = []string{"d", "d-s"}

type BootTeam struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
	Token  string `json:"token"`
}

func storageExpression(keys []string) string {
	encoded, err := json.Marshal(keys)
	if err != nil {
		panic(err)
	}
	return `(() => {
  const out = {};
  for (const key of ` + string(encoded) + `) out[key] = localStorage.getItem(key);
  return out;
})()`
}

// ReadLocalConfig returns the raw localConfig values as the page has them.
func (c *Client) ReadLocalConfig(ctx context.Context, sessionID string) (map[string]*string, error) {
	value, err := c.Evaluate(ctx, sessionID, storageExpression(StorageKeys))
	if err != nil {
		return nil, err
	}
	var raw map[string]*string
	if err := json.Unmarshal(value, &raw); err != nil {
		return nil, fmt.Errorf("decode localConfig: %w", err)
	}
	return raw, nil
}

// ParseBootTeams pulls teams that carry both an id and a token. Values are
// read in StorageKeys order, then by team key, so a re-run is deterministic.
func ParseBootTeams(raw map[string]*string) []BootTeam {
	seen := map[string]bool{}
	var teams []BootTeam
	for _, key := range StorageKeys {
		value := raw[key]
		if value == nil || *value == "" {
			continue
		}
		var parsed struct {
			Teams map[string]BootTeam `json:"teams"`
		}
		if err := json.Unmarshal([]byte(*value), &parsed); err != nil {
			continue
		}
		keys := make([]string, 0, len(parsed.Teams))
		for teamKey := range parsed.Teams {
			keys = append(keys, teamKey)
		}
		sort.Strings(keys)
		for _, teamKey := range keys {
			team := parsed.Teams[teamKey]
			if team.ID == "" || team.Token == "" || seen[team.ID] {
				continue
			}
			seen[team.ID] = true
			teams = append(teams, team)
		}
	}
	return teams
}

// SlackCookies returns the cookies the browser would send to slack.com.
func (c *Client) SlackCookies(ctx context.Context) ([]Cookie, error) {
	all, err := c.Cookies(ctx)
	if err != nil {
		return nil, err
	}
	var matching []Cookie
	for _, cookie := range all {
		if CookieHostMatches(cookie.Domain, "app.slack.com") || CookieHostMatches(cookie.Domain, "slack.com") {
			matching = append(matching, cookie)
		}
	}
	return matching, nil
}

// PickSessionCookie returns the first present cookie in SessionCookieNames,
// with the name it actually has.
func PickSessionCookie(cookies []Cookie) (Cookie, bool) {
	for _, name := range SessionCookieNames {
		for _, cookie := range cookies {
			if cookie.Name == name && cookie.Value != "" {
				return cookie, true
			}
		}
	}
	return Cookie{}, false
}
