package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonMikone/CloudWire/core/internal/msg"
)

func startServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cwapi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "core.sock")
	s := NewServer(nil)
	type echoParams struct {
		Name string `json:"name"`
	}
	s.Handle("test.echo", Bind(func(_ context.Context, p echoParams) (any, error) {
		return map[string]string{"hello": p.Name}, nil
	}))
	s.Handle("test.fail", NoParams(func(context.Context) (any, error) {
		return nil, Fail("mount.pointInUse", msg.New("mount.pointInUse", "path", "/x")).WithData("mountPoint", "/x")
	}))
	s.Handle("test.invalid", NoParams(func(context.Context) (any, error) {
		return nil, InvalidText(msg.New("vault.invalidName", "name", "a/b"))
	}))
	s.Handle("test.slow", NoParams(func(context.Context) (any, error) {
		time.Sleep(150 * time.Millisecond)
		return "slow", nil
	}))
	// A stale socket file must not prevent listening.
	_ = os.WriteFile(sock, []byte("stale"), 0o644)
	if err := s.ListenUnix(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v (%v), want 0600", fi.Mode().Perm(), err)
	}
	return s, sock
}

type client struct {
	nc net.Conn
	r  *bufio.Reader
}

func dial(t *testing.T, sock string) *client {
	t.Helper()
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	return &client{nc: nc, r: bufio.NewReader(nc)}
}

func (c *client) send(t *testing.T, s string) {
	t.Helper()
	if _, err := c.nc.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
}

func (c *client) recv(t *testing.T) map[string]any {
	t.Helper()
	_ = c.nc.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("bad line %q: %v", line, err)
	}
	return m
}

func TestFramingAcrossPartialWrites(t *testing.T) {
	_, sock := startServer(t)
	c := dial(t, sock)
	c.send(t, `{"jsonrpc":"2.0","id":1,"method":"test.echo","params":{"na`)
	time.Sleep(20 * time.Millisecond)
	c.send(t, `me":"Mike"}}`+"\n")
	m := c.recv(t)
	if m["id"].(float64) != 1 || m["result"].(map[string]any)["hello"] != "Mike" {
		t.Fatalf("unexpected response %v", m)
	}
}

func TestPipelinedRequestsMatchedByID(t *testing.T) {
	_, sock := startServer(t)
	c := dial(t, sock)
	c.send(t, `{"jsonrpc":"2.0","id":7,"method":"test.slow"}`+"\n"+`{"jsonrpc":"2.0","id":8,"method":"test.echo","params":{"name":"x"}}`+"\n")
	first, second := c.recv(t), c.recv(t)
	if first["id"].(float64) != 8 || second["id"].(float64) != 7 || second["result"] != "slow" {
		t.Fatalf("fast request must not wait for slow one: %v / %v", first, second)
	}
}

func TestErrors(t *testing.T) {
	_, sock := startServer(t)
	c := dial(t, sock)
	c.send(t, `{"jsonrpc":"2.0","id":1,"method":"nope"}`+"\n")
	if e := c.recv(t)["error"].(map[string]any); e["code"].(float64) != CodeMethodNotFound {
		t.Fatalf("unknown method: %v", e)
	}
	c.send(t, `{"jsonrpc":"2.0","id":2,"method":"test.echo","params":{"name":5}}`+"\n")
	if e := c.recv(t)["error"].(map[string]any); e["code"].(float64) != CodeInvalidParams {
		t.Fatalf("invalid params: %v", e)
	}
	c.send(t, `{"jsonrpc":"2.0","id":3,"method":"test.fail"}`+"\n")
	e := c.recv(t)["error"].(map[string]any)
	data := e["data"].(map[string]any)
	params, _ := data["params"].(map[string]any)
	if e["code"].(float64) != CodeApplication || data["code"] != "mount.pointInUse" || data["mountPoint"] != "/x" ||
		data["message"] != "Another Mount already uses /x" || data["key"] != "mount.pointInUse" || params["path"] != "/x" {
		t.Fatalf("application error: %v", e)
	}
	// Parameter errors a user can cause carry a text as well.
	c.send(t, `{"jsonrpc":"2.0","id":4,"method":"test.invalid"}`+"\n")
	e = c.recv(t)["error"].(map[string]any)
	data, _ = e["data"].(map[string]any)
	if e["code"].(float64) != CodeInvalidParams || data["key"] != "vault.invalidName" ||
		data["message"] != `"a/b" cannot be used as a Vault name` {
		t.Fatalf("invalid params with text: %v", e)
	}
	c.send(t, "garbage\n")
	if e := c.recv(t)["error"].(map[string]any); e["code"].(float64) != CodeParseError {
		t.Fatalf("parse error: %v", e)
	}
}

func TestEventsOnlyToSubscribers(t *testing.T) {
	s, sock := startServer(t)
	sub := dial(t, sock)
	other := dial(t, sock)
	sub.send(t, `{"jsonrpc":"2.0","id":1,"method":"events.subscribe","params":{"client":"app"}}`+"\n")
	sub.recv(t)
	if !s.HasClient("app") || s.HasClient("finder") {
		t.Fatal("client identification wrong")
	}
	s.Publish("mount.status", map[string]string{"id": "m1"})
	ev := sub.recv(t)
	params := ev["params"].(map[string]any)
	if ev["method"] != "event" || params["type"] != "mount.status" || params["data"].(map[string]any)["id"] != "m1" {
		t.Fatalf("event shape: %v", ev)
	}
	_ = other.nc.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := other.r.ReadBytes('\n'); err == nil {
		t.Fatal("unsubscribed connection received an event")
	}
}
