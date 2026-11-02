// A minimal CDP client: one websocket to the browser, flat sessions, calls
// correlated by id. This is the slice of Playwright's connectOverCDP() the login
// actually uses.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

// readLimit is large because a Runtime.evaluate can return a whole
// localConfig (multi-MB) and coder/websocket's default cap is 32KiB.
const readLimit = 64 << 20

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *cdpError) Error() string { return fmt.Sprintf("CDP error %d: %s", e.Code, e.Message) }

type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

// Client is a single CDP connection. Calls fail as soon as the socket dies:
// a dropped connection is never allowed to look like a slow page.
type Client struct {
	URL string

	conn    *websocket.Conn
	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[int64]chan cdpMessage
	err     error
	done    chan struct{}

	nextID atomic.Int64
	events chan cdpMessage
}

func DialCDP(ctx context.Context, wsURL string) (*Client, error) {
	return DialCDPWithLimit(ctx, wsURL, readLimit)
}

func DialCDPWithLimit(ctx context.Context, wsURL string, limit int64) (*Client, error) {
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{})
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", wsURL, err)
	}
	conn.SetReadLimit(limit)
	client := &Client{
		URL:     wsURL,
		conn:    conn,
		pending: make(map[int64]chan cdpMessage),
		done:    make(chan struct{}),
		events:  make(chan cdpMessage, 256),
	}
	go client.readLoop()
	return client, nil
}

func (c *Client) readLoop() {
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			c.fail(fmt.Errorf("CDP connection closed: %w", err))
			return
		}
		var message cdpMessage
		if err := json.Unmarshal(data, &message); err != nil {
			continue
		}
		if message.ID != 0 {
			c.mu.Lock()
			waiter := c.pending[message.ID]
			delete(c.pending, message.ID)
			c.mu.Unlock()
			if waiter != nil {
				waiter <- message
			}
			continue
		}
		select {
		case c.events <- message:
		default:
		}
	}
}

// fail records the reason the connection died and wakes every waiter.
func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
		close(c.done)
	}
	c.pending = make(map[int64]chan cdpMessage)
	c.mu.Unlock()
}

func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Client) Done() <-chan struct{} { return c.done }

// Events carries protocol events. They are dropped, not queued, when the
// buffer is full, so a slow consumer cannot stall the reader.
func (c *Client) Events() <-chan cdpMessage { return c.events }

// Call issues one command. sessionID is empty for browser-level commands;
// page commands fail without it.
func (c *Client) Call(ctx context.Context, sessionID, method string, params, out any) error {
	id := c.nextID.Add(1)
	envelope := map[string]any{"id": id, "method": method}
	if params != nil {
		envelope["params"] = params
	}
	if sessionID != "" {
		envelope["sessionId"] = sessionID
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return err
	}

	waiter := make(chan cdpMessage, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.pending[id] = waiter
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	c.writeMu.Lock()
	err = c.conn.Write(ctx, websocket.MessageText, payload)
	c.writeMu.Unlock()
	if err != nil {
		c.fail(fmt.Errorf("CDP write failed: %w", err))
		return c.Err()
	}

	select {
	case message := <-waiter:
		if message.Error != nil {
			return message.Error
		}
		if out != nil && len(message.Result) > 0 {
			return json.Unmarshal(message.Result, out)
		}
		return nil
	case <-c.done:
		return c.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close detaches. The target keeps running: a page does not close because an
// inspector looked at it.
func (c *Client) Close() error {
	err := c.conn.Close(websocket.StatusNormalClosure, "")
	c.fail(errors.New("CDP connection closed by this process"))
	return err
}

// Abort tears the socket down without a closing handshake.
func (c *Client) Abort() {
	_ = c.conn.CloseNow()
}
