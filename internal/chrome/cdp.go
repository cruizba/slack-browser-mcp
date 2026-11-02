package chrome

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

// Event is one message Chrome sent that nobody is waiting for: a navigation,
// a request, a target appearing.
type Event struct {
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
	pending map[int64]chan Event
	err     error
	done    chan struct{}

	nextID atomic.Int64
	events chan Event
}

// DialCDP attaches to a browser's debugging endpoint.
func DialCDP(ctx context.Context, wsURL string) (*Client, error) {
	return DialCDPWithLimit(ctx, wsURL, readLimit)
}

// DialCDPWithLimit is DialCDP with a read limit, which a test uses to prove that an
// oversized frame is a closed connection rather than a hung one.
func DialCDPWithLimit(ctx context.Context, wsURL string, limit int64) (*Client, error) {
	conn, response, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{})
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", wsURL, err)
	}

	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}

	conn.SetReadLimit(limit)

	client := &Client{
		URL:     wsURL,
		conn:    conn,
		pending: make(map[int64]chan Event),
		done:    make(chan struct{}),
		events:  make(chan Event, 256),
	}
	// The read loop outlives the call that started it: it ends when the socket does,
	// which is what closes the client.
	go client.readLoop() //nolint:gosec // the loop ends with the socket, not with the dial

	return client, nil
}

func (c *Client) readLoop() {
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			c.fail(fmt.Errorf("CDP connection closed: %w", err))
			return
		}

		var message Event
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

	c.pending = make(map[int64]chan Event)
	c.mu.Unlock()
}

// Err answers the failure that ended the connection, if one did.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.err
}

// Done is closed when the connection ends, which is what a caller waits on.
func (c *Client) Done() <-chan struct{} { return c.done }

// Events carries protocol events. They are dropped, not queued, when the
// buffer is full, so a slow consumer cannot stall the reader.
// Events is the stream of protocol events, which is how a page tells a caller that
// it navigated or that a request went out.
func (c *Client) Events() <-chan Event { return c.events }

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

	waiter := make(chan Event, 1)

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
