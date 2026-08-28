package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type recordingLogger struct {
	warns []string
}

func (l *recordingLogger) Info(msg string, args ...any)  {}
func (l *recordingLogger) Warn(msg string, args ...any)  { l.warns = append(l.warns, msg) }
func (l *recordingLogger) Error(msg string, args ...any) {}

// TestDebugServerDisabledByDefault pins the safety default: profiling exposes
// goroutine stacks and heap contents, so an unset address must not listen.
func TestDebugServerDisabledByDefault(t *testing.T) {
	for _, addr := range []string{"", "   "} {
		srv, err := startDebugServer(addr, nil)
		if err != nil {
			t.Fatalf("startDebugServer(%q) error = %v, want nil", addr, err)
		}
		if srv != nil {
			_ = srv.Shutdown(context.Background())
			t.Fatalf("startDebugServer(%q) started a listener", addr)
		}
	}
	// Shutdown on the disabled (nil) server must be a no-op, not a panic.
	var nilServer *debugServer
	if err := nilServer.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown on a disabled server = %v, want nil", err)
	}
}

func TestDebugServerServesPprof(t *testing.T) {
	srv, err := startDebugServer("127.0.0.1:0", nil)
	if err != nil {
		t.Fatalf("startDebugServer: %v", err)
	}
	if srv == nil {
		t.Fatal("startDebugServer returned no server for a configured address")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	resp, err := http.Get("http://" + srv.addr + "/debug/pprof/heap?debug=1")
	if err != nil {
		t.Fatalf("GET heap profile: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heap profile status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		t.Fatalf("read heap profile: %v", err)
	}
	if !strings.Contains(string(body), "heap profile") {
		t.Fatalf("response does not look like a heap profile: %.100q", body)
	}
}

// TestDebugServerWarnsOnNonLoopback makes the exposure explicit in the log
// rather than silent.
func TestDebugServerWarnsOnNonLoopback(t *testing.T) {
	logger := &recordingLogger{}
	srv, err := startDebugServer("0.0.0.0:0", logger)
	if err != nil {
		t.Skipf("cannot bind 0.0.0.0 in this environment: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	if len(logger.warns) == 0 {
		t.Error("binding a non-loopback address produced no warning")
	}

	loopbackLogger := &recordingLogger{}
	loopback, err := startDebugServer("127.0.0.1:0", loopbackLogger)
	if err != nil {
		t.Fatalf("startDebugServer on loopback: %v", err)
	}
	defer func() { _ = loopback.Shutdown(context.Background()) }()
	if len(loopbackLogger.warns) != 0 {
		t.Errorf("loopback bind warned unnecessarily: %v", loopbackLogger.warns)
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	for _, tt := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:6060", true},
		{"localhost:6060", true},
		{"[::1]:6060", true},
		{"0.0.0.0:6060", false},
		{"192.168.1.10:6060", false},
		{":6060", false}, // a bare port binds every interface
		{"", false},
	} {
		if got := isLoopbackAddr(tt.addr); got != tt.want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}
