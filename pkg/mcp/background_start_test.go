package mcp

import (
	"net"
	"testing"
	"time"
)

// A server that never answers must not hold up the caller, and Close must
// leave nothing connected behind it.
func TestStartServersInBackgroundDoesNotWait(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Servers = nil
	cfg.AddServer(&ServerConfig{
		Name: "silent", Type: ServerTypeStdio,
		Command: []string{"sleep"}, Args: []string{"30"}, AutoStart: true,
	})
	svc := &Service{manager: NewManager(&cfg), mcpConfig: &cfg, conversations: map[string]*Conversation{}}

	start := time.Now()
	svc.StartServersInBackground(nil)
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("StartServersInBackground waited %s", d)
	}
	time.Sleep(100 * time.Millisecond)
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(svc.manager.ListClients()); n != 0 {
		t.Fatalf("%d clients left after Close", n)
	}
}

// freeAddr is a loopback address nothing listens on yet.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func downServerService(addr string) *Service {
	cfg := DefaultConfig()
	cfg.Servers = nil
	cfg.AddServer(&ServerConfig{
		Name: "late", Type: ServerTypeHTTP, URL: "http://" + addr + "/mcp",
		DefaultTimeout: 2 * time.Second, AutoStart: true,
	})
	svc := &Service{manager: NewManager(&cfg), mcpConfig: &cfg, conversations: map[string]*Conversation{}}
	svc.startRetry, svc.startRetryMax = 20*time.Millisecond, 80*time.Millisecond
	return svc
}

// A server that is down when the host starts — the network not up yet, the
// machine it lives on rebooting — is tried again until it answers, without
// the caller waiting for any of it.
func TestABackgroundServerThatIsDownIsTriedUntilItComesUp(t *testing.T) {
	addr := freeAddr(t)
	svc := downServerService(addr)
	ready := make(chan string, 1)
	start := time.Now()
	svc.StartServersInBackground(func(name string) { ready <- name })
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("StartServersInBackground waited %s", d)
	}
	time.Sleep(200 * time.Millisecond) // several attempts fail meanwhile
	s := startServerAt(t, addr, newEchoHandler("", false))
	defer s.Close()
	defer svc.Close() // before the server: its Close waits for open streams
	select {
	case name := <-ready:
		if name != "late" {
			t.Fatalf("ready %q", name)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server came up and was never connected")
	}
	if c, ok := svc.manager.GetClient("late"); !ok || c == nil || !c.IsConnected() {
		t.Fatal("not connected after ready")
	}
}

// Close ends the retrying: a server that comes up afterwards is left alone.
func TestCloseStopsRetryingABackgroundServer(t *testing.T) {
	addr := freeAddr(t)
	svc := downServerService(addr)
	ready := make(chan string, 1)
	svc.StartServersInBackground(func(name string) { ready <- name })
	time.Sleep(100 * time.Millisecond)
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	s := startServerAt(t, addr, newEchoHandler("", false))
	defer s.Close()
	select {
	case <-ready:
		t.Fatal("connected after Close")
	case <-time.After(500 * time.Millisecond):
	}
}
