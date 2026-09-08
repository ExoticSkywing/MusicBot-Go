package kugou

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	conceptVerificationTTL            = 10 * time.Minute
	conceptVerificationRelayBodyLimit = 64 << 10
)

var errConceptVerificationRelay = errors.New("kugou verification relay unavailable")

type conceptVerificationProof struct {
	Ticket  string `json:"ticket"`
	RandStr string `json:"randstr"`
	SID     string `json:"sid"`
	EDT     string `json:"edt"`
}

type conceptVerificationRelayChallenge struct {
	ID        string
	URL       string
	ExpiresAt time.Time
}

type conceptVerificationRelayResult struct {
	Status string                   `json:"status"`
	Proof  conceptVerificationProof `json:"proof"`
}

type conceptVerificationRelay interface {
	Create(context.Context, string, time.Duration) (conceptVerificationRelayChallenge, error)
	Poll(context.Context, string) (conceptVerificationRelayResult, error)
	Complete(context.Context, string, string) error
}

type conceptVerificationTestRelay interface {
	CreateTest(context.Context, string, time.Duration) (conceptVerificationRelayChallenge, error)
}

type httpConceptVerificationRelay struct {
	baseURL *url.URL
	secret  string
	client  *http.Client
}

func newHTTPConceptVerificationRelay(rawURL, secret string, client *http.Client) (*httpConceptVerificationRelay, error) {
	rawURL = strings.TrimSpace(rawURL)
	secret = strings.TrimSpace(secret)
	if rawURL == "" && secret == "" {
		return nil, nil
	}
	if rawURL == "" || secret == "" {
		return nil, errors.New("kugou verification relay configuration incomplete")
	}
	if len(secret) < 32 {
		return nil, errors.New("kugou verification relay secret is too short")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("kugou verification relay URL must be HTTPS")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	isolatedClient := *client
	isolatedClient.Jar = nil
	isolatedClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &httpConceptVerificationRelay{baseURL: parsed, secret: secret, client: &isolatedClient}, nil
}

func (r *httpConceptVerificationRelay) Create(ctx context.Context, appID string, ttl time.Duration) (conceptVerificationRelayChallenge, error) {
	return r.create(ctx, appID, ttl, false)
}

func (r *httpConceptVerificationRelay) CreateTest(ctx context.Context, appID string, ttl time.Duration) (conceptVerificationRelayChallenge, error) {
	return r.create(ctx, appID, ttl, true)
}

func (r *httpConceptVerificationRelay) create(ctx context.Context, appID string, ttl time.Duration, testMode bool) (conceptVerificationRelayChallenge, error) {
	if r == nil || r.baseURL == nil || r.client == nil || !validConceptVerificationAppID(appID) {
		return conceptVerificationRelayChallenge{}, errConceptVerificationRelay
	}
	if ttl <= 0 {
		ttl = conceptVerificationTTL
	}
	startedAt := time.Now()
	payload := struct {
		CaptchaAppID string `json:"captcha_app_id"`
		ExpiresIn    int64  `json:"expires_in"`
		TestMode     bool   `json:"test_mode,omitempty"`
	}{CaptchaAppID: strings.TrimSpace(appID), ExpiresIn: int64(ttl / time.Second), TestMode: testMode}
	var response struct {
		ID        string `json:"id"`
		URL       string `json:"url"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := r.doJSON(ctx, http.MethodPost, "/api/challenges", payload, &response); err != nil {
		return conceptVerificationRelayChallenge{}, err
	}
	response.ID = strings.TrimSpace(response.ID)
	response.URL = strings.TrimSpace(response.URL)
	publicURL, err := url.Parse(response.URL)
	expiresAt := time.Unix(response.ExpiresAt, 0)
	if !validConceptRelayID(response.ID) || err != nil || publicURL.Scheme != r.baseURL.Scheme || !strings.EqualFold(publicURL.Host, r.baseURL.Host) || publicURL.User != nil || publicURL.RawPath != "" || publicURL.RawQuery != "" || publicURL.Fragment != "" || !validConceptPublicPath(publicURL.Path) || !expiresAt.After(time.Now()) || expiresAt.After(startedAt.Add(ttl+time.Minute)) {
		return conceptVerificationRelayChallenge{}, errConceptVerificationRelay
	}
	return conceptVerificationRelayChallenge{ID: response.ID, URL: response.URL, ExpiresAt: expiresAt}, nil
}

func (r *httpConceptVerificationRelay) Poll(ctx context.Context, id string) (conceptVerificationRelayResult, error) {
	if r == nil || !validConceptRelayID(id) {
		return conceptVerificationRelayResult{}, errConceptVerificationRelay
	}
	var result conceptVerificationRelayResult
	if err := r.doJSON(ctx, http.MethodGet, "/api/challenges/"+id, nil, &result); err != nil {
		return conceptVerificationRelayResult{}, err
	}
	result.Status = strings.ToLower(strings.TrimSpace(result.Status))
	return result, nil
}

func (r *httpConceptVerificationRelay) Complete(ctx context.Context, id, status string) error {
	status = strings.ToLower(strings.TrimSpace(status))
	if r == nil || !validConceptRelayID(id) || (status != "verified" && status != "failed" && status != "expired") {
		return errConceptVerificationRelay
	}
	payload := struct {
		Status string `json:"status"`
	}{Status: status}
	return r.doJSON(ctx, http.MethodPost, "/api/challenges/"+id+"/complete", payload, nil)
}

func (r *httpConceptVerificationRelay) doJSON(ctx context.Context, method, path string, payload, out any) error {
	if r == nil || r.baseURL == nil || r.client == nil {
		return errConceptVerificationRelay
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return errConceptVerificationRelay
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := *r.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	endpoint.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return errConceptVerificationRelay
	}
	req.Header.Set("Authorization", "Bearer "+r.secret)
	req.Header.Set("User-Agent", "MusicBot-Verification/1.0")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return errConceptVerificationRelay
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, conceptVerificationRelayBodyLimit+1))
	if err != nil || len(responseBody) > conceptVerificationRelayBodyLimit {
		return errConceptVerificationRelay
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errConceptVerificationRelay
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(responseBody, out); err != nil {
		return errConceptVerificationRelay
	}
	return nil
}

func validConceptRelayID(id string) bool {
	id = strings.TrimSpace(id)
	if len(id) != 64 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'f') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func validConceptPublicPath(path string) bool {
	return strings.HasPrefix(path, "/v/") && len(path) == len("/v/")+64 && validConceptRelayID(strings.TrimPrefix(path, "/v/"))
}
