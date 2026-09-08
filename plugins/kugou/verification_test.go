package kugou

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guohuiyuan/music-lib/model"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const (
	testVerificationRelayID   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testVerificationPublicKey = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var _ platform.VerificationTestProvider = (*KugouPlatform)(nil)

type fakeConceptVerificationRelay struct {
	mu               sync.Mutex
	createCalls      int
	pollCalls        int
	completeStatuses []string
	completeFailures int
	result           conceptVerificationRelayResult
	createStarted    chan struct{}
	createOnce       sync.Once
	blockCreate      bool
}

func newFakeConceptVerificationRelay() *fakeConceptVerificationRelay {
	return &fakeConceptVerificationRelay{
		result:        conceptVerificationRelayResult{Status: "pending"},
		createStarted: make(chan struct{}),
	}
}

func (r *fakeConceptVerificationRelay) Create(ctx context.Context, appID string, ttl time.Duration) (conceptVerificationRelayChallenge, error) {
	r.mu.Lock()
	r.createCalls++
	blocked := r.blockCreate
	r.mu.Unlock()
	r.createOnce.Do(func() { close(r.createStarted) })
	if appID != "123456789" || ttl != conceptVerificationTTL {
		return conceptVerificationRelayChallenge{}, errConceptVerificationRelay
	}
	if blocked {
		<-ctx.Done()
		return conceptVerificationRelayChallenge{}, ctx.Err()
	}
	return conceptVerificationRelayChallenge{
		ID:        testVerificationRelayID,
		URL:       "https://relay.example/v/" + testVerificationPublicKey,
		ExpiresAt: time.Now().Add(conceptVerificationTTL - time.Second),
	}, nil
}

func (r *fakeConceptVerificationRelay) Poll(context.Context, string) (conceptVerificationRelayResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pollCalls++
	return r.result, nil
}

func (r *fakeConceptVerificationRelay) Complete(_ context.Context, _ string, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completeStatuses = append(r.completeStatuses, status)
	if r.completeFailures > 0 {
		r.completeFailures--
		return errConceptVerificationRelay
	}
	return nil
}

func (r *fakeConceptVerificationRelay) setResult(result conceptVerificationRelayResult) {
	r.mu.Lock()
	r.result = result
	r.mu.Unlock()
}

func (r *fakeConceptVerificationRelay) counts() (int, int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.createCalls, r.pollCalls, append([]string(nil), r.completeStatuses...)
}

func attachFakeVerificationRelay(t *testing.T, mgr *ConceptSessionManager, relay *fakeConceptVerificationRelay) {
	t.Helper()
	if err := mgr.verification.setRelay(relay); err != nil {
		t.Fatalf("setRelay() error = %v", err)
	}
	mgr.verification.pollInterval = 5 * time.Millisecond
}

func verificationTestHTTPClient(t *testing.T, handler func(*http.Request) (string, http.Header)) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, header := handler(req)
		if header == nil {
			header = make(http.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

func verificationInfoResponse() string {
	return `{"status":1,"data":{"v_type":23,"captcha_app_id":"123456789"}}`
}

func verificationRequiredResponse() (string, http.Header) {
	header := make(http.Header)
	header.Set("ssa-code", "private-event-id")
	return `{"status":0,"errcode":20028,"error":"需要验证"}`, header
}

func TestVerificationRelayDisabledByEmptyConfig(t *testing.T) {
	mgr := newTestConceptSessionManager("stable-dfid")
	if err := mgr.SetVerificationRelay("", ""); err != nil {
		t.Fatalf("SetVerificationRelay(empty) error = %v", err)
	}
	mgr.verification.mu.Lock()
	relay := mgr.verification.relay
	mgr.verification.mu.Unlock()
	if relay != nil {
		t.Fatal("empty relay configuration must remain disabled")
	}
}

func TestVerificationRelayConfigurationValidation(t *testing.T) {
	secret := strings.Repeat("s", 32)
	tests := []struct {
		name   string
		url    string
		secret string
	}{
		{name: "missing secret", url: "https://relay.example"},
		{name: "missing URL", secret: secret},
		{name: "insecure URL", url: "http://relay.example", secret: secret},
		{name: "short secret", url: "https://relay.example", secret: "too-short"},
		{name: "URL credentials", url: "https://user@relay.example", secret: secret},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if relay, err := newHTTPConceptVerificationRelay(tt.url, tt.secret, nil); err == nil || relay != nil {
				t.Fatalf("newHTTPConceptVerificationRelay() = %#v, %v; want configuration error", relay, err)
			}
		})
	}
}

func TestStartVerificationTestUsesRelayOnly(t *testing.T) {
	var relayCalls atomic.Int32
	var providerCalls atomic.Int32
	expiresAt := time.Now().Add(conceptVerificationTTL).Unix()
	relayClient := verificationTestHTTPClient(t, func(req *http.Request) (string, http.Header) {
		relayCalls.Add(1)
		if req.Method != http.MethodPost || req.URL.String() != "https://relay.example/api/challenges" {
			t.Fatalf("relay request = %s %s", req.Method, req.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("decode relay request: %v", err)
		}
		if len(body) != 3 || body["captcha_app_id"] != conceptVerificationTestAppID || body["expires_in"] != float64(600) || body["test_mode"] != true {
			t.Fatalf("relay request body = %#v", body)
		}
		return `{"id":"` + testVerificationRelayID + `","url":"https://relay.example/v/` + testVerificationPublicKey + `","expires_at":` + strconvFormatUnix(expiresAt) + `}`, nil
	})
	relay, err := newHTTPConceptVerificationRelay("https://relay.example", strings.Repeat("s", 32), relayClient)
	if err != nil {
		t.Fatalf("construct relay: %v", err)
	}
	mgr := newTestConceptSessionManager("stable-dfid")
	mgr.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		providerCalls.Add(1)
		return nil, errors.New("provider must not be called")
	})})
	if err := mgr.verification.setRelay(relay); err != nil {
		t.Fatalf("set relay: %v", err)
	}
	before := mgr.Snapshot()
	client := NewClient("", nil)
	client.AttachConcept(mgr)
	result, err := NewPlatform(client).StartVerificationTest(context.Background())
	if err != nil {
		t.Fatalf("StartVerificationTest() error = %v", err)
	}
	if result.URL != "https://relay.example/v/"+testVerificationPublicKey || result.ExpiresAt.Unix() != expiresAt {
		t.Fatalf("StartVerificationTest() = %+v", result)
	}
	if relayCalls.Load() != 1 || providerCalls.Load() != 0 {
		t.Fatalf("relay/provider calls = %d/%d, want 1/0", relayCalls.Load(), providerCalls.Load())
	}
	if after := mgr.Snapshot(); !reflect.DeepEqual(after, before) {
		t.Fatalf("verification test changed session\n got: %#v\nwant: %#v", after, before)
	}
	mgr.verification.mu.Lock()
	active := mgr.verification.active
	mgr.verification.mu.Unlock()
	if active != nil {
		t.Fatal("verification test created an active coordinator attempt")
	}
	_ = mgr.Close()
}

func TestStartVerificationTestFailsGenericallyWhenUnavailable(t *testing.T) {
	var nilPlatform *KugouPlatform
	client := NewClient("", nil)
	client.AttachConcept(newTestConceptSessionManager("stable-dfid"))
	disabledPlatform := NewPlatform(client)
	for name, provider := range map[string]*KugouPlatform{
		"nil":      nilPlatform,
		"disabled": disabledPlatform,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := provider.StartVerificationTest(context.Background())
			if !errors.Is(err, errConceptVerificationFlow) {
				t.Fatalf("StartVerificationTest() error = %v", err)
			}
		})
	}
	_ = client.Concept().Close()
}

func TestSupportedLoginMethodsIncludesVerificationTest(t *testing.T) {
	for _, method := range (&KugouPlatform{}).SupportedLoginMethods() {
		if method == "verify-test" {
			return
		}
	}
	t.Fatal("SupportedLoginMethods() missing verify-test")
}

func TestRetryVerificationPlaybackPreservesEffectiveV5AlbumID(t *testing.T) {
	tests := []struct {
		name    string
		albumID string
		extra   map[string]string
		want    string
	}{
		{name: "extra overrides model", albumID: "model-album", extra: map[string]string{"album_id": "extra-album"}, want: "extra-album"},
		{name: "extra only", extra: map[string]string{"album_id": "extra-only"}, want: "extra-only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := newTestConceptSessionManager("stable-dfid")
			mgr.SetHTTPClient(verificationTestHTTPClient(t, func(req *http.Request) (string, http.Header) {
				if req.URL.Path != "/v5/url" {
					t.Fatalf("unexpected path: %s", req.URL.Path)
				}
				if got := req.URL.Query().Get("album_id"); got != tt.want {
					t.Fatalf("replayed album_id = %q, want %q", got, tt.want)
				}
				return `{"status":1,"url":["https://media.example/song.mp3"],"extName":"mp3"}`, nil
			}))
			song := &model.Song{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AlbumID: tt.albumID, Extra: tt.extra}
			challenge := newConceptVerificationChallenge("event", mgr.Snapshot(), song, kugouDownloadPlan{Hash: song.ID, Quality: platform.QualityStandard}, conceptVerificationPlaybackV5)
			if challenge == nil || challenge.Playback.AlbumID != tt.want {
				t.Fatalf("captured playback = %#v, want album_id %q", challenge, tt.want)
			}
			if err := mgr.client.retryVerificationPlayback(context.Background(), challenge.Session, challenge.Playback); err != nil {
				t.Fatalf("retryVerificationPlayback() error = %v", err)
			}
			_ = mgr.Close()
		})
	}
}

func TestRetryVerificationPlaybackRejectsV6DataWithoutPlayableURL(t *testing.T) {
	mgr := newTestConceptSessionManager("stable-dfid")
	mgr.SetHTTPClient(verificationTestHTTPClient(t, func(req *http.Request) (string, http.Header) {
		if req.URL.Path != "/v6/priv_url" {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		return `{"status":1,"data":[{"extname":"mp3"}]}`, nil
	}))
	playback := conceptVerificationPlayback{
		Endpoint: conceptVerificationPlaybackV6,
		SongID:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Plan:     kugouDownloadPlan{Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Quality: platform.QualityStandard, Format: "mp3"},
	}
	if err := mgr.client.retryVerificationPlayback(context.Background(), mgr.Snapshot(), playback); !errors.Is(err, errConceptVerificationFlow) {
		t.Fatalf("retryVerificationPlayback() error = %v, want unusable response", err)
	}
	_ = mgr.Close()
}

func TestVerificationCoordinatorCoalescesBeforeDeviceRegistration(t *testing.T) {
	const workers = 8
	var registerCalls atomic.Int32
	var verificationInfoCalls atomic.Int32
	mgr := newTestConceptSessionManager("stable-dfid")
	relay := newFakeConceptVerificationRelay()
	attachFakeVerificationRelay(t, mgr, relay)
	mgr.SetHTTPClient(verificationTestHTTPClient(t, func(req *http.Request) (string, http.Header) {
		switch req.URL.Path {
		case "/v5/url":
			return verificationRequiredResponse()
		case "/verifyservice/v3/get_verify_info":
			verificationInfoCalls.Add(1)
			return verificationInfoResponse(), nil
		case "/risk/v2/r_register_dev":
			registerCalls.Add(1)
			return `{"status":1,"data":{"dfid":"must-not-register"}}`, nil
		default:
			t.Errorf("unexpected path: %s", req.URL.Path)
			return `{"status":0}`, nil
		}
	}))
	client := NewClient("", nil)
	client.AttachConcept(mgr)

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			song := &model.Song{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Extra: map[string]string{}}
			_, err := client.ResolveDownloadByQuality(context.Background(), song, platform.QualityStandard)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		var required *platform.VerificationRequiredError
		if !errors.As(err, &required) {
			t.Fatalf("ResolveDownloadByQuality() error = %v, want verification requirement", err)
		}
		if required.URL != "https://relay.example/v/"+testVerificationPublicKey {
			t.Fatalf("verification URL = %q", required.URL)
		}
	}
	createCalls, _, _ := relay.counts()
	if createCalls != 1 || verificationInfoCalls.Load() != 1 {
		t.Fatalf("create/info calls = %d/%d, want 1/1", createCalls, verificationInfoCalls.Load())
	}
	if registerCalls.Load() != 0 {
		t.Fatalf("register calls = %d, want 0", registerCalls.Load())
	}
	if got := mgr.Snapshot().Device.Dfid; got != "stable-dfid" {
		t.Fatalf("device changed to %q", got)
	}
	_ = mgr.Close()
}

func TestVerificationCoordinatorSubmitsOnceRetriesCompletionAndPlayback(t *testing.T) {
	var submitCalls atomic.Int32
	var playbackCalls atomic.Int32
	var providerAccepted atomic.Bool
	mgr := newTestConceptSessionManager("stable-dfid")
	relay := newFakeConceptVerificationRelay()
	relay.completeFailures = 1
	attachFakeVerificationRelay(t, mgr, relay)
	mgr.SetHTTPClient(verificationTestHTTPClient(t, func(req *http.Request) (string, http.Header) {
		switch req.URL.Path {
		case "/v5/url":
			playbackCalls.Add(1)
			if providerAccepted.Load() {
				return `{"status":1,"url":["https://media.example/song.mp3"],"extName":"mp3"}`, nil
			}
			return verificationRequiredResponse()
		case "/verifyservice/v3/get_verify_info":
			return `{"status":1,"data":{"url":"KGCodeTX|123456789"}}`, nil
		case "/v4/verify_user_info":
			submitCalls.Add(1)
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("decode verification submission: %v", err)
			}
			if body["v_type"] != float64(23) {
				t.Errorf("v_type = %#v", body["v_type"])
			}
			verifyCode, _ := body["verifycode"].(string)
			var ticket map[string]string
			if !strings.HasPrefix(verifyCode, "KGCodeTX|") || json.Unmarshal([]byte(strings.TrimPrefix(verifyCode, "KGCodeTX|")), &ticket) != nil || ticket["txappid"] != "123456789" {
				t.Errorf("invalid verifycode")
			}
			providerAccepted.Store(true)
			return `{"status":1,"errcode":0,"error_code":0}`, nil
		default:
			t.Errorf("unexpected path: %s", req.URL.Path)
			return `{"status":0}`, nil
		}
	}))
	client := NewClient("", nil)
	client.AttachConcept(mgr)
	song := &model.Song{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Extra: map[string]string{}}
	_, err := client.ResolveDownloadByQuality(context.Background(), song, platform.QualityStandard)
	var required *platform.VerificationRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("ResolveDownloadByQuality() error = %v, want verification requirement", err)
	}
	relay.setResult(conceptVerificationRelayResult{
		Status: "submitted",
		Proof: conceptVerificationProof{
			Ticket: "human-ticket", RandStr: "human-rand", SID: "browser-sid", EDT: "browser-evidence",
		},
	})
	waitForVerificationCompletion(t, relay, "verified", 2)
	if submitCalls.Load() != 1 {
		t.Fatalf("provider submissions = %d, want 1", submitCalls.Load())
	}
	if playbackCalls.Load() != 2 {
		t.Fatalf("playback calls = %d, want initial challenge plus one retry", playbackCalls.Load())
	}
	_ = mgr.Close()
}

func TestVerificationCoordinatorExpiresWhenSessionChanges(t *testing.T) {
	var submitCalls atomic.Int32
	mgr := newTestConceptSessionManager("stable-dfid")
	relay := newFakeConceptVerificationRelay()
	attachFakeVerificationRelay(t, mgr, relay)
	mgr.SetHTTPClient(verificationTestHTTPClient(t, func(req *http.Request) (string, http.Header) {
		switch req.URL.Path {
		case "/v5/url":
			return verificationRequiredResponse()
		case "/verifyservice/v3/get_verify_info":
			return verificationInfoResponse(), nil
		case "/v4/verify_user_info":
			submitCalls.Add(1)
			return `{"status":1}`, nil
		default:
			t.Errorf("unexpected path: %s", req.URL.Path)
			return `{"status":0}`, nil
		}
	}))
	client := NewClient("", nil)
	client.AttachConcept(mgr)
	_, err := client.ResolveDownloadByQuality(context.Background(), &model.Song{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Extra: map[string]string{}}, platform.QualityStandard)
	var required *platform.VerificationRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("ResolveDownloadByQuality() error = %v, want verification requirement", err)
	}
	mgr.Update(func(state *conceptSession) { state.Token = "replacement-token" })
	relay.setResult(conceptVerificationRelayResult{Status: "submitted", Proof: conceptVerificationProof{Ticket: "ticket", RandStr: "rand", SID: "sid", EDT: "edt"}})
	waitForVerificationCompletion(t, relay, "expired", 1)
	if submitCalls.Load() != 0 {
		t.Fatalf("stale provider submissions = %d, want 0", submitCalls.Load())
	}
	_ = mgr.Close()
}

func TestVerificationCoordinatorCloseCancelsBlockedSetup(t *testing.T) {
	mgr := newTestConceptSessionManager("stable-dfid")
	relay := newFakeConceptVerificationRelay()
	relay.blockCreate = true
	attachFakeVerificationRelay(t, mgr, relay)
	mgr.SetHTTPClient(verificationTestHTTPClient(t, func(req *http.Request) (string, http.Header) {
		if req.URL.Path == "/v5/url" {
			return verificationRequiredResponse()
		}
		if req.URL.Path == "/verifyservice/v3/get_verify_info" {
			return verificationInfoResponse(), nil
		}
		t.Errorf("unexpected path: %s", req.URL.Path)
		return `{"status":0}`, nil
	}))
	client := NewClient("", nil)
	client.AttachConcept(mgr)
	resolved := make(chan error, 1)
	go func() {
		_, err := client.ResolveDownloadByQuality(context.Background(), &model.Song{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Extra: map[string]string{}}, platform.QualityStandard)
		resolved <- err
	}()
	select {
	case <-relay.createStarted:
	case <-time.After(time.Second):
		t.Fatal("relay setup did not start")
	}
	closed := make(chan struct{})
	go func() {
		_ = mgr.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close() did not cancel blocked setup")
	}
	select {
	case err := <-resolved:
		if !errors.Is(err, errConceptDeviceVerification) {
			t.Fatalf("ResolveDownloadByQuality() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked request did not return")
	}
}

func TestHTTPVerificationRelayContractIsIsolated(t *testing.T) {
	secret := strings.Repeat("s", 32)
	var serverURL string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer "+secret {
			t.Errorf("authorization header missing")
		}
		if req.Header.Get("User-Agent") != "MusicBot-Verification/1.0" {
			t.Errorf("User-Agent = %q", req.Header.Get("User-Agent"))
		}
		for _, header := range []string{"Cookie", "dfid", "mid"} {
			if req.Header.Get(header) != "" {
				t.Errorf("relay received forbidden %s header", header)
			}
		}
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/api/challenges":
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			if len(body) != 2 || body["captcha_app_id"] != "123456789" || body["expires_in"] != float64(600) {
				t.Errorf("create payload = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": testVerificationRelayID, "url": serverURL + "/v/" + testVerificationPublicKey, "expires_at": time.Now().Unix() + 600})
		case req.Method == http.MethodGet && req.URL.Path == "/api/challenges/"+testVerificationRelayID:
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "submitted", "proof": map[string]string{"ticket": "ticket", "randstr": "rand", "sid": "sid", "edt": "edt"}})
		case req.Method == http.MethodPost && req.URL.Path == "/api/challenges/"+testVerificationRelayID+"/complete":
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			if len(body) != 1 || body["status"] != "verified" {
				t.Errorf("complete payload = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "verified"})
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	serverURL = server.URL
	jar, _ := cookiejar.New(nil)
	parsedServerURL, _ := url.Parse(server.URL)
	jar.SetCookies(parsedServerURL, []*http.Cookie{{Name: "provider-token", Value: "must-not-forward"}})
	seedClient := server.Client()
	seedClient.Jar = jar
	relay, err := newHTTPConceptVerificationRelay(server.URL, secret, seedClient)
	if err != nil {
		t.Fatalf("newHTTPConceptVerificationRelay() error = %v", err)
	}
	if seedClient.Jar != jar {
		t.Fatal("relay constructor mutated supplied HTTP client")
	}
	handle, err := relay.Create(context.Background(), "123456789", conceptVerificationTTL)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if handle.ID != testVerificationRelayID || handle.URL != server.URL+"/v/"+testVerificationPublicKey {
		t.Fatalf("Create() = %+v", handle)
	}
	result, err := relay.Poll(context.Background(), handle.ID)
	if err != nil || result.Status != "submitted" || result.Proof.Ticket != "ticket" {
		t.Fatalf("Poll() = %+v, %v", result, err)
	}
	if err := relay.Complete(context.Background(), handle.ID, "verified"); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
}

func TestHTTPVerificationRelayRejectsUnsafeResponses(t *testing.T) {
	secret := strings.Repeat("s", 32)
	tests := []struct {
		name string
		body func(string) string
	}{
		{name: "cross origin", body: func(string) string {
			return `{"id":"` + testVerificationRelayID + `","url":"https://evil.example/v/` + testVerificationPublicKey + `","expires_at":` + strconvFormatUnix(time.Now().Unix()+600) + `}`
		}},
		{name: "query capability", body: func(base string) string {
			return `{"id":"` + testVerificationRelayID + `","url":"` + base + `/v/` + testVerificationPublicKey + `?leak=1","expires_at":` + strconvFormatUnix(time.Now().Unix()+600) + `}`
		}},
		{name: "excessive expiry", body: func(base string) string {
			return `{"id":"` + testVerificationRelayID + `","url":"` + base + `/v/` + testVerificationPublicKey + `","expires_at":` + strconvFormatUnix(time.Now().Unix()+720) + `}`
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var serverURL string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body(serverURL))
			}))
			defer server.Close()
			serverURL = server.URL
			relay, err := newHTTPConceptVerificationRelay(server.URL, secret, server.Client())
			if err != nil {
				t.Fatalf("construct relay: %v", err)
			}
			if _, err := relay.Create(context.Background(), "123456789", conceptVerificationTTL); !errors.Is(err, errConceptVerificationRelay) {
				t.Fatalf("Create() error = %v", err)
			}
			if _, err := relay.CreateTest(context.Background(), conceptVerificationTestAppID, conceptVerificationTTL); !errors.Is(err, errConceptVerificationRelay) {
				t.Fatalf("CreateTest() error = %v", err)
			}
		})
	}

	redirectTargetHits := atomic.Int32{}
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectTargetHits.Add(1) }))
	defer target.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { http.Redirect(w, req, target.URL, http.StatusFound) }))
	defer redirect.Close()
	relay, err := newHTTPConceptVerificationRelay(redirect.URL, secret, redirect.Client())
	if err != nil {
		t.Fatalf("construct redirect relay: %v", err)
	}
	if _, err := relay.Create(context.Background(), "123456789", conceptVerificationTTL); !errors.Is(err, errConceptVerificationRelay) {
		t.Fatalf("redirect Create() error = %v", err)
	}
	if redirectTargetHits.Load() != 0 {
		t.Fatal("relay followed a redirect")
	}
}

func TestHTTPVerificationRelayAllowsBoundedExpirySkew(t *testing.T) {
	secret := strings.Repeat("s", 32)
	var serverURL string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":         testVerificationRelayID,
			"url":        serverURL + "/v/" + testVerificationPublicKey,
			"expires_at": time.Now().Unix() + 601,
		})
	}))
	defer server.Close()
	serverURL = server.URL
	relay, err := newHTTPConceptVerificationRelay(server.URL, secret, server.Client())
	if err != nil {
		t.Fatalf("construct relay: %v", err)
	}
	if _, err := relay.Create(context.Background(), "123456789", conceptVerificationTTL); err != nil {
		t.Fatalf("Create() rejected bounded expiry skew: %v", err)
	}
}

func TestHTTPVerificationRelayRejectsInvalidAppIDWithoutRequest(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected request")
	})}
	relay, err := newHTTPConceptVerificationRelay("https://relay.example", strings.Repeat("s", 32), client)
	if err != nil {
		t.Fatalf("construct relay: %v", err)
	}
	if _, err := relay.Create(context.Background(), "not-public", conceptVerificationTTL); !errors.Is(err, errConceptVerificationRelay) {
		t.Fatalf("Create() error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("relay requests = %d, want 0", calls.Load())
	}
}

func TestHTTPVerificationRelayErrorsDoNotExposeResponseBody(t *testing.T) {
	const privateBody = "private-upstream-diagnostic"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, privateBody)
	}))
	defer server.Close()
	relay, err := newHTTPConceptVerificationRelay(server.URL, strings.Repeat("s", 32), server.Client())
	if err != nil {
		t.Fatalf("construct relay: %v", err)
	}
	_, err = relay.Poll(context.Background(), testVerificationRelayID)
	if !errors.Is(err, errConceptVerificationRelay) {
		t.Fatalf("Poll() error = %v", err)
	}
	if strings.Contains(err.Error(), privateBody) {
		t.Fatalf("Poll() leaked response body: %q", err)
	}
}

func TestVerificationProtocolRequiresKGCodeTX(t *testing.T) {
	if got := conceptVerificationType(map[string]json.RawMessage{"v_type": json.RawMessage(`24`)}); got != 24 {
		t.Fatalf("explicit v_type = %d", got)
	}
	if got := conceptVerificationType(map[string]json.RawMessage{"url": json.RawMessage(`"https://verify.example/start?type=KGCodeTX"`)}); got != 23 {
		t.Fatalf("KGCodeTX URL type = %d", got)
	}
	actualShape := map[string]json.RawMessage{"url": json.RawMessage(`"KGCodeTX|123456789"`)}
	if got := conceptVerificationType(actualShape); got != 23 {
		t.Fatalf("KGCodeTX descriptor type = %d", got)
	}
	if got := conceptVerificationCaptchaAppID(actualShape); got != "123456789" {
		t.Fatalf("KGCodeTX descriptor appid = %q", got)
	}
	if got := conceptVerificationType(map[string]json.RawMessage{"url": json.RawMessage(`"https://verify.example/start"`)}); got != 0 {
		t.Fatalf("missing verification type = %d, want 0", got)
	}
}

func waitForVerificationCompletion(t *testing.T, relay *fakeConceptVerificationRelay, status string, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, _, statuses := relay.counts()
		matches := 0
		for _, got := range statuses {
			if got == status {
				matches++
			}
		}
		if matches >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	_, _, statuses := relay.counts()
	t.Fatalf("completion statuses = %v, want %q at least %d times", statuses, status, count)
}

func strconvFormatUnix(value int64) string {
	return strconv.FormatInt(value, 10)
}
