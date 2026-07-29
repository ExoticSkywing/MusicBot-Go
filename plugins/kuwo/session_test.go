package kuwo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildSecretFixedVector(t *testing.T) {
	got := buildSecret("0123456789abcdef0123456789abcdef", 12345678)
	const want = "1361b99125125e1ce61cc1f328ded44b38fec3e403cfaa3031b91afbe5e200ea00bc614e"
	if got != want {
		t.Fatalf("buildSecret() = %q, want %q", got, want)
	}
}

func TestSessionRejectsInvalidHomepageCookies(t *testing.T) {
	for _, cookie := range []string{"", "short", strings.Repeat("a", 129), "abcdefghijklmnop-"} {
		t.Run(cookie, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: cookie, Path: "/"})
			}))
			defer server.Close()

			client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{home: server.URL, search: server.URL, detail: server.URL})
			if err := client.ensureSession(context.Background()); err == nil {
				t.Fatal("ensureSession() unexpectedly accepted invalid cookie")
			}
		})
	}
}

func TestSessionCookieValidationRejectsNonASCIIAndControlCharacters(t *testing.T) {
	for _, value := range []string{"abcdefghijklmnoé", "abcdefghijklmnop\n"} {
		if validSessionCookie(value) {
			t.Fatalf("validSessionCookie(%q) unexpectedly accepted an invalid character", value)
		}
	}
}

func TestSessionCachesCookieAndRefreshesOnce(t *testing.T) {
	var mu sync.Mutex
	homeCalls, searchCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/" {
			homeCalls++
			http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: "abcdefghijklmnop", Path: "/"})
			return
		}
		searchCalls++
		if searchCalls == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"list":[]}}`))
	}))
	defer server.Close()

	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{home: server.URL + "/", search: server.URL + "/search", detail: server.URL + "/detail"})
	if err := client.ensureSession(context.Background()); err != nil {
		t.Fatalf("first ensureSession() = %v", err)
	}
	if err := client.ensureSession(context.Background()); err != nil {
		t.Fatalf("cached ensureSession() = %v", err)
	}
	mu.Lock()
	if homeCalls != 1 {
		mu.Unlock()
		t.Fatalf("cached session homepage calls = %d, want 1", homeCalls)
	}
	mu.Unlock()
	if _, err := client.Search(context.Background(), "test", 1); err != nil {
		t.Fatalf("Search() = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if homeCalls != 2 || searchCalls != 2 {
		t.Fatalf("calls home=%d search=%d, want 2 each", homeCalls, searchCalls)
	}
}

func TestSessionRefreshesOnceForEveryInvalidResponseSignal(t *testing.T) {
	for _, response := range []struct {
		name      string
		status    int
		body      string
		permanent bool
	}{
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "forbidden", status: http.StatusForbidden},
		{name: "illegal body", status: http.StatusOK, body: `{"msg":"The request is illegal!"}`},
		{name: "permanently illegal body", status: http.StatusOK, body: `{"msg":"The request is illegal!"}`, permanent: true},
	} {
		t.Run(response.name, func(t *testing.T) {
			homeCalls, apiCalls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/" {
					homeCalls++
					http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: "abcdefghijklmnop", Path: "/"})
					return
				}
				apiCalls++
				if apiCalls == 1 || response.permanent {
					w.WriteHeader(response.status)
					_, _ = w.Write([]byte(response.body))
					return
				}
				_, _ = w.Write([]byte(`{"data":{"list":[]}}`))
			}))
			defer server.Close()
			client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{home: server.URL + "/", search: server.URL + "/search", detail: server.URL + "/detail"})
			_, err := client.Search(context.Background(), "test", 1)
			if response.permanent {
				if err == nil {
					t.Fatal("Search() unexpectedly accepted a permanently illegal response")
				}
			} else if err != nil {
				t.Fatalf("Search() = %v", err)
			}
			if homeCalls != 2 || apiCalls != 2 {
				t.Fatalf("calls home=%d api=%d, want 2 each", homeCalls, apiCalls)
			}
		})
	}
}

func TestSessionSignsEveryRequestWithLatestCookie(t *testing.T) {
	var mu sync.Mutex
	var secrets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: "abcdefghijklmnop", Path: "/"})
			return
		}
		mu.Lock()
		secrets = append(secrets, r.Header.Get("Secret"))
		call := len(secrets)
		mu.Unlock()
		if call == 1 {
			http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: "qrstuvwxyzABCDEF", Path: "/search"})
		}
		_, _ = w.Write([]byte(`{"data":{"list":[]}}`))
	}))
	defer server.Close()

	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{home: server.URL + "/", search: server.URL + "/search", detail: server.URL + "/detail"})
	for range 2 {
		if _, err := client.Search(context.Background(), "test", 1); err != nil {
			t.Fatalf("Search() = %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(secrets) != 2 {
		t.Fatalf("signed requests = %d, want 2", len(secrets))
	}
	for i, wantCookie := range []string{"abcdefghijklmnop", "qrstuvwxyzABCDEF"} {
		want := buildSecret(wantCookie, 10000000)
		wantPrefix := want[:len(want)-8]
		if len(secrets[i]) != len(want) || !strings.HasPrefix(secrets[i], wantPrefix) {
			t.Errorf("Secret[%d] = %q, want cookie-derived prefix %q", i, secrets[i], wantPrefix)
		}
		nonce, err := strconv.ParseInt(secrets[i][len(secrets[i])-8:], 16, 64)
		if err != nil || nonce < 10000000 || nonce >= 100000000 {
			t.Errorf("Secret[%d] nonce = %q, want encoded eight-digit decimal nonce", i, secrets[i][len(secrets[i])-8:])
		}
	}
}

func TestSessionWaiterReturnsWhenRefreshContextIsCancelled(t *testing.T) {
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			t.Fatal("unexpected API request")
		}
		select {
		case <-refreshStarted:
		default:
			close(refreshStarted)
		}
		<-releaseRefresh
		http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: "abcdefghijklmnop", Path: "/"})
	}))
	defer server.Close()
	var releaseOnce sync.Once
	defer func() { releaseOnce.Do(func() { close(releaseRefresh) }) }()

	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{home: server.URL + "/", search: server.URL + "/search", detail: server.URL + "/detail"})
	leaderErr := make(chan error, 1)
	go func() { leaderErr <- client.ensureSession(context.Background()) }()
	<-refreshStarted

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	waiterErr := make(chan error, 1)
	go func() { waiterErr <- client.ensureSession(ctx) }()
	select {
	case err := <-waiterErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter error = %v, want context cancellation", err)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("cancelled waiter did not return while a refresh was in progress")
	}
	releaseOnce.Do(func() { close(releaseRefresh) })
	if err := <-leaderErr; err != nil {
		t.Fatalf("refresh leader error = %v", err)
	}
}

func TestSessionConcurrentUseIsRaceSafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: "abcdefghijklmnop", Path: "/"})
			return
		}
		_, _ = w.Write([]byte(`{"data":{"list":[]}}`))
	}))
	defer server.Close()

	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{home: server.URL + "/", search: server.URL + "/search", detail: server.URL + "/detail"})
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.Search(context.Background(), "concurrent", 1); err != nil {
				t.Errorf("Search() = %v", err)
			}
		}()
	}
	wg.Wait()
}
