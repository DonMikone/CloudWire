// Package api is the Core's JSON-RPC 2.0 server: newline-delimited JSON on a
// 0600 unix socket, with server-pushed events for subscribed connections.
package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
)

// JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeApplication    = -32000
)

// maxLine bounds one request line.
const maxLine = 16 << 20

// Handler serves one method.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Error is an application error with a stable code (plan Appendix A).
type Error struct {
	Code    string
	Message string
	Data    map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf builds an application error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WithData attaches extra fields to the error's data object.
func (e *Error) WithData(k string, v any) *Error {
	if e.Data == nil {
		e.Data = map[string]any{}
	}
	e.Data[k] = v
	return e
}

// InvalidParams is returned for malformed parameters.
type InvalidParams struct{ Err error }

func (e InvalidParams) Error() string { return "invalid params: " + e.Err.Error() }

// Invalid returns an InvalidParams error with a message.
func Invalid(format string, args ...any) error {
	return InvalidParams{fmt.Errorf(format, args...)}
}

// Bind adapts a typed handler: params are decoded into T. Unknown fields are
// ignored so newer clients keep working with older Cores.
func Bind[T any](fn func(ctx context.Context, p T) (any, error)) Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p T
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
			if err := json.Unmarshal(trimmed, &p); err != nil {
				return nil, InvalidParams{err}
			}
		}
		return fn(ctx, p)
	}
}

// NoParams adapts a handler without parameters.
func NoParams(fn func(ctx context.Context) (any, error)) Handler {
	return func(ctx context.Context, _ json.RawMessage) (any, error) { return fn(ctx) }
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type eventMsg struct {
	JSONRPC string     `json:"jsonrpc"`
	Method  string     `json:"method"`
	Params  eventParam `json:"params"`
}

type eventParam struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Server is the JSON-RPC server.
type Server struct {
	log      *slog.Logger
	mu       sync.RWMutex
	handlers map[string]Handler
	conns    map[*conn]struct{}
	ln       net.Listener
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
}

// NewServer creates a server with the built-in events.subscribe method.
func NewServer(log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{log: log, handlers: map[string]Handler{}, conns: map[*conn]struct{}{}, ctx: ctx, cancel: cancel}
	s.Handle("events.subscribe", func(ctx context.Context, raw json.RawMessage) (any, error) {
		c, _ := ctx.Value(connKey{}).(*conn)
		if c == nil {
			return nil, errors.New("no connection")
		}
		var p struct {
			Client string `json:"client"`
		}
		_ = json.Unmarshal(raw, &p)
		c.mu.Lock()
		c.subscribed = true
		c.client = p.Client
		c.mu.Unlock()
		return struct{}{}, nil
	})
	return s
}

// Handle registers a method.
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.handlers[method]; dup {
		panic("duplicate method " + method)
	}
	s.handlers[method] = h
}

// Methods returns the registered method names.
func (s *Server) Methods() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.handlers))
	for m := range s.handlers {
		out = append(out, m)
	}
	return out
}

type connKey struct{}

type conn struct {
	nc         net.Conn
	wmu        sync.Mutex // serialises writes
	mu         sync.Mutex
	subscribed bool
	client     string
	events     chan []byte
}

// ListenUnix removes a stale socket file, listens and restricts it to 0600.
func (s *Server) ListenUnix(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	s.Serve(ln)
	return nil
}

// Serve accepts connections on ln in the background.
func (s *Server) Serve(ln net.Listener) {
	s.ln = ln
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.serveConn(nc)
			}()
		}
	}()
}

// Close stops accepting and closes every connection.
func (s *Server) Close() {
	s.cancel()
	if s.ln != nil {
		_ = s.ln.Close()
	}
	s.mu.Lock()
	for c := range s.conns {
		_ = c.nc.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Server) serveConn(nc net.Conn) {
	c := &conn{nc: nc, events: make(chan []byte, 256)}
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	done := make(chan struct{})
	defer func() {
		close(done)
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		_ = nc.Close()
	}()
	go func() {
		for {
			select {
			case b := <-c.events:
				if c.write(b) != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()
	ctx := context.WithValue(s.ctx, connKey{}, c)
	r := bufio.NewReaderSize(nc, 64<<10)
	var reqs sync.WaitGroup
	defer reqs.Wait()
	for {
		line, err := readLine(r)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.log.Debug("rpc read", "err", err)
			}
			return
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = c.writeJSON(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: CodeParseError, Message: "parse error"}})
			continue
		}
		reqs.Add(1)
		go func() {
			defer reqs.Done()
			resp := s.dispatch(ctx, req)
			if len(req.ID) == 0 {
				return // notification: no response
			}
			if err := c.writeJSON(resp); err != nil {
				s.log.Debug("rpc write", "err", err)
			}
		}()
	}
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk...)
		if len(buf) > maxLine {
			return nil, errors.New("request line too long")
		}
		if !isPrefix {
			return buf, nil
		}
	}
}

func (s *Server) dispatch(ctx context.Context, req request) (resp response) {
	resp = response{JSONRPC: "2.0", ID: req.ID}
	if req.JSONRPC != "2.0" || req.Method == "" {
		resp.Error = &rpcError{Code: CodeInvalidRequest, Message: "invalid request"}
		return
	}
	s.mu.RLock()
	h := s.handlers[req.Method]
	s.mu.RUnlock()
	if h == nil {
		resp.Error = &rpcError{Code: CodeMethodNotFound, Message: "method not found: " + req.Method}
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("rpc panic", "method", req.Method, "panic", r)
			resp.Result = nil
			resp.Error = &rpcError{Code: CodeApplication, Message: "internal error",
				Data: map[string]any{"code": "core.internal", "message": fmt.Sprint(r)}}
		}
	}()
	result, err := h(ctx, req.Params)
	if err != nil {
		resp.Error = toRPCError(err)
		if resp.Error.Code == CodeApplication {
			s.log.Debug("rpc error", "method", req.Method, "err", err)
		}
		return
	}
	if result == nil {
		result = struct{}{}
	}
	resp.Result = result
	return
}

func toRPCError(err error) *rpcError {
	var ip InvalidParams
	if errors.As(err, &ip) {
		return &rpcError{Code: CodeInvalidParams, Message: ip.Error()}
	}
	var ae *Error
	if errors.As(err, &ae) {
		data := map[string]any{}
		for k, v := range ae.Data {
			data[k] = v
		}
		data["code"] = ae.Code
		data["message"] = ae.Message
		return &rpcError{Code: CodeApplication, Message: ae.Message, Data: data}
	}
	return &rpcError{Code: CodeApplication, Message: err.Error(),
		Data: map[string]any{"code": "core.internal", "message": err.Error()}}
}

func (c *conn) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.write(b)
}

func (c *conn) write(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	b = append(b, '\n')
	_, err := c.nc.Write(b)
	return err
}

// Publish pushes an event to every subscribed connection. Slow clients drop
// events instead of blocking the Core.
func (s *Server) Publish(eventType string, data any) {
	b, err := json.Marshal(eventMsg{JSONRPC: "2.0", Method: "event", Params: eventParam{Type: eventType, Data: data}})
	if err != nil {
		s.log.Error("event marshal", "type", eventType, "err", err)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.conns {
		c.mu.Lock()
		sub := c.subscribed
		c.mu.Unlock()
		if !sub {
			continue
		}
		select {
		case c.events <- b:
		default:
			s.log.Warn("event dropped for slow client", "type", eventType)
		}
	}
}

// HasClient reports whether a subscribed connection identified itself as client.
func (s *Server) HasClient(client string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.conns {
		c.mu.Lock()
		ok := c.subscribed && c.client == client
		c.mu.Unlock()
		if ok {
			return true
		}
	}
	return false
}
