package request

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetHeadersStatusAndCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			if r.Header.Get("Cookie") != "test=cookie" {
				t.Error("missing caller header")
			}
			_, _ = w.Write([]byte("ok"))
		case "/status":
			w.WriteHeader(http.StatusTooManyRequests)
		case "/cancel":
			close(started)
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	body, err := Get(context.Background(), server.Client(), server.URL+"/ok", WithHeader("Cookie", "test=cookie"))
	if err != nil || string(body) != "ok" {
		t.Fatalf("Get = %q, %v", body, err)
	}
	if _, err := Get(context.Background(), server.Client(), server.URL+"/status"); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("status error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Get(ctx, server.Client(), server.URL+"/cancel")
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("context error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt the request")
	}
}
