package soda

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type sodaRoundTripFunc func(*http.Request) (*http.Response, error)

func (f sodaRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type sodaTimeoutError struct{}

func (sodaTimeoutError) Error() string   { return "temporary signer timeout" }
func (sodaTimeoutError) Timeout() bool   { return true }
func (sodaTimeoutError) Temporary() bool { return true }

func successfulSodaSignerResponse() *http.Response {
	body := `{"url":"https://api.qishui.com/luna/pc/me?device_id=1234567890123456789","headers":{"X-Helios":"helios-value","X-Medusa":"medusa-value"}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestSodaBDMSSignerSignsWithoutForwardingSecrets(t *testing.T) {
	var captured sodaSignerRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sign" {
			t.Errorf("unexpected signer request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer private-token" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode signer request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(sodaSignerResponse{
			URL: "https://api.qishui.com/luna/pc/search/track?q=test&device_id=1234567890123456789",
			Headers: map[string]string{
				"X-Helios": "helios-value",
				"X-Medusa": "medusa-value",
			},
		})
	}))
	defer server.Close()

	signer, err := newSodaBDMSSigner(server.URL, "private-token", time.Second)
	if err != nil {
		t.Fatalf("newSodaBDMSSigner() error = %v", err)
	}
	headers := make(http.Header)
	headers.Set("User-Agent", sodaPCUserAgent)
	headers.Set("Cookie", "sessionid=must-not-leave-client")
	headers.Set("Authorization", "must-not-leave-client")
	signedURL, signedHeaders, err := signer.Sign(
		context.Background(),
		"https://api.qishui.com/luna/pc/search/track?q=test",
		headers,
	)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if !captured.AddCommonParams {
		t.Fatal("add_common_params = false, want true")
	}
	for name := range captured.Headers {
		if strings.EqualFold(name, "Cookie") || strings.EqualFold(name, "Authorization") {
			t.Fatalf("sensitive header forwarded to signer: %s", name)
		}
	}
	if got := captured.Headers["User-Agent"]; got != sodaPCUserAgent {
		t.Fatalf("signer user-agent = %q", got)
	}
	if !strings.Contains(signedURL, "device_id=1234567890123456789") {
		t.Fatalf("signed URL = %q", signedURL)
	}
	if signedHeaders.Get("X-Helios") != "helios-value" || signedHeaders.Get("X-Medusa") != "medusa-value" {
		t.Fatalf("signed headers = %#v", signedHeaders)
	}
}

func TestSodaBDMSSignerRejectsMismatchedTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(sodaSignerResponse{
			URL: "https://attacker.example/collect",
			Headers: map[string]string{
				"X-Helios": "helios-value",
				"X-Medusa": "medusa-value",
			},
		})
	}))
	defer server.Close()

	signer, err := newSodaBDMSSigner(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("newSodaBDMSSigner() error = %v", err)
	}
	_, _, err = signer.Sign(context.Background(), "https://api.qishui.com/luna/pc/me", nil)
	if err == nil || !strings.Contains(err.Error(), "mismatched target") {
		t.Fatalf("Sign() error = %v, want mismatched target", err)
	}
}

func TestSodaBDMSSignerRetriesTemporaryTransportError(t *testing.T) {
	signer, err := newSodaBDMSSigner("http://signer.internal", "", time.Second)
	if err != nil {
		t.Fatalf("newSodaBDMSSigner() error = %v", err)
	}
	calls := 0
	signer.retryDelay = 0
	signer.httpClient.Transport = sodaRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, sodaTimeoutError{}
		}
		return successfulSodaSignerResponse(), nil
	})

	if _, _, err := signer.Sign(context.Background(), "https://api.qishui.com/luna/pc/me", nil); err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("signer requests = %d, want 2", calls)
	}
}

func TestSodaBDMSSignerRetriesHTTP5xx(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(sodaSignerResponse{
			URL: "https://api.qishui.com/luna/pc/me?device_id=1234567890123456789",
			Headers: map[string]string{
				"X-Helios": "helios-value",
				"X-Medusa": "medusa-value",
			},
		})
	}))
	defer server.Close()

	signer, err := newSodaBDMSSigner(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("newSodaBDMSSigner() error = %v", err)
	}
	signer.retryDelay = 0
	if _, _, err := signer.Sign(context.Background(), "https://api.qishui.com/luna/pc/me", nil); err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("signer requests = %d, want 2", calls)
	}
}

func TestSodaBDMSSignerDoesNotRetryHTTP4xx(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(sodaSignerResponse{Error: "bad request"})
	}))
	defer server.Close()

	signer, err := newSodaBDMSSigner(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("newSodaBDMSSigner() error = %v", err)
	}
	signer.retryDelay = 0
	_, _, err = signer.Sign(context.Background(), "https://api.qishui.com/luna/pc/me", nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("Sign() error = %v, want HTTP 400", err)
	}
	if calls != 1 {
		t.Fatalf("signer requests = %d, want 1", calls)
	}
}

func TestSodaBDMSSignerDoesNotRetryCanceledContext(t *testing.T) {
	signer, err := newSodaBDMSSigner("http://signer.internal", "", time.Second)
	if err != nil {
		t.Fatalf("newSodaBDMSSigner() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	signer.retryDelay = 0
	signer.httpClient.Transport = sodaRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		cancel()
		return nil, context.Canceled
	})

	_, _, err = signer.Sign(ctx, "https://api.qishui.com/luna/pc/me", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Sign() error = %v, want context canceled", err)
	}
	if calls != 1 {
		t.Fatalf("signer requests = %d, want 1", calls)
	}
}

type sodaStaticSigner struct {
	t            *testing.T
	receivedHead http.Header
}

func (s *sodaStaticSigner) Sign(_ context.Context, rawURL string, headers http.Header) (string, http.Header, error) {
	s.receivedHead = headers.Clone()
	target, err := url.Parse(rawURL)
	if err != nil {
		return "", nil, err
	}
	query := target.Query()
	query.Set("device_id", "1234567890123456789")
	target.RawQuery = query.Encode()
	return target.String(), http.Header{
		"X-Helios": []string{"helios-value"},
		"X-Medusa": []string{"medusa-value"},
	}, nil
}

func TestClientSignsPCAPIAndKeepsCookieLocal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/luna/pc/me" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("device_id") != "1234567890123456789" {
			t.Errorf("device_id = %q", r.URL.Query().Get("device_id"))
		}
		if r.Header.Get("User-Agent") != sodaPCUserAgent {
			t.Errorf("user-agent = %q", r.Header.Get("User-Agent"))
		}
		if r.Header.Get("X-Helios") != "helios-value" || r.Header.Get("X-Medusa") != "medusa-value" {
			t.Errorf("signature headers missing: %#v", r.Header)
		}
		if r.Header.Get("Cookie") != "sessionid=local-only" {
			t.Errorf("cookie = %q", r.Header.Get("Cookie"))
		}
		_, _ = w.Write([]byte(`{"status_code":0}`))
	}))
	defer server.Close()

	client := newSodaTestClient(server.URL)
	client.cookie = "sessionid=local-only"
	signer := &sodaStaticSigner{t: t}
	client.signer = signer
	if _, err := client.getJSON(context.Background(), "https://api.qishui.com/luna/pc/me"); err != nil {
		t.Fatalf("getJSON() error = %v", err)
	}
	if signer.receivedHead.Get("Cookie") != "" || signer.receivedHead.Get("Authorization") != "" {
		t.Fatalf("signer received sensitive headers: %#v", signer.receivedHead)
	}
}
