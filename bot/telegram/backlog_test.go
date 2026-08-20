package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
)

const testBotToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"

// backlogStub emulates the two Bot API methods the backlog helpers rely on and
// records the getUpdates offsets it was asked for.
type backlogStub struct {
	pending int
	latest  []telego.Update
	offsets []int

	webhookInfoErr bool
	getUpdatesErr  bool
}

func (s *backlogStub) server(t *testing.T) *telego.Bot {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasSuffix(r.URL.Path, "/getWebhookInfo"):
			if s.webhookInfoErr {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "boom"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"pending_update_count": s.pending},
			})

		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			if s.getUpdatesErr {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "boom"})
				return
			}
			var params struct {
				Offset int `json:"offset"`
			}
			_ = json.NewDecoder(r.Body).Decode(&params)
			s.offsets = append(s.offsets, params.Offset)

			// Only the peek (offset -1) returns updates; the confirming call
			// retires the queue and comes back empty, as Telegram does.
			result := []telego.Update{}
			if params.Offset == -1 {
				result = s.latest
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	bot, err := telego.NewBot(testBotToken, telego.WithAPIServer(srv.URL))
	if err != nil {
		t.Fatalf("NewBot() error = %v", err)
	}
	return bot
}

func TestPendingUpdateCount(t *testing.T) {
	stub := &backlogStub{pending: 656}
	got, err := PendingUpdateCount(context.Background(), stub.server(t))
	if err != nil {
		t.Fatalf("PendingUpdateCount() error = %v", err)
	}
	if got != 656 {
		t.Fatalf("PendingUpdateCount() = %d, want 656", got)
	}
}

func TestPendingUpdateCountRequiresClient(t *testing.T) {
	if _, err := PendingUpdateCount(context.Background(), nil); err == nil {
		t.Fatal("PendingUpdateCount(nil) error = nil, want error")
	}
}

func TestPendingUpdateCountPropagatesError(t *testing.T) {
	stub := &backlogStub{webhookInfoErr: true}
	if _, err := PendingUpdateCount(context.Background(), stub.server(t)); err == nil {
		t.Fatal("PendingUpdateCount() error = nil, want error")
	}
}

// A backlog is retired by confirming one past the newest update ID, which is
// what actually clears the queue on Telegram's side.
func TestDropPendingUpdatesConfirmsPastNewestID(t *testing.T) {
	stub := &backlogStub{
		pending: 656,
		latest:  []telego.Update{{UpdateID: 703141855}},
	}

	dropped, err := DropPendingUpdates(context.Background(), stub.server(t))
	if err != nil {
		t.Fatalf("DropPendingUpdates() error = %v", err)
	}
	if dropped != 656 {
		t.Fatalf("DropPendingUpdates() = %d, want 656", dropped)
	}

	want := []int{-1, 703141856}
	if len(stub.offsets) != len(want) {
		t.Fatalf("getUpdates offsets = %v, want %v", stub.offsets, want)
	}
	for i := range want {
		if stub.offsets[i] != want[i] {
			t.Fatalf("getUpdates offset[%d] = %d, want %d", i, stub.offsets[i], want[i])
		}
	}
}

// An empty queue must cost nothing: no getUpdates call at all, so a restart
// with no backlog is not delayed by a pointless round trip.
func TestDropPendingUpdatesSkipsEmptyBacklog(t *testing.T) {
	stub := &backlogStub{pending: 0}

	dropped, err := DropPendingUpdates(context.Background(), stub.server(t))
	if err != nil {
		t.Fatalf("DropPendingUpdates() error = %v", err)
	}
	if dropped != 0 {
		t.Fatalf("DropPendingUpdates() = %d, want 0", dropped)
	}
	if len(stub.offsets) != 0 {
		t.Fatalf("getUpdates called %d time(s), want 0", len(stub.offsets))
	}
}

// The backlog can drain between the count and the peek; that race must not be
// reported as a drop, and must not confirm a bogus offset.
func TestDropPendingUpdatesHandlesDrainedBacklog(t *testing.T) {
	stub := &backlogStub{pending: 5, latest: nil}

	dropped, err := DropPendingUpdates(context.Background(), stub.server(t))
	if err != nil {
		t.Fatalf("DropPendingUpdates() error = %v", err)
	}
	if dropped != 0 {
		t.Fatalf("DropPendingUpdates() = %d, want 0", dropped)
	}
	if len(stub.offsets) != 1 || stub.offsets[0] != -1 {
		t.Fatalf("getUpdates offsets = %v, want [-1]", stub.offsets)
	}
}

func TestDropPendingUpdatesRequiresClient(t *testing.T) {
	if _, err := DropPendingUpdates(context.Background(), nil); err == nil {
		t.Fatal("DropPendingUpdates(nil) error = nil, want error")
	}
}

func TestDropPendingUpdatesPropagatesError(t *testing.T) {
	stub := &backlogStub{pending: 10, getUpdatesErr: true}
	if _, err := DropPendingUpdates(context.Background(), stub.server(t)); err == nil {
		t.Fatal("DropPendingUpdates() error = nil, want error")
	}
}

// The fetch limit is the safeguard that keeps a replayed backlog from arriving
// as one burst of handler goroutines, so it must stay set and within the range
// the Bot API accepts.
func TestLongPollingParamsBoundsFetchLimit(t *testing.T) {
	params := LongPollingParams()

	if params.Limit <= 0 || params.Limit > 100 {
		t.Fatalf("LongPollingParams().Limit = %d, want within 1..100", params.Limit)
	}
	if params.Limit != updateFetchLimit {
		t.Fatalf("LongPollingParams().Limit = %d, want %d", params.Limit, updateFetchLimit)
	}
	if params.Timeout != longPollingTimeoutSeconds {
		t.Fatalf("LongPollingParams().Timeout = %d, want %d", params.Timeout, longPollingTimeoutSeconds)
	}
	if len(params.AllowedUpdates) != len(allowedUpdates) {
		t.Fatalf("LongPollingParams().AllowedUpdates = %v, want %v", params.AllowedUpdates, allowedUpdates)
	}
}
