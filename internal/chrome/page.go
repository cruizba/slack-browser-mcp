package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Target is one page or worker Chrome exposes over CDP.
type Target struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	URL      string `json:"url"`
	Title    string `json:"title"`
}

// Targets is every page the browser has open.
func (c *Client) Targets(ctx context.Context) ([]Target, error) {
	var result struct {
		TargetInfos []Target `json:"targetInfos"`
	}
	if err := c.Call(ctx, "", "Target.getTargets", struct{}{}, &result); err != nil {
		return nil, err
	}

	return result.TargetInfos, nil
}

// FirstPage returns the first page target, creating one if the browser started
// without a tab.
func (c *Client) FirstPage(ctx context.Context) (Target, error) {
	targets, err := c.Targets(ctx)
	if err != nil {
		return Target{}, err
	}

	for _, target := range targets {
		if target.Type == "page" {
			return target, nil
		}
	}

	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := c.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
		return Target{}, err
	}

	return Target{TargetID: created.TargetID, Type: "page", URL: "about:blank"}, nil
}

// Attach opens a flat session to a target. Attaching does not open a tab and
// does not mark the page as automated; several clients may attach at once.
func (c *Client) Attach(ctx context.Context, targetID string) (string, error) {
	var result struct {
		SessionID string `json:"sessionId"`
	}

	params := map[string]any{"targetId": targetID, "flatten": true}
	if err := c.Call(ctx, "", "Target.attachToTarget", params, &result); err != nil {
		return "", err
	}

	if result.SessionID == "" {
		return "", errors.New("Target.attachToTarget returned no session")
	}

	return result.SessionID, nil
}

// Navigate loads a URL in an attached page and waits for the load to finish.
func (c *Client) Navigate(ctx context.Context, sessionID, url string, timeout time.Duration) error {
	if err := c.Call(ctx, sessionID, "Page.enable", struct{}{}, nil); err != nil {
		return err
	}

	if err := c.Call(ctx, sessionID, "Page.navigate", map[string]any{"url": url}, nil); err != nil {
		return err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		value, err := c.Evaluate(ctx, sessionID, "document.readyState")
		if err == nil && string(value) == `"complete"` {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}

	return fmt.Errorf("page did not finish loading %s in %s", url, timeout)
}

// Evaluate runs an expression in the page and answers its value as JSON.
func (c *Client) Evaluate(ctx context.Context, sessionID, expression string) (json.RawMessage, error) {
	var result struct {
		Result struct {
			Type        string          `json:"type"`
			Subtype     string          `json:"subtype"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}

	params := map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	}
	if err := c.Call(ctx, sessionID, "Runtime.evaluate", params, &result); err != nil {
		return nil, err
	}

	if result.ExceptionDetails != nil {
		detail := result.ExceptionDetails.Exception.Description
		if detail == "" {
			detail = result.ExceptionDetails.Text
		}

		return nil, fmt.Errorf("page exception: %s", detail)
	}

	return result.Result.Value, nil
}

// Cookie is one browser cookie.
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires,omitempty"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	SameSite string  `json:"sameSite,omitempty"`
}

// Cookies reads the whole browser cookie store, httpOnly included, which is
// what page JavaScript cannot see and what Slack's session cookie is.
func (c *Client) Cookies(ctx context.Context) ([]Cookie, error) {
	var result struct {
		Cookies []Cookie `json:"cookies"`
	}
	if err := c.Call(ctx, "", "Storage.getCookies", struct{}{}, &result); err != nil {
		return nil, err
	}

	return result.Cookies, nil
}

// SetCookie files a cookie, which a test uses to stand in for a sign-in.
func (c *Client) SetCookie(ctx context.Context, cookie Cookie) error {
	params := map[string]any{"cookies": []Cookie{cookie}}
	return c.Call(ctx, "", "Storage.setCookies", params, nil)
}

// DeleteCookie goes through the page session: Chrome 156 has no
// Storage.deleteCookies, so deletion is the one cookie operation that needs a
// session (Network domain) while reading and writing work browser-wide.
func (c *Client) DeleteCookie(ctx context.Context, sessionID, name, domain, path string) error {
	params := map[string]any{"name": name, "domain": domain, "path": path}
	return c.Call(ctx, sessionID, "Network.deleteCookies", params, nil)
}

// CookieHostMatches applies cookie domain rules: ".slack.com" covers
// "app.slack.com" but not "notslack.com".
func CookieHostMatches(domain, host string) bool {
	bare := strings.TrimPrefix(domain, ".")
	host = strings.ToLower(host)
	bare = strings.ToLower(bare)

	return host == bare || strings.HasSuffix(host, "."+bare)
}
