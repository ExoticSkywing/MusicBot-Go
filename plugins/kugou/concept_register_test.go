package kugou

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestRegisterDeviceFailureEnvelopePreservesStateAndSkipsPersist(t *testing.T) {
	const responseSecret = "must-not-leak-token"
	tests := []struct {
		name     string
		register func(*ConceptAPIClient) (conceptDeviceInfo, error)
	}{
		{
			name: "regular",
			register: func(client *ConceptAPIClient) (conceptDeviceInfo, error) {
				return client.registerDevice(context.Background(), false)
			},
		},
		{
			name: "forced",
			register: func(client *ConceptAPIClient) (conceptDeviceInfo, error) {
				return client.ForceRegisterDevice(context.Background(), "stale-dfid")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initial := newTestConceptSessionManager("stale-dfid").Snapshot()
			initial.Cookie = "original-cookie"
			initial.Device.Source = "existing"
			initial.Device.UpdatedAt = "2026-09-08T00:00:00Z"
			persistCalls := 0
			mgr := NewConceptSessionManager(nil, func(map[string]string) error {
				persistCalls++
				return nil
			}, initial)
			mgr.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != "/risk/v2/r_register_dev" {
					t.Fatalf("unexpected path: %s", req.URL.Path)
				}
				body := `{"status":0,"errcode":20028,"error":"` + responseSecret + `","data":[]}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			})})

			_, err := tt.register(mgr.API())
			if err == nil {
				t.Fatal("registerDevice() expected failure")
			}
			for _, want := range []string{"status=0", "errcode=20028"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("registerDevice() error=%q missing %q", err, want)
				}
			}
			if strings.Contains(err.Error(), responseSecret) {
				t.Fatalf("registerDevice() leaked upstream response content: %q", err)
			}
			if persistCalls != 0 {
				t.Fatalf("persist calls=%d want 0", persistCalls)
			}
			if got := mgr.Snapshot(); !reflect.DeepEqual(got, initial) {
				t.Fatalf("failed registration changed state\n got: %#v\nwant: %#v", got, initial)
			}
		})
	}
}
