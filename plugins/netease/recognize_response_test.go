package netease

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type recognitionResponseTransport func(*http.Request) (*http.Response, error)

func (f recognitionResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// Exercise the API and adapter together: only successful empty matches should
// reach the Telegram handlers' existing no-result path, never service errors.
func TestRecognizeResponseMapping(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantID  string
		wantErr string
	}{
		{name: "no match", status: 200, body: `{"code":200,"message":"","data":{"result":[],"noMatchReason":10}}`},
		{name: "null data", status: 200, body: `{"code":200,"data":null}`},
		{name: "matched", status: 200, body: `{"code":200,"data":{"result":[{"song":{"id":1877703,"name":"Children"}}]}}`, wantID: "1877703"},
		{name: "business failure", status: 200, body: `{"code":403,"message":"denied"}`, wantErr: "returned code 403: denied"},
		{name: "business rate limit", status: 200, body: `{"code":429,"message":"busy","data":{"result":[]}}`, wantErr: "returned code 429"},
		{name: "missing code", status: 200, body: `{"data":{"result":[]}}`, wantErr: "returned code 0"},
		{name: "http failure", status: 503, body: `{"code":200,"data":{"result":[]}}`, wantErr: "returned status 503"},
		{name: "invalid json", status: 200, body: `not json`, wantErr: "parse audio-match response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			svc := NewRecognizeService(0)
			svc.client.Transport = recognitionResponseTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodPost || req.URL.String() != neteaseMatchURL {
					t.Fatalf("unexpected recognition request: %s %s", req.Method, req.URL)
				}
				return &http.Response{
					StatusCode: tt.status,
					Body:       io.NopCloser(strings.NewReader(tt.body)),
					Header:     make(http.Header),
				}, nil
			})
			response, matchErr := svc.match(context.Background(), "test-fingerprint")
			result, err := mapRecognizeResult(response, matchErr)
			if calls != 1 {
				t.Fatalf("made %d API calls, want 1", calls)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || result != nil {
					t.Fatalf("got (%+v, %v), want error containing %q", result, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantID == "" {
				if result != nil {
					t.Fatalf("expected no result, got %+v", result)
				}
				return
			}
			if result == nil || result.TrackID != tt.wantID || result.Platform != "netease" || result.URL != "https://music.163.com/song/"+tt.wantID {
				t.Fatalf("unexpected match: %+v", result)
			}
		})
	}
}

func TestRecognizeResponsePreservesTransportError(t *testing.T) {
	want := errors.New("upstream unavailable")
	svc := NewRecognizeService(0)
	svc.client.Transport = recognitionResponseTransport(func(*http.Request) (*http.Response, error) {
		return nil, want
	})
	response, matchErr := svc.match(context.Background(), "test-fingerprint")
	result, err := mapRecognizeResult(response, matchErr)
	if result != nil || !errors.Is(err, want) {
		t.Fatalf("got (%+v, %v), want original transport error", result, err)
	}
}
