package kuwo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestSessionInvalidationReplacesPathScopedCookieBeforeDetailRetry(t *testing.T) {
	const (
		initialRootCookie = "abcdefghijklmnop"
		stalePathCookie   = "qrstuvwxyzABCDEF"
		freshRootCookie   = "ZYXWVUTSRQPONMLK"
	)

	homeCalls, detailCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			homeCalls++
			cookie := initialRootCookie
			if homeCalls == 2 {
				cookie = freshRootCookie
			}
			http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: cookie, Path: "/"})
		case "/api/www/music/musicInfo":
			detailCalls++
			secret := r.Header.Get("Secret")
			wantCookie := initialRootCookie
			if detailCalls == 2 {
				wantCookie = freshRootCookie
				if strings.Contains(r.Header.Get("Cookie"), stalePathCookie) {
					t.Errorf("retry Cookie header retained stale path cookie: %q", r.Header.Get("Cookie"))
				}
			}
			wantSecretPrefix := buildSecret(wantCookie, 10000000)
			wantSecretPrefix = wantSecretPrefix[:len(wantSecretPrefix)-8]
			if !strings.HasPrefix(secret, wantSecretPrefix) {
				t.Errorf("detail Secret = %q, want prefix derived from %q", secret, wantCookie)
			}
			if detailCalls == 1 {
				// No Path means cookiejar scopes this to /api/www/music.
				http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: stalePathCookie})
				_, _ = w.Write([]byte(`{"msg":"The request is illegal!"}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"rid":"41378936","name":"Song"}}`))
		default:
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		home:   server.URL + "/",
		search: server.URL + "/search",
		detail: server.URL + "/api/www/music/musicInfo",
	})
	track, err := client.GetTrack(context.Background(), "41378936")
	if err != nil {
		t.Fatalf("GetTrack() = %v", err)
	}
	if track == nil || track.ID != "41378936" {
		t.Fatalf("GetTrack() = %#v, want requested track", track)
	}
	if homeCalls != 2 || detailCalls != 2 {
		t.Fatalf("calls home=%d detail=%d, want 2 each", homeCalls, detailCalls)
	}
}

func TestSessionInvalidationKeepsSignedRequestCookieAndSecretFromOneSnapshot(t *testing.T) {
	const sessionCookie = "abcdefghijklmnop"

	type observedRequest struct {
		cookie string
		secret string
	}
	requests := make(chan observedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		requests <- observedRequest{cookie: r.Header.Get("Cookie"), secret: r.Header.Get("Secret")}
		_, _ = w.Write([]byte(`{"data":{"list":[]}}`))
	}))
	defer server.Close()

	jar := &pauseOnSecondCookieReadJar{
		cookie:   &http.Cookie{Name: kuwoSessionCookie, Value: sessionCookie},
		selected: make(chan struct{}),
		release:  make(chan struct{}),
	}
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{home: server.URL + "/", search: server.URL + "/search", detail: server.URL + "/detail"})
	client.clientMu.Lock()
	client.apiHTTPClient.Jar = jar
	client.clientMu.Unlock()
	client.sessionMu.Lock()
	client.sessionExpires = time.Now().Add(sessionTTL)
	client.sessionMu.Unlock()

	searchResult := make(chan error, 1)
	go func() {
		_, err := client.Search(context.Background(), "test", 1)
		searchResult <- err
	}()
	<-jar.selected

	invalidated := make(chan struct{})
	go func() {
		client.invalidateSession(server.URL + "/search")
		close(invalidated)
	}()

	// A coherent client/Jar snapshot keeps invalidation from replacing the jar
	// until its cookie has been selected. The pre-fix implementation lets this
	// complete here and later sends through the replacement client.
	select {
	case <-invalidated:
	case <-time.After(100 * time.Millisecond):
	}
	close(jar.release)

	if err := <-searchResult; err != nil {
		t.Fatalf("Search() = %v", err)
	}
	select {
	case got := <-requests:
		if !strings.Contains(got.cookie, kuwoSessionCookie+"="+sessionCookie) {
			t.Errorf("request Cookie = %q, want the cookie used to build Secret", got.cookie)
		}
		wantPrefix := buildSecret(sessionCookie, 10000000)
		wantPrefix = wantPrefix[:len(wantPrefix)-8]
		if !strings.HasPrefix(got.secret, wantPrefix) {
			t.Errorf("request Secret = %q, want prefix derived from %q", got.secret, sessionCookie)
		}
	case <-time.After(time.Second):
		t.Fatal("signed request did not reach the API")
	}
	select {
	case <-invalidated:
	case <-time.After(time.Second):
		t.Fatal("session invalidation did not complete")
	}
}

func TestSessionBindsCookieSnapshotAndWritesResponseCookies(t *testing.T) {
	const (
		snapshotCookie = "abcdefghijklmnop"
		rotatedCookie  = "qrstuvwxyzABCDEF"
		responseCookie = "ponmlkjihgfedcba"
	)

	type observedRequest struct {
		cookie string
		secret string
	}
	requests := make(chan observedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		requests <- observedRequest{cookie: r.Header.Get("Cookie"), secret: r.Header.Get("Secret")}
		http.SetCookie(w, &http.Cookie{Name: kuwoSessionCookie, Value: responseCookie, Path: "/"})
		_, _ = w.Write([]byte(`{"data":{"list":[]}}`))
	}))
	defer server.Close()

	jar := &snapshotThenRotatedJar{
		current: &http.Cookie{Name: kuwoSessionCookie, Value: snapshotCookie},
		rotated: &http.Cookie{Name: kuwoSessionCookie, Value: rotatedCookie},
	}
	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{home: server.URL + "/", search: server.URL + "/search", detail: server.URL + "/detail"})
	client.clientMu.Lock()
	client.apiHTTPClient.Jar = jar
	client.clientMu.Unlock()
	client.sessionMu.Lock()
	client.sessionExpires = time.Now().Add(sessionTTL)
	client.sessionMu.Unlock()

	if _, err := client.Search(context.Background(), "test", 1); err != nil {
		t.Fatalf("Search() = %v", err)
	}
	select {
	case got := <-requests:
		if !strings.Contains(got.cookie, kuwoSessionCookie+"="+snapshotCookie) {
			t.Errorf("request Cookie = %q, want snapshot cookie", got.cookie)
		}
		if strings.Contains(got.cookie, kuwoSessionCookie+"="+rotatedCookie) {
			t.Errorf("request Cookie = %q, must not re-read rotated cookie", got.cookie)
		}
		wantPrefix := buildSecret(snapshotCookie, 10000000)
		wantPrefix = wantPrefix[:len(wantPrefix)-8]
		if !strings.HasPrefix(got.secret, wantPrefix) {
			t.Errorf("request Secret = %q, want prefix derived from snapshot cookie", got.secret)
		}
	case <-time.After(time.Second):
		t.Fatal("signed request did not reach the API")
	}
	if got := client.sessionCookie(server.URL + "/search"); got != responseCookie {
		t.Errorf("session cookie after response = %q, want response Set-Cookie value %q", got, responseCookie)
	}
}

type snapshotThenRotatedJar struct {
	mu sync.Mutex

	current *http.Cookie
	rotated *http.Cookie
	reads   int
	sets    int
}

func (j *snapshotThenRotatedJar) SetCookies(_ *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.sets++
	for _, cookie := range cookies {
		if cookie.Name == kuwoSessionCookie {
			copy := *cookie
			j.current = &copy
		}
	}
}

func (j *snapshotThenRotatedJar) Cookies(_ *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.reads++
	if j.sets == 0 && j.reads == 3 {
		return []*http.Cookie{j.rotated}
	}
	return []*http.Cookie{j.current}
}

type pauseOnSecondCookieReadJar struct {
	cookie   *http.Cookie
	selected chan struct{}
	release  chan struct{}

	mu    sync.Mutex
	reads int
}

func (j *pauseOnSecondCookieReadJar) SetCookies(_ *url.URL, _ []*http.Cookie) {}

func (j *pauseOnSecondCookieReadJar) Cookies(_ *url.URL) []*http.Cookie {
	j.mu.Lock()
	j.reads++
	read := j.reads
	j.mu.Unlock()
	if read == 2 {
		close(j.selected)
		<-j.release
	}
	return []*http.Cookie{j.cookie}
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
