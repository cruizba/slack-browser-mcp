// Package mcp is the agent-facing surface: the JSON-RPC protocol on stdio and the
// tools it serves.
//
// The protocol is written here rather than pulled from an SDK because the output
// is compared byte for byte against the TypeScript build this replaces, and the
// tool schemas have to keep the key order that build emits. A struct per message
// is the cheapest way to guarantee that.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

//go:embed tools.json forum_tools.json
var toolFiles embed.FS

// ProtocolVersion is what this server speaks, and what it echoes back when a
// client asks for it.
const ProtocolVersion = "2025-06-18"

// Tool is one entry of `tools/list`: its name and the schema object as it goes on
// the wire, already in the order the oracle prints it.
type Tool struct {
	Name   string
	Schema json.RawMessage
}

// catalog is what tools.json holds: the instructions the server sends once, and
// the tool list.
type catalog struct {
	Instructions string            `json:"instructions"`
	Tools        []json.RawMessage `json:"tools"`
}

// Catalog reads the embedded tool list. The schemas are stored pretty-printed so
// they can be read and reviewed; they go out compact, which is what the wire
// format is.
func Catalog() (string, []Tool, error) {
	raw, err := toolFiles.ReadFile("tools.json")
	if err != nil {
		return "", nil, err
	}

	var parsed catalog
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", nil, err
	}

	tools := make([]Tool, 0, len(parsed.Tools))

	for _, schema := range parsed.Tools {
		var (
			name    struct{ Name string }
			compact bytes.Buffer
		)

		if err := json.Unmarshal(schema, &name); err != nil {
			return "", nil, err
		}

		if err := json.Compact(&compact, schema); err != nil {
			return "", nil, err
		}

		tools = append(tools, Tool{Name: name.Name, Schema: compact.Bytes()})
	}

	return parsed.Instructions, tools, nil
}

// Handler answers one tool call. The arguments arrive as the client sent them, and
// what it returns is rendered as the tool's result text.
type Handler func(ctx context.Context, args json.RawMessage) ([]Content, error)

// Content is one block of a tool result.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// Text is the common result: one block of prose.
func Text(value string) []Content {
	return []Content{{Type: "text", Text: value}}
}

// Server is the stdio endpoint.
type Server struct {
	Version      string
	Instructions string
	Tools        []Tool
	Handlers     map[string]Handler
	In           io.Reader
	Out          io.Writer
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// response keeps the key order the TypeScript build emits: the payload, then the
// protocol version, then the id.
type response struct {
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve answers requests until the stream ends. A notification gets no response,
// which is what keeps the transcript a client reads in step with the one it wrote.
func (s *Server) Serve(ctx context.Context) error {
	reader := bufio.NewReaderSize(s.In, 1<<20)

	var write sync.Mutex

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return err
		}

		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			answers := s.handle(ctx, trimmed)

			write.Lock()

			for _, answer := range answers {
				if _, err := s.Out.Write(append(answer, '\n')); err != nil {
					write.Unlock()

					return err
				}
			}

			write.Unlock()
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return err
		}
	}
}

// handle answers one request. It returns no message for a notification.
func (s *Server) handle(ctx context.Context, line []byte) [][]byte {
	var incoming request

	if err := json.Unmarshal(line, &incoming); err != nil {
		return [][]byte{s.encode(response{Error: &rpcError{Code: -32700, Message: "parse error"}})}
	}

	if incoming.ID == nil {
		return nil
	}

	switch incoming.Method {
	case "initialize":
		return [][]byte{s.encode(response{Result: s.initialized(incoming.Params), ID: incoming.ID})}
	case "tools/list":
		return [][]byte{s.encode(response{Result: s.toolList(), ID: incoming.ID})}
	case "tools/call":
		answer := s.call(ctx, incoming.Params)
		answer.ID = incoming.ID

		return [][]byte{s.encode(answer)}
	case "ping":
		return [][]byte{s.encode(response{Result: json.RawMessage(`{}`), ID: incoming.ID})}
	default:
		return [][]byte{s.encode(response{
			Error: &rpcError{Code: -32601, Message: "method not found: " + incoming.Method},
			ID:    incoming.ID,
		})}
	}
}

func (s *Server) encode(answer response) []byte {
	answer.JSONRPC = "2.0"

	encoded, err := marshalPlain(answer)
	if err != nil {
		encoded = []byte(`{"error":{"code":-32603,"message":"encoding the answer failed"},"jsonrpc":"2.0"}`)
	}

	return encoded
}

func (s *Server) initialized(params json.RawMessage) json.RawMessage {
	var asked struct {
		ProtocolVersion string `json:"protocolVersion"`
	}

	_ = json.Unmarshal(params, &asked)

	version := asked.ProtocolVersion
	if version == "" {
		version = ProtocolVersion
	}

	result := struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools struct {
				ListChanged bool `json:"listChanged"`
			} `json:"tools"`
		} `json:"capabilities"`
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions"`
	}{}

	result.ProtocolVersion = version
	result.Capabilities.Tools.ListChanged = true
	result.ServerInfo.Name = "slack-browser"
	result.ServerInfo.Version = s.Version
	result.Instructions = s.Instructions

	encoded, err := marshalPlain(result)
	if err != nil {
		return json.RawMessage(`{}`)
	}

	return encoded
}

func (s *Server) toolList() json.RawMessage {
	var list bytes.Buffer

	list.WriteString(`{"tools":[`)

	for index, tool := range s.Tools {
		if index > 0 {
			list.WriteByte(',')
		}

		list.Write(tool.Schema)
	}

	list.WriteString(`]}`)

	return list.Bytes()
}

func (s *Server) call(ctx context.Context, params json.RawMessage) response {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}

	if err := json.Unmarshal(params, &call); err != nil {
		return response{Error: &rpcError{Code: -32602, Message: "invalid tool call"}}
	}

	handler, known := s.Handlers[call.Name]
	if !known {
		return response{Error: &rpcError{Code: -32602, Message: "unknown tool: " + call.Name}}
	}

	content, err := handler(ctx, call.Arguments)
	if err != nil {
		// A tool that failed says so in its result rather than as a protocol
		// error: the agent has to read the reason, not fall over.
		return response{Result: renderCall(Text(err.Error()), true)}
	}

	return response{Result: renderCall(content, false)}
}

func renderCall(content []Content, failed bool) json.RawMessage {
	payload := struct {
		Content []Content `json:"content"`
		IsError bool      `json:"isError,omitempty"`
	}{Content: content, IsError: failed}

	encoded, err := marshalPlain(payload)
	if err != nil {
		return json.RawMessage(`{"content":[{"type":"text","text":"the tool result could not be encoded"}]}`)
	}

	return encoded
}

// UnknownTool is the answer of a tool this build has not ported yet, kept honest
// instead of pretending the call worked.
func UnknownTool(name string) Handler {
	return func(context.Context, json.RawMessage) ([]Content, error) {
		return nil, fmt.Errorf("%s is not available in this build yet", name)
	}
}

// TrimSpace is a helper for handlers that take a free-text argument.
func TrimSpace(value string) string {
	return strings.TrimSpace(value)
}

// marshalPlain encodes without the HTML escaping the standard library applies by
// default: a schema that says `<nickname>` has to reach the client as those
// characters, not as \u003c, or the transcript stops matching the build this
// replaces.
func marshalPlain(value any) ([]byte, error) {
	var buffer bytes.Buffer

	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(value); err != nil {
		return nil, err
	}

	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}
