package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"
)

// debugServer serves net/http/pprof. It exists because the project had no way
// to answer "where is the time and the memory actually going" on a live
// deployment -- every performance question had to be reasoned about from the
// source, which is how plausible-but-wrong conclusions get shipped.
//
// It is off unless DebugListenAddr is set, and it is meant for a loopback or
// otherwise private address: the profiles it serves include full goroutine
// stacks and heap contents, and nothing here authenticates the caller.
type debugServer struct {
	server *http.Server
	addr   string
}

// startDebugServer starts the pprof listener when addr is non-empty. It returns
// a nil server and a nil error when profiling is disabled, so callers can treat
// "not configured" as ordinary rather than exceptional.
func startDebugServer(addr string, logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
},
) (*debugServer, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	if logger != nil && !isLoopbackAddr(addr) {
		logger.Warn("pprof is listening on a non-loopback address; it exposes goroutine stacks and heap contents without authentication",
			"addr", listener.Addr().String())
	}

	srv := &http.Server{
		Handler: mux,
		// A profile capture is a long read by design (the CPU profile defaults
		// to 30s), so only the header deadline is tightened.
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && logger != nil {
			logger.Error("pprof server stopped", "error", err)
		}
	}()
	if logger != nil {
		logger.Info("pprof listening", "addr", listener.Addr().String())
	}
	return &debugServer{server: srv, addr: listener.Addr().String()}, nil
}

// Shutdown stops the debug listener. A nil receiver is a no-op, matching the
// "profiling disabled" case.
func (d *debugServer) Shutdown(ctx context.Context) error {
	if d == nil || d.server == nil {
		return nil
	}
	return d.server.Shutdown(ctx)
}

// isLoopbackAddr reports whether addr binds only to the loopback interface. A
// bare port or an empty host binds every interface and is not loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
