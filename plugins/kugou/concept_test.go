package kugou

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/guohuiyuan/music-lib/model"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestConceptSessionPersistAndStatus(t *testing.T) {
	var persisted map[string]string
	mgr := NewConceptSessionManager(nil, func(pairs map[string]string) error {
		persisted = pairs
		return nil
	}, conceptSession{Enabled: true, Token: "tok", UserID: "uid", Nickname: "tester", AutoRefresh: true})
	if !mgr.HasUsableSession() {
		t.Fatal("expected usable session")
	}
	if err := mgr.Persist(); err != nil {
		t.Fatalf("Persist() error = %v", err)
	}
	if persisted["concept_token"] != "tok" || persisted["concept_user_id"] != "uid" {
		t.Fatalf("persisted session mismatch: %#v", persisted)
	}
	status := mgr.StatusSummary()
	if !strings.Contains(status, "tester") || !strings.Contains(status, "uid") {
		t.Fatalf("StatusSummary()=%q", status)
	}
}

func TestConceptCreateQRCodeAndCheck(t *testing.T) {
	oldClient := http.DefaultClient
	defer func() { http.DefaultClient = oldClient }()
	hits := map[string]int{}
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		hits[req.URL.Path]++
		var body string
		switch req.URL.Path {
		case "/risk/v2/r_register_dev":
			body = `{"status":1,"data":{"dfid":"df123"}}`
		case "/v2/qrcode":
			body = `{"status":1,"data":{"qrcode":"qr-key-1"}}`
		case "/v2/get_userinfo_qrcode":
			body = `{"status":1,"data":{"status":4,"token":"token-1","userid":12345,"nickname":"nick"}}`
		default:
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	mgr := NewConceptSessionManager(nil, nil, conceptSession{Enabled: true})
	data, err := mgr.CreateQRCode(context.Background())
	if err != nil {
		t.Fatalf("CreateQRCode() error = %v", err)
	}
	if data.URL != "https://h5.kugou.com/apps/loginQRCode/html/index.html?qrcode=qr-key-1" {
		t.Fatalf("CreateQRCode URL=%q", data.URL)
	}
	if !strings.HasPrefix(data.Base64, "data:image/png;base64,") {
		t.Fatalf("CreateQRCode base64=%q", data.Base64)
	}
	check, err := mgr.CheckQRCode(context.Background())
	if err != nil {
		t.Fatalf("CheckQRCode() error = %v", err)
	}
	if check.Status != 4 {
		t.Fatalf("CheckQRCode status=%d", check.Status)
	}
	state := mgr.Snapshot()
	if state.Token != "token-1" || state.UserID != "12345" || state.Device.Dfid != "df123" {
		t.Fatalf("unexpected state: %#v", state)
	}
	if state.QRKey != "" || state.QRURL != "" || state.QRBase64 != "" {
		t.Fatalf("expected qr fields cleared after success, got state=%#v", state)
	}
	if hits["/risk/v2/r_register_dev"] == 0 {
		t.Fatal("expected risk/v2/r_register_dev to be called")
	}
}

func TestBuildQRStatusCaption(t *testing.T) {
	caption := buildQRStatusCaption(conceptQRCheckData{Status: 2, Nickname: conceptJSONText("tester"), UserID: conceptJSONText("12345")}, false)
	for _, want := range []string{"二维码状态: 已扫码，待确认", "昵称: tester", "用户ID: 12345", "已扫码，等待确认"} {
		if !strings.Contains(caption, want) {
			t.Fatalf("caption=%q missing %q", caption, want)
		}
	}
}

func TestBuildQRStatusCaptionMasked(t *testing.T) {
	caption := buildQRStatusCaption(conceptQRCheckData{Status: 2, Nickname: conceptJSONText("tester"), UserID: conceptJSONText("123456789")}, true)
	if !strings.Contains(caption, "昵称: tester") {
		t.Fatalf("caption=%q missing nickname", caption)
	}
	if strings.Contains(caption, "用户ID: 123456789") {
		t.Fatalf("caption=%q should mask user id", caption)
	}
}

func TestConceptFetchSongURLAndClientResolve(t *testing.T) {
	oldClient := http.DefaultClient
	defer func() { http.DefaultClient = oldClient }()
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v5/url" {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"status":1,"data":{}}`))}, nil
		}
		q := req.URL.Query()
		if q.Get("hash") != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
			t.Fatalf("unexpected hash=%q", q.Get("hash"))
		}
		if q.Get("signature") == "" {
			t.Fatal("expected signature query")
		}
		body := `{"status":1,"url":["https://concept.cdn/test.flac"],"timeLength":226000,"extName":"flac"}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	mgr := NewConceptSessionManager(nil, nil, conceptSession{Enabled: true, Token: "tok", UserID: "uid", Device: conceptDeviceInfo{Dfid: "dfid"}})
	resp, err := mgr.FetchSongURL(context.Background(), &model.Song{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", AlbumID: "41668184", Extra: map[string]string{"album_audio_id": "123", "album_id": "41668184"}}, kugouDownloadPlan{Hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Quality: platform.QualityLossless})
	if err != nil {
		t.Fatalf("FetchSongURL() error = %v", err)
	}
	if resp.URL[0] != "https://concept.cdn/test.flac" {
		t.Fatalf("FetchSongURL() url=%v", resp.URL)
	}
	client := NewClient("", nil)
	client.AttachConcept(mgr)
	resolved, err := client.fetchConceptSongURL(context.Background(), &model.Song{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", AlbumID: "41668184", Extra: map[string]string{"album_audio_id": "123", "album_id": "41668184"}}, kugouDownloadPlan{Hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Quality: platform.QualityLossless, Format: "flac"})
	if err != nil {
		t.Fatalf("fetchConceptSongURL() error = %v", err)
	}
	if resolved.URL != "https://concept.cdn/test.flac" || resolved.Ext != "flac" {
		t.Fatalf("resolved song=%+v", resolved)
	}
	if resolved.Extra["concept_source"] != "song/url" {
		t.Fatalf("resolved.Extra[concept_source]=%q", resolved.Extra["concept_source"])
	}
}

func TestConceptFetchSongURLRejectsVerificationWithNonEmptyURL(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"status":1,"errcode":20028,"error":"本次请求需要验证","url":["https://concept.cdn/must-not-use.flac"],"extName":"flac"}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	mgr := newTestConceptSessionManager("stale-dfid")
	mgr.SetHTTPClient(httpClient)

	_, err := mgr.FetchSongURL(
		context.Background(),
		&model.Song{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Extra: map[string]string{}},
		kugouDownloadPlan{Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Quality: platform.QualityHiRes},
	)
	if !errors.Is(err, errConceptDeviceVerification) {
		t.Fatalf("FetchSongURL() error=%v want device verification", err)
	}
}

func TestApplySessionMap(t *testing.T) {
	state := &conceptSession{}
	var payload map[string]any
	if err := json.Unmarshal([]byte(`{"token":"tok","userid":"uid","t1":"t1","vip_type":6,"vip_token":"vip","nickname":"nick"}`), &payload); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}
	applySessionMap(state, payload)
	if state.Token != "tok" || state.UserID != "uid" || state.T1 != "t1" || state.VIPToken != "vip" || state.Nickname != "nick" {
		t.Fatalf("unexpected state: %#v", state)
	}
	if state.VIPType != "6" {
		t.Fatalf("VIPType=%q want 6", state.VIPType)
	}
}

func TestConceptFetchAccountStatusUpdatesSession(t *testing.T) {
	oldClient := http.DefaultClient
	defer func() { http.DefaultClient = oldClient }()
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v3/get_my_info":
			body := `{"status":1,"data":{"nickname":"tester"}}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		case "/v1/get_union_vip":
			body := `{"status":1,"data":{"vip_type":"6","vip_token":"vip-token","expire_time":"2099-01-01"}}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		default:
			t.Fatalf("unexpected path: %s", req.URL.Path)
			return nil, nil
		}
	})}
	mgr := NewConceptSessionManager(nil, nil, conceptSession{Enabled: true, Token: "tok", UserID: "123", Device: conceptDeviceInfo{Dfid: "df", Guid: "guid", Mid: "mid", Dev: "DEV1234567", Mac: "02:00:00:00:00:00"}})
	user, vip, err := mgr.FetchAccountStatus(context.Background())
	if err != nil {
		t.Fatalf("FetchAccountStatus() error = %v", err)
	}
	if user == nil || user.Nickname != "tester" {
		t.Fatalf("unexpected user = %#v", user)
	}
	if vip == nil || vip.Raw["vip_token"] != "vip-token" {
		t.Fatalf("unexpected vip = %#v", vip)
	}
	state := mgr.Snapshot()
	if state.Nickname != "tester" || state.VIPType != "6" || state.VIPToken != "vip-token" || state.VIPExpireTime != "2099-01-01" {
		t.Fatalf("unexpected session state = %#v", state)
	}
	if state.LastCheckTime.IsZero() {
		t.Fatal("expected LastCheckTime updated")
	}
}

func TestConceptManualRenewUpdatesSession(t *testing.T) {
	oldClient := http.DefaultClient
	defer func() { http.DefaultClient = oldClient }()
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v5/login_by_token":
			body := `{"status":1,"data":{"token":"tok-new","userid":"321","t1":"t1-new","vip_type":7,"vip_token":"vip-new","nickname":"renewed"}}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		default:
			t.Fatalf("unexpected path: %s", req.URL.Path)
			return nil, nil
		}
	})}
	mgr := NewConceptSessionManager(nil, nil, conceptSession{Enabled: true, Token: "tok-old", UserID: "123", T1: "t1-old", Device: conceptDeviceInfo{Dfid: "df", Guid: "guid", Mid: "mid", Dev: "DEV1234567", Mac: "02:00:00:00:00:00"}})
	msg, err := mgr.ManualRenew(context.Background())
	if err != nil {
		t.Fatalf("ManualRenew() error = %v", err)
	}
	if !strings.Contains(msg, "续期完成") {
		t.Fatalf("ManualRenew() msg=%q", msg)
	}
	state := mgr.Snapshot()
	if state.Token != "tok-new" || state.UserID != "321" || state.T1 != "t1-new" || state.VIPType != "7" || state.VIPToken != "vip-new" || state.Nickname != "renewed" {
		t.Fatalf("unexpected renewed state = %#v", state)
	}
	if state.LastRefreshTime.IsZero() {
		t.Fatal("expected LastRefreshTime updated")
	}
}

func TestConceptFetchSongURLNew(t *testing.T) {
	oldClient := http.DefaultClient
	defer func() { http.DefaultClient = oldClient }()
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v6/priv_url" {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		body := `{"status":1,"data":[{"quality":"flac","tracker_url":"https://concept.cdn/enc.mflac"}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	mgr := NewConceptSessionManager(nil, nil, conceptSession{Enabled: true, Token: "tok", UserID: "123", VIPType: "6", VIPToken: "vip", Device: conceptDeviceInfo{Dfid: "df", Guid: "guid", Mid: "mid", Dev: "DEV1234567", Mac: "02:00:00:00:00:00"}})
	resp, err := mgr.FetchSongURLNew(context.Background(), &model.Song{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Extra: map[string]string{"album_audio_id": "123"}}, kugouDownloadPlan{Hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Quality: platform.QualityLossless})
	if err != nil {
		t.Fatalf("FetchSongURLNew() error = %v", err)
	}
	if resp.Status != 1 {
		t.Fatalf("FetchSongURLNew status=%d", resp.Status)
	}
	if !strings.Contains(string(resp.Data), "tracker_url") {
		t.Fatalf("FetchSongURLNew data=%s", string(resp.Data))
	}
}

func TestResolveConceptSongURLNewUsesNonEncryptedTrackerURL(t *testing.T) {
	client := NewClient("", nil)
	resp := &conceptSongURLNewResponse{
		Status: 1,
		Data: json.RawMessage(`[
			{"quality":"flac","tracker_url":"https://concept.cdn/encrypted.mflac","extname":"mflac"},
			{"quality":"320","tracker_url":"https://concept.cdn/plain.mp3","extname":"mp3"}
		]`),
	}
	resolved, ok := client.resolveConceptSongURLNew(&model.Song{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Extra: map[string]string{}}, kugouDownloadPlan{Hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Quality: platform.QualityHigh, Format: "mp3"}, resp)
	if !ok || resolved == nil {
		t.Fatal("expected resolveConceptSongURLNew to resolve a usable tracker url")
	}
	if resolved.URL != "https://concept.cdn/plain.mp3" {
		t.Fatalf("resolved.URL=%q", resolved.URL)
	}
	if resolved.Ext != "mp3" {
		t.Fatalf("resolved.Ext=%q", resolved.Ext)
	}
	if resolved.Extra["concept_source"] != "song/url/new" {
		t.Fatalf("concept_source=%q", resolved.Extra["concept_source"])
	}
}

func TestResolveConceptSongURLNewReplacesInheritedTierMetadata(t *testing.T) {
	client := NewClient("", nil)
	resp := &conceptSongURLNewResponse{
		Status: 1,
		Data: json.RawMessage(`[
			{"quality":"high","tracker_url":"https://concept.cdn/plain.flac","extname":"flac"}
		]`),
	}
	standard := &model.Song{
		ID:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Ext:     "mp3",
		Size:    3449303,
		Bitrate: 128,
		Extra:   map[string]string{},
	}
	resolved, ok := client.resolveConceptSongURLNew(standard, kugouDownloadPlan{
		Hash:    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Quality: platform.QualityHiRes,
		Format:  "flac",
		Size:    0,
		Bitrate: 2400,
	}, resp)
	if !ok || resolved == nil {
		t.Fatal("expected a resolved Hi-Res resource")
	}
	if resolved.Size != 0 {
		t.Fatalf("resolved.Size=%d inherited standard-tier size", resolved.Size)
	}
	if resolved.Bitrate != 2400 {
		t.Fatalf("resolved.Bitrate=%d want 2400", resolved.Bitrate)
	}
	if resolved.Ext != "flac" {
		t.Fatalf("resolved.Ext=%q want response ext flac", resolved.Ext)
	}
}

func TestConceptSongURLNewAuthError(t *testing.T) {
	err := conceptSongURLNewAuthError(&conceptSongURLNewResponse{ErrCode: 20018})
	if err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("expected auth error for errcode 20018, got %v", err)
	}
	err = conceptSongURLNewAuthError(&conceptSongURLNewResponse{Error: "需要VIP"})
	if err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("expected auth error for vip message, got %v", err)
	}
}

func TestResolveDownloadByQualityReregistersDeviceOnceAndRetriesSamePlan(t *testing.T) {
	const (
		staleDFID = "stale-dfid"
		freshDFID = "fresh-dfid"
		hiResHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	var oldURLHits, newURLHits, registerHits int
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/v5/url":
			oldURLHits++
			switch req.URL.Query().Get("dfid") {
			case staleDFID:
				body = `{"status":0,"errcode":20028,"error":"本次请求需要验证"}`
			case freshDFID:
				body = `{"status":1,"url":["https://concept.cdn/recovered.flac"],"extName":"flac"}`
			default:
				body = `{"status":0,"errcode":20028,"error":"本次请求需要验证"}`
			}
		case "/v6/priv_url":
			newURLHits++
			body = `{"status":0,"errcode":20028,"error":"本次请求需要验证"}`
		case "/risk/v2/r_register_dev":
			registerHits++
			if req.URL.Query().Get("dfid") != "-" {
				t.Errorf("register query dfid=%q want -", req.URL.Query().Get("dfid"))
			}
			if got := req.Header.Get("dfid"); got != "" && got != "-" {
				t.Errorf("register header dfid=%q must not reuse stale identity", got)
			}
			body = `{"status":1,"data":{"dfid":"fresh-dfid"}}`
		default:
			t.Errorf("unexpected path: %s", req.URL.Path)
			body = `{"status":0}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	mgr := newTestConceptSessionManager(staleDFID)
	mgr.SetHTTPClient(httpClient)
	client := NewClient("", nil)
	client.AttachConcept(mgr)
	song := &model.Song{
		ID:      "dddddddddddddddddddddddddddddddd",
		Ext:     "mp3",
		Size:    3449303,
		Bitrate: 128,
		Extra: map[string]string{
			"res_hash":      hiResHash,
			"high_filesize": "46414399",
		},
	}

	resolved, err := client.ResolveDownloadByQuality(context.Background(), song, platform.QualityHiRes)
	if err != nil {
		t.Fatalf("ResolveDownloadByQuality() error = %v", err)
	}
	if resolved == nil || resolved.ID != hiResHash || resolved.Size != 46414399 {
		t.Fatalf("resolved song=%+v", resolved)
	}
	if oldURLHits != 2 || newURLHits != 0 || registerHits != 1 {
		t.Fatalf("hits old/new/register=%d/%d/%d want 2/0/1", oldURLHits, newURLHits, registerHits)
	}
	if got := mgr.Snapshot().Device.Dfid; got != freshDFID {
		t.Fatalf("persisted dfid=%q want %q", got, freshDFID)
	}
}

func TestResolveDownloadByQualityRefreshesNewEndpointVerificationBeforeUsingPayload(t *testing.T) {
	var oldURLHits, newURLHits, registerHits int
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/v5/url":
			oldURLHits++
			if req.URL.Query().Get("dfid") == "fresh-dfid" {
				body = `{"status":1,"url":["https://concept.cdn/recovered.flac"],"extName":"flac"}`
			} else {
				body = `{"status":0,"error":"unavailable"}`
			}
		case "/v6/priv_url":
			newURLHits++
			body = `{"status":0,"errcode":20028,"error":"本次请求需要验证","data":[{"tracker_url":"https://concept.cdn/stale.flac","extname":"flac"}]}`
		case "/risk/v2/r_register_dev":
			registerHits++
			body = `{"status":1,"data":{"dfid":"fresh-dfid"}}`
		default:
			t.Errorf("unexpected path: %s", req.URL.Path)
			body = `{"status":0}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	mgr := newTestConceptSessionManager("stale-dfid")
	mgr.SetHTTPClient(httpClient)
	client := NewClient("", nil)
	client.AttachConcept(mgr)
	song := &model.Song{
		ID: "dddddddddddddddddddddddddddddddd",
		Extra: map[string]string{
			"res_hash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}

	resolved, err := client.ResolveDownloadByQuality(context.Background(), song, platform.QualityHiRes)
	if err != nil {
		t.Fatalf("ResolveDownloadByQuality() error = %v", err)
	}
	if resolved == nil || resolved.URL != "https://concept.cdn/recovered.flac" {
		t.Fatalf("resolved song=%+v", resolved)
	}
	if oldURLHits != 2 || newURLHits != 1 || registerHits != 1 {
		t.Fatalf("hits old/new/register=%d/%d/%d want 2/1/1", oldURLHits, newURLHits, registerHits)
	}
}

func TestResolveDownloadByQualityStopsAfterPersistentDeviceVerification(t *testing.T) {
	const hiResHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var urlHashes []string
	var newURLHits, registerHits int
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/v5/url":
			urlHashes = append(urlHashes, req.URL.Query().Get("hash"))
			body = `{"status":0,"errcode":20028,"error":"本次请求需要验证"}`
		case "/v6/priv_url":
			newURLHits++
			body = `{"status":0,"errcode":20028,"error":"本次请求需要验证"}`
		case "/risk/v2/r_register_dev":
			registerHits++
			body = `{"status":1,"data":{"dfid":"fresh-dfid"}}`
		default:
			t.Errorf("unexpected path: %s", req.URL.Path)
			body = `{"status":0}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	mgr := newTestConceptSessionManager("stale-dfid")
	mgr.SetHTTPClient(httpClient)
	client := NewClient("", nil)
	client.AttachConcept(mgr)
	song := &model.Song{
		ID: "dddddddddddddddddddddddddddddddd",
		Extra: map[string]string{
			"res_hash": hiResHash,
			"sq_hash":  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}

	_, err := client.ResolveDownloadByQuality(context.Background(), song, platform.QualityHiRes)
	if !errors.Is(err, errConceptDeviceVerification) {
		t.Fatalf("ResolveDownloadByQuality() error=%v want device verification", err)
	}
	if len(urlHashes) != 2 || urlHashes[0] != hiResHash || urlHashes[1] != hiResHash {
		t.Fatalf("url hashes=%v want the same Hi-Res plan twice", urlHashes)
	}
	if newURLHits != 0 || registerHits != 1 {
		t.Fatalf("hits new/register=%d/%d want 0/1", newURLHits, registerHits)
	}
}

func TestResolveDownloadByQualityConcurrentVerificationRegistersDeviceOnce(t *testing.T) {
	const workers = 8
	var staleURLHits, freshURLHits, registerHits int32
	allStaleRequests := make(chan struct{})
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/v5/url":
			if req.URL.Query().Get("dfid") == "fresh-dfid" {
				atomic.AddInt32(&freshURLHits, 1)
				body = `{"status":1,"url":["https://concept.cdn/recovered.flac"],"extName":"flac"}`
			} else {
				if atomic.AddInt32(&staleURLHits, 1) == workers {
					close(allStaleRequests)
				}
				<-allStaleRequests
				body = `{"status":0,"errcode":20028,"error":"本次请求需要验证"}`
			}
		case "/risk/v2/r_register_dev":
			atomic.AddInt32(&registerHits, 1)
			body = `{"status":1,"data":{"dfid":"fresh-dfid"}}`
		default:
			t.Errorf("unexpected path: %s", req.URL.Path)
			body = `{"status":0}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	mgr := newTestConceptSessionManager("stale-dfid")
	mgr.SetHTTPClient(httpClient)
	client := NewClient("", nil)
	client.AttachConcept(mgr)
	song := &model.Song{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Extra: map[string]string{}}

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			resolved, err := client.ResolveDownloadByQuality(context.Background(), song, platform.QualityStandard)
			if err == nil && (resolved == nil || resolved.URL == "") {
				err = errors.New("resolved song missing URL")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ResolveDownloadByQuality() error = %v", err)
		}
	}
	if got := atomic.LoadInt32(&registerHits); got != 1 {
		t.Fatalf("register hits=%d want 1", got)
	}
	if got := atomic.LoadInt32(&staleURLHits); got != workers {
		t.Fatalf("stale URL hits=%d want %d", got, workers)
	}
	if got := atomic.LoadInt32(&freshURLHits); got != workers {
		t.Fatalf("fresh URL hits=%d want %d", got, workers)
	}
}

func TestResolveDownloadByQualityRejectsFailedForcedRegistration(t *testing.T) {
	tests := []struct {
		name         string
		registerBody string
		registerCode int
	}{
		{name: "empty dfid", registerBody: `{"status":1,"data":{"dfid":""}}`, registerCode: http.StatusOK},
		{name: "http failure", registerBody: `temporary failure`, registerCode: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var urlHits, registerHits int
			httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body := `{"status":0,"errcode":20028,"error":"本次请求需要验证"}`
				status := http.StatusOK
				if req.URL.Path == "/v5/url" {
					urlHits++
				} else if req.URL.Path == "/risk/v2/r_register_dev" {
					registerHits++
					body = tt.registerBody
					status = tt.registerCode
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			mgr := newTestConceptSessionManager("stale-dfid")
			mgr.SetHTTPClient(httpClient)
			client := NewClient("", nil)
			client.AttachConcept(mgr)
			song := &model.Song{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Extra: map[string]string{}}

			if _, err := client.ResolveDownloadByQuality(context.Background(), song, platform.QualityStandard); err == nil {
				t.Fatal("ResolveDownloadByQuality() expected forced-registration error")
			}
			if urlHits != 1 || registerHits != 1 {
				t.Fatalf("hits url/register=%d/%d want 1/1", urlHits, registerHits)
			}
			if got := mgr.Snapshot().Device.Dfid; got != "stale-dfid" {
				t.Fatalf("failed registration replaced dfid with %q", got)
			}
		})
	}
}

func TestResolveDownloadByQualityPropagatesForcedRegistrationPersistFailure(t *testing.T) {
	persistErr := errors.New("persist failed")
	var urlHits, registerHits int
	var attemptedPersistDFID string
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/v5/url":
			urlHits++
			body = `{"status":0,"errcode":20028,"error":"本次请求需要验证"}`
		case "/risk/v2/r_register_dev":
			registerHits++
			body = `{"status":1,"data":{"dfid":"fresh-dfid"}}`
		default:
			t.Errorf("unexpected path: %s", req.URL.Path)
			body = `{"status":0}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	mgr := NewConceptSessionManager(nil, func(pairs map[string]string) error {
		attemptedPersistDFID = pairs["concept_dfid"]
		return persistErr
	}, newTestConceptSessionManager("stale-dfid").Snapshot())
	mgr.SetHTTPClient(httpClient)
	client := NewClient("", nil)
	client.AttachConcept(mgr)
	song := &model.Song{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Extra: map[string]string{}}

	_, err := client.ResolveDownloadByQuality(context.Background(), song, platform.QualityStandard)
	if !errors.Is(err, persistErr) {
		t.Fatalf("ResolveDownloadByQuality() error=%v want persist failure", err)
	}
	if urlHits != 1 || registerHits != 1 {
		t.Fatalf("hits url/register=%d/%d want 1/1", urlHits, registerHits)
	}
	if got := mgr.Snapshot().Device.Dfid; got != "stale-dfid" {
		t.Fatalf("persist failure left in-memory dfid=%q want rollback to stale-dfid", got)
	}
	if attemptedPersistDFID != "fresh-dfid" {
		t.Fatalf("attempted persist dfid=%q want fresh-dfid", attemptedPersistDFID)
	}
}

func TestForceRegisterDevicePersistFailurePreservesRenewedSessionCookie(t *testing.T) {
	const (
		staleDFID    = "stale-dfid"
		freshDFID    = "fresh-dfid"
		renewedToken = "renewed-token"
		renewedT1    = "renewed-t1"
	)
	persistErr := errors.New("persist failed")
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/risk/v2/r_register_dev" {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		body := `{"status":1,"data":{"dfid":"fresh-dfid"}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	initial := newTestConceptSessionManager(staleDFID).Snapshot()
	var mgr *ConceptSessionManager
	mgr = NewConceptSessionManager(nil, func(pairs map[string]string) error {
		if got := pairs["concept_dfid"]; got != freshDFID {
			t.Fatalf("persisted dfid=%q want %q", got, freshDFID)
		}
		mgr.Update(func(s *conceptSession) {
			s.Token = renewedToken
			s.T1 = renewedT1
			s.Cookie = mgr.API().buildConceptCookie(s)
		})
		return persistErr
	}, initial)
	mgr.Update(func(s *conceptSession) {
		s.Cookie = mgr.API().buildConceptCookie(s)
	})
	mgr.SetHTTPClient(httpClient)

	_, err := mgr.API().ForceRegisterDevice(context.Background(), staleDFID)
	if !errors.Is(err, persistErr) {
		t.Fatalf("ForceRegisterDevice() error=%v want persist failure", err)
	}
	state := mgr.Snapshot()
	if state.Device.Dfid != staleDFID {
		t.Fatalf("rollback dfid=%q want %q", state.Device.Dfid, staleDFID)
	}
	if state.Token != renewedToken || state.T1 != renewedT1 {
		t.Fatalf("renewed session fields token/t1=%q/%q", state.Token, state.T1)
	}
	wantCookie := mgr.API().buildConceptCookie(&state)
	if state.Cookie != wantCookie {
		t.Fatalf("rollback cookie=%q want current session cookie %q", state.Cookie, wantCookie)
	}
}

func newTestConceptSessionManager(dfid string) *ConceptSessionManager {
	return NewConceptSessionManager(nil, nil, conceptSession{
		Enabled: true,
		Token:   "test-token",
		UserID:  "123",
		Device: conceptDeviceInfo{
			Dfid: dfid,
			Guid: "0123456789abcdef0123456789abcdef",
			Mid:  "fedcba9876543210fedcba9876543210",
			Dev:  "TESTDEVICE",
			Mac:  "02:00:00:00:00:00",
		},
	})
}

func TestConceptStatusSummaryIncludesMoreFields(t *testing.T) {
	mgr := NewConceptSessionManager(nil, nil, conceptSession{
		Enabled:       true,
		Token:         "tok",
		UserID:        "uid",
		T1:            "t1",
		Nickname:      "tester",
		VIPType:       "6",
		VIPExpireTime: "2099-01-01",
		SessionSource: "concept_qr",
		Device: conceptDeviceInfo{
			Dfid: "dfid",
			Mid:  "mid",
			Dev:  "DEV1234567",
		},
	})
	summary := mgr.StatusSummary()
	for _, want := range []string{"酷狗概念版状态", "- 会话: 可用", "- Token: tok", "- T1: t1", "- VIP到期: 2099-01-01", "DFID: dfid", "MID: mid", "DEV: DEV1234567", "- 来源: concept_qr"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("StatusSummary()=%q missing %q", summary, want)
		}
	}
}

func TestDescribeQRStatus(t *testing.T) {
	tests := map[int]string{
		0: "已过期",
		1: "等待扫码",
		2: "已扫码，待确认",
		4: "登录成功",
		9: "9",
	}
	for input, want := range tests {
		if got := describeQRStatus(input); got != want {
			t.Fatalf("describeQRStatus(%d)=%q want %q", input, got, want)
		}
	}
}

func TestConceptSignInAcceptsStringData(t *testing.T) {
	oldClient := http.DefaultClient
	defer func() { http.DefaultClient = oldClient }()
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v3/get_my_info":
			body := `{"status":1,"data":{"nickname":"tester"}}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		case "/v1/get_union_vip":
			body := `{"status":1,"data":{"vip_type":"6","vip_token":"vip-token","expire_time":"2099-01-01"}}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		case "/youth/v1/recharge/receive_vip_listen_song":
			body := `{"status":1,"message":"领取成功","data":"ok"}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		case "/youth/v1/listen_song/upgrade_vip_reward":
			body := `{"status":1,"message":"升级成功","data":"ok"}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		default:
			t.Fatalf("unexpected path: %s", req.URL.Path)
			return nil, nil
		}
	})}
	mgr := NewConceptSessionManager(nil, nil, conceptSession{Enabled: true, Token: "tok", UserID: "123", Device: conceptDeviceInfo{Dfid: "df", Guid: "guid", Mid: "mid", Dev: "DEV1234567", Mac: "02:00:00:00:00:00"}})
	msg, err := mgr.SignIn(context.Background())
	if err != nil {
		t.Fatalf("SignIn() error = %v", err)
	}
	for _, want := range []string{"概念版签到/VIP 领取已尝试", "领取: 领取成功", "升级: 升级成功", "当前VIP类型: 6", "VIP到期: 2099-01-01"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("SignIn()=%q missing %q", msg, want)
		}
	}
}
