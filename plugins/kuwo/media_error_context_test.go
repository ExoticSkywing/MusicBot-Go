package kuwo

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// TestPaidTrackErrorCarriesResolverCause covers a diagnosability defect that
// cost real debugging time. When every lossless resolver failed, GetDownloadInfo
// discarded their errors and returned only the access flag, so a region block, an
// unreachable resolver host and a genuine entitlement problem all surfaced
// identically as "kuwo: paid track". Production logs showed 117 such lines whose
// actual cause was that the plugin had no API proxy configured and kuwo answered
// with HTTP 407 "not available in your region".
func TestPaidTrackErrorCarriesResolverCause(t *testing.T) {
	const trackID = "41378936"

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/" {
			return response(http.StatusOK, map[string]string{
				"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/",
			}, nil), nil
		}
		// A paid track, so validateTrackAccess fails.
		if req.URL.Host == "www.kuwo.cn" && !strings.Contains(req.URL.Path, "playUrl") {
			return response(http.StatusOK, nil, []byte(
				`{"data":{"rid":41378936,"duration":213,"isListenFee":true}}`,
			)), nil
		}
		// Every resolver fails with a distinctive, recognisable status.
		return response(http.StatusBadGateway, nil, nil), nil
	})

	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		home:            kuwoHomeURL,
		detail:          kuwoDetailURL,
		qualityResolver: "https://resolver.example/api",
	})
	client.apiHTTPClient.Transport = transport
	client.mediaHTTPClient.Transport = transport
	client.downloadHTTPClient = &http.Client{Transport: transport}

	_, err := client.GetDownloadInfo(context.Background(), trackID, platform.QualityHiRes)
	if err == nil {
		t.Fatal("GetDownloadInfo() succeeded, want failure")
	}

	// The access verdict must still be reported...
	if !strings.Contains(err.Error(), errPaidTrack.Error()) {
		t.Errorf("error lost the access verdict: %v", err)
	}
	// ...but it must no longer be the whole story.
	if !strings.Contains(err.Error(), "lossless resolvers failed") {
		t.Fatalf("error does not explain why the resolvers failed, so a network "+
			"fault is indistinguishable from an entitlement problem: %v", err)
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("resolver cause did not survive into the message: %v", err)
	}
}

// TestPaidTrackErrorStaysBareWhenNoResolverRan keeps the message clean for the
// case it was originally written for: a paid track whose tier has no resolver
// plan at all, where there is no underlying cause to report.
func TestPaidTrackErrorStaysBareWhenNoResolverRan(t *testing.T) {
	const trackID = "41378936"

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/" {
			return response(http.StatusOK, map[string]string{
				"Set-Cookie": kuwoSessionCookie + "=abcdefghijklmnop; Path=/",
			}, nil), nil
		}
		return response(http.StatusOK, nil, []byte(
			`{"data":{"rid":41378936,"duration":213,"isListenFee":true}}`,
		)), nil
	})

	client := newClientWithEndpoints(time.Second, nil, kuwoEndpoints{
		home:   kuwoHomeURL,
		detail: kuwoDetailURL,
	})
	client.apiHTTPClient.Transport = transport
	client.mediaHTTPClient.Transport = transport

	// QualityStandard has no lossless resolver plan, so accessErr returns alone.
	_, err := client.GetDownloadInfo(context.Background(), trackID, platform.QualityStandard)
	if err == nil {
		t.Fatal("GetDownloadInfo() succeeded, want failure")
	}
	if strings.Contains(err.Error(), "lossless resolvers failed") {
		t.Fatalf("no resolver ran, yet the message claims one failed: %v", err)
	}
	if !strings.Contains(err.Error(), errPaidTrack.Error()) {
		t.Fatalf("error = %v, want the paid-track verdict", err)
	}
}
