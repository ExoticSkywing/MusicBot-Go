package kugou

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/guohuiyuan/music-lib/model"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const (
	conceptVerificationPlaybackV5 = "v5"
	conceptVerificationPlaybackV6 = "v6"
	conceptVerificationVType      = 23
	conceptVerificationPollEvery  = 2 * time.Second
	conceptVerificationAPITimeout = 20 * time.Second
	conceptVerificationProofLimit = 50_000
	conceptVerificationTestAppID  = "197787253"
)

var errConceptVerificationFlow = errors.New("kugou verification unavailable")

type conceptVerificationPlayback struct {
	Endpoint     string
	SongID       string
	AlbumID      string
	AlbumAudioID string
	Plan         kugouDownloadPlan
}

type conceptVerificationChallenge struct {
	EventID  string
	Session  conceptSession
	Playback conceptVerificationPlayback
}

func newConceptVerificationChallenge(eventID string, state conceptSession, song *model.Song, plan kugouDownloadPlan, endpoint string) *conceptVerificationChallenge {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" || song == nil {
		return nil
	}
	albumID := firstNonEmpty(song.AlbumID, "0")
	albumAudioID := ""
	if song.Extra != nil {
		albumID = firstNonEmpty(song.Extra["album_id"], song.AlbumID, "0")
		albumAudioID = strings.TrimSpace(song.Extra["album_audio_id"])
	}
	return &conceptVerificationChallenge{
		EventID: eventID,
		Session: state,
		Playback: conceptVerificationPlayback{
			Endpoint:     endpoint,
			SongID:       strings.TrimSpace(song.ID),
			AlbumID:      albumID,
			AlbumAudioID: albumAudioID,
			Plan:         plan,
		},
	}
}

type conceptVerificationIdentity struct {
	Enabled  bool
	Token    string
	UserID   string
	T1       string
	VIPType  string
	VIPToken string
	Cookie   string
	Device   conceptDeviceInfo
}

func conceptVerificationIdentityFor(state conceptSession) conceptVerificationIdentity {
	return conceptVerificationIdentity{
		Enabled:  state.Enabled,
		Token:    strings.TrimSpace(state.Token),
		UserID:   strings.TrimSpace(state.UserID),
		T1:       strings.TrimSpace(state.T1),
		VIPType:  strings.TrimSpace(state.VIPType),
		VIPToken: strings.TrimSpace(state.VIPToken),
		Cookie:   strings.TrimSpace(state.Cookie),
		Device: conceptDeviceInfo{
			Dfid: strings.TrimSpace(state.Device.Dfid),
			Mid:  strings.TrimSpace(state.Device.Mid),
			Guid: strings.TrimSpace(state.Device.Guid),
			Dev:  strings.TrimSpace(state.Device.Dev),
			Mac:  strings.TrimSpace(state.Device.Mac),
		},
	}
}

type conceptVerificationAttempt struct {
	identity     conceptVerificationIdentity
	session      conceptSession
	eventID      string
	playback     conceptVerificationPlayback
	ready        chan struct{}
	readyOnce    sync.Once
	setupOK      bool
	relayID      string
	publicURL    string
	expiresAt    time.Time
	verifyType   int
	captchaAppID string
	ctx          context.Context
	cancel       context.CancelFunc
}

type conceptVerificationCoordinator struct {
	manager      *ConceptSessionManager
	mu           sync.Mutex
	relay        conceptVerificationRelay
	active       *conceptVerificationAttempt
	closed       bool
	pollInterval time.Duration
	now          func() time.Time
	wg           sync.WaitGroup
}

func newConceptVerificationCoordinator(manager *ConceptSessionManager) *conceptVerificationCoordinator {
	return &conceptVerificationCoordinator{
		manager:      manager,
		pollInterval: conceptVerificationPollEvery,
		now:          time.Now,
	}
}

func (m *ConceptSessionManager) SetVerificationRelay(rawURL, secret string) error {
	if m == nil {
		return errConceptVerificationFlow
	}
	relay, err := newHTTPConceptVerificationRelay(rawURL, secret, nil)
	if err != nil {
		return err
	}
	if m.verification == nil {
		m.verification = newConceptVerificationCoordinator(m)
	}
	if relay == nil {
		return m.verification.setRelay(nil)
	}
	return m.verification.setRelay(relay)
}

func (c *conceptVerificationCoordinator) setRelay(relay conceptVerificationRelay) error {
	if c == nil {
		return errConceptVerificationFlow
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.active != nil {
		return errConceptVerificationFlow
	}
	c.relay = relay
	return nil
}

func (m *ConceptSessionManager) startVerificationTest(ctx context.Context) (platform.VerificationTest, error) {
	if m == nil || m.verification == nil {
		return platform.VerificationTest{}, errConceptVerificationFlow
	}
	c := m.verification
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return platform.VerificationTest{}, errConceptVerificationFlow
	}
	relay, ok := c.relay.(conceptVerificationTestRelay)
	c.mu.Unlock()
	if !ok || relay == nil {
		return platform.VerificationTest{}, errConceptVerificationFlow
	}
	handle, err := relay.CreateTest(ctx, conceptVerificationTestAppID, conceptVerificationTTL)
	if err != nil {
		return platform.VerificationTest{}, errConceptVerificationFlow
	}
	return platform.VerificationTest{URL: handle.URL, ExpiresAt: handle.ExpiresAt}, nil
}

func (m *ConceptSessionManager) beginVerification(ctx context.Context, sourceErr error) (error, bool) {
	if m == nil || m.verification == nil {
		return sourceErr, false
	}
	challenge := conceptVerificationChallengeFromError(sourceErr)
	if challenge == nil || strings.TrimSpace(challenge.EventID) == "" {
		return sourceErr, false
	}
	return m.verification.begin(ctx, challenge, sourceErr)
}

func (c *conceptVerificationCoordinator) begin(ctx context.Context, challenge *conceptVerificationChallenge, sourceErr error) (error, bool) {
	if c == nil || challenge == nil {
		return sourceErr, false
	}
	identity := conceptVerificationIdentityFor(challenge.Session)
	now := c.now()

	c.mu.Lock()
	if c.closed || c.relay == nil {
		c.mu.Unlock()
		return sourceErr, false
	}
	relay := c.relay
	if active := c.active; active != nil && active.identity == identity && (active.expiresAt.IsZero() || now.Before(active.expiresAt)) {
		c.mu.Unlock()
		return c.waitUntilReady(ctx, active, sourceErr), true
	}
	old := c.active
	oldRelayID := ""
	if old != nil {
		oldRelayID = old.relayID
		old.setupOK = false
		old.readyOnce.Do(func() { close(old.ready) })
		old.cancel()
	}
	attemptCtx, cancel := context.WithCancel(context.Background())
	attempt := &conceptVerificationAttempt{
		identity:  identity,
		session:   challenge.Session,
		eventID:   strings.TrimSpace(challenge.EventID),
		playback:  challenge.Playback,
		ready:     make(chan struct{}),
		ctx:       attemptCtx,
		cancel:    cancel,
		setupOK:   false,
		expiresAt: now.Add(conceptVerificationTTL),
	}
	c.active = attempt
	c.wg.Add(1)
	c.mu.Unlock()

	if oldRelayID != "" {
		c.completeDetached(relay, oldRelayID, "expired")
	}
	c.setup(attempt, relay)
	return c.waitUntilReady(ctx, attempt, sourceErr), true
}

func (c *conceptVerificationCoordinator) waitUntilReady(ctx context.Context, attempt *conceptVerificationAttempt, sourceErr error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-attempt.ready:
	}
	c.mu.Lock()
	setupOK := attempt.setupOK
	publicURL := attempt.publicURL
	expiresAt := attempt.expiresAt
	c.mu.Unlock()
	if !setupOK || publicURL == "" || expiresAt.IsZero() || !c.identityMatches(attempt.identity) {
		return sourceErr
	}
	return &platform.VerificationRequiredError{URL: publicURL, ExpiresAt: expiresAt}
}

func (c *conceptVerificationCoordinator) setup(attempt *conceptVerificationAttempt, relay conceptVerificationRelay) {
	defer c.wg.Done()
	setupCtx, cancel := context.WithTimeout(attempt.ctx, conceptVerificationAPITimeout)
	defer cancel()
	if !c.identityMatches(attempt.identity) {
		c.failSetup(attempt)
		return
	}
	appID, verifyType, err := c.manager.client.fetchVerificationInfo(setupCtx, attempt.session, attempt.eventID)
	if err != nil || !c.identityMatches(attempt.identity) {
		c.failSetup(attempt)
		return
	}
	handle, err := relay.Create(setupCtx, appID, conceptVerificationTTL)
	if err != nil {
		c.failSetup(attempt)
		return
	}
	now := c.now()
	if !handle.ExpiresAt.After(now) || handle.ExpiresAt.After(now.Add(conceptVerificationTTL+time.Minute)) {
		c.completeDetached(relay, handle.ID, "expired")
		c.failSetup(attempt)
		return
	}
	identityCurrent := c.identityMatches(attempt.identity)
	c.mu.Lock()
	if c.closed || c.active != attempt || !identityCurrent {
		c.mu.Unlock()
		c.completeDetached(relay, handle.ID, "expired")
		c.failSetup(attempt)
		return
	}
	attempt.relayID = handle.ID
	attempt.publicURL = handle.URL
	attempt.expiresAt = handle.ExpiresAt
	attempt.verifyType = verifyType
	attempt.captchaAppID = appID
	attempt.setupOK = true
	attempt.readyOnce.Do(func() { close(attempt.ready) })
	c.wg.Add(1)
	c.mu.Unlock()
	go c.poll(attempt, relay)
}

func (c *conceptVerificationCoordinator) failSetup(attempt *conceptVerificationAttempt) {
	if c == nil || attempt == nil {
		return
	}
	c.mu.Lock()
	if c.active == attempt {
		c.active = nil
	}
	attempt.setupOK = false
	attempt.readyOnce.Do(func() { close(attempt.ready) })
	c.mu.Unlock()
	attempt.cancel()
}

func (c *conceptVerificationCoordinator) poll(attempt *conceptVerificationAttempt, relay conceptVerificationRelay) {
	defer c.wg.Done()
	for {
		if !c.isCurrent(attempt) {
			return
		}
		if !c.identityMatches(attempt.identity) {
			c.completeAndFinish(attempt, relay, "expired")
			return
		}
		if !c.now().Before(attempt.expiresAt) {
			c.completeAndFinish(attempt, relay, "expired")
			return
		}

		pollCtx, cancel := context.WithTimeout(attempt.ctx, conceptVerificationAPITimeout)
		result, err := relay.Poll(pollCtx, attempt.relayID)
		cancel()
		if err == nil {
			switch result.Status {
			case "pending":
			case "submitted":
				status := c.processProof(attempt, result.Proof)
				c.completeAndFinish(attempt, relay, status)
				return
			case "verified", "failed", "expired":
				c.finish(attempt)
				return
			default:
				c.completeAndFinish(attempt, relay, "failed")
				return
			}
		}
		if !c.waitForPoll(attempt) {
			return
		}
	}
}

func (c *conceptVerificationCoordinator) processProof(attempt *conceptVerificationAttempt, proof conceptVerificationProof) string {
	if !validConceptVerificationProof(proof) {
		return "failed"
	}
	if !c.isCurrent(attempt) || !c.identityMatches(attempt.identity) {
		return "expired"
	}
	requestCtx, cancel := context.WithTimeout(attempt.ctx, conceptVerificationAPITimeout)
	err := c.manager.client.submitVerificationProof(requestCtx, attempt.session, attempt.eventID, attempt.verifyType, attempt.captchaAppID, proof)
	cancel()
	if err != nil {
		return "failed"
	}
	if !c.isCurrent(attempt) || !c.identityMatches(attempt.identity) {
		return "expired"
	}
	requestCtx, cancel = context.WithTimeout(attempt.ctx, conceptVerificationAPITimeout)
	err = c.manager.client.retryVerificationPlayback(requestCtx, attempt.session, attempt.playback)
	cancel()
	if err != nil {
		return "failed"
	}
	if !c.isCurrent(attempt) || !c.identityMatches(attempt.identity) {
		return "expired"
	}
	return "verified"
}

func (c *conceptVerificationCoordinator) waitForPoll(attempt *conceptVerificationAttempt) bool {
	interval := c.pollInterval
	if interval <= 0 {
		interval = conceptVerificationPollEvery
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-attempt.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *conceptVerificationCoordinator) completeAndFinish(attempt *conceptVerificationAttempt, relay conceptVerificationRelay, status string) {
	for tries := 0; tries < 3 && c.isCurrent(attempt) && c.now().Before(attempt.expiresAt); tries++ {
		completeCtx, cancel := context.WithTimeout(attempt.ctx, conceptVerificationAPITimeout)
		err := relay.Complete(completeCtx, attempt.relayID, status)
		cancel()
		if err == nil {
			break
		}
		if tries < 2 && !c.waitForPoll(attempt) {
			break
		}
	}
	c.finish(attempt)
}

func (c *conceptVerificationCoordinator) finish(attempt *conceptVerificationAttempt) {
	c.mu.Lock()
	if c.active == attempt {
		c.active = nil
	}
	c.mu.Unlock()
	attempt.cancel()
}

func (c *conceptVerificationCoordinator) isCurrent(attempt *conceptVerificationAttempt) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && c.active == attempt
}

func (c *conceptVerificationCoordinator) identityMatches(identity conceptVerificationIdentity) bool {
	return c != nil && c.manager != nil && conceptVerificationIdentityFor(c.manager.Snapshot()) == identity
}

func (c *conceptVerificationCoordinator) completeDetached(relay conceptVerificationRelay, id, status string) {
	if relay == nil || strings.TrimSpace(id) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), conceptVerificationAPITimeout)
	_ = relay.Complete(ctx, id, status)
	cancel()
}

func (c *conceptVerificationCoordinator) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	attempt := c.active
	relay := c.relay
	relayID := ""
	if attempt != nil {
		c.active = nil
		relayID = attempt.relayID
		attempt.setupOK = false
		attempt.readyOnce.Do(func() { close(attempt.ready) })
		attempt.cancel()
	}
	c.mu.Unlock()
	if relayID != "" {
		c.completeDetached(relay, relayID, "expired")
	}
	c.wg.Wait()
}

func (c *ConceptAPIClient) fetchVerificationInfo(ctx context.Context, state conceptSession, eventID string) (string, int, error) {
	body, err := json.Marshal(map[string]any{
		"eventid": strings.TrimSpace(eventID),
		"userid":  parseConceptInt64(state.UserID),
		"platid":  2,
		"rtype":   1,
		"wasm":    1,
		"i":       "",
		"sid":     "",
		"edt":     "",
	})
	if err != nil {
		return "", 0, errConceptVerificationFlow
	}
	query, _ := c.defaultQuery(state, time.Now(), false)
	query.Set("signature", conceptSignatureAndroid(query, string(body)))
	var response struct {
		conceptBaseResponse
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodPost, kugouConceptGatewayBaseURL+"/verifyservice/v3/get_verify_info?"+query.Encode(), bytes.NewReader(body), state, map[string]string{"Content-Type": "application/json"}, &response); err != nil || response.Status != 1 {
		return "", 0, errConceptVerificationFlow
	}
	appID := conceptVerificationCaptchaAppID(response.Data)
	if appID == "" {
		return "", 0, errConceptVerificationFlow
	}
	verifyType := conceptVerificationType(response.Data)
	if verifyType != conceptVerificationVType {
		return "", 0, errConceptVerificationFlow
	}
	return appID, verifyType, nil
}

func (c *ConceptAPIClient) submitVerificationProof(ctx context.Context, state conceptSession, eventID string, verifyType int, captchaAppID string, proof conceptVerificationProof) error {
	if !validConceptVerificationProof(proof) || verifyType != conceptVerificationVType || !validConceptVerificationAppID(captchaAppID) {
		return errConceptVerificationFlow
	}
	params, key, err := conceptAESCBCEncryptHexAuto(map[string]any{})
	if err != nil {
		return errConceptVerificationFlow
	}
	pk, err := conceptRSARawEncryptHex(map[string]any{"key": key}, conceptLitePublicKeyPEM)
	if err != nil {
		return errConceptVerificationFlow
	}
	ticket, err := json.Marshal(map[string]string{
		"ticket":  strings.TrimSpace(proof.Ticket),
		"randstr": strings.TrimSpace(proof.RandStr),
		"txappid": strings.TrimSpace(captchaAppID),
	})
	if err != nil {
		return errConceptVerificationFlow
	}
	body, err := json.Marshal(map[string]any{
		"eventid":    strings.TrimSpace(eventID),
		"userid":     parseConceptInt64(state.UserID),
		"platid":     2,
		"v_type":     verifyType,
		"wasm":       1,
		"i":          "",
		"sid":        strings.TrimSpace(proof.SID),
		"edt":        strings.TrimSpace(proof.EDT),
		"verifycode": "KGCodeTX|" + string(ticket),
		"pk":         pk,
		"params":     params,
	})
	if err != nil {
		return errConceptVerificationFlow
	}
	query, _ := c.defaultQuery(state, time.Now(), false)
	query.Set("clientver", "11510")
	query.Set("signature", conceptSignatureAndroid(query, string(body)))
	var response struct {
		conceptBaseResponse
		ErrorCode int `json:"error_code"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "https://verifyservice.kugou.com/v4/verify_user_info?"+query.Encode(), bytes.NewReader(body), state, map[string]string{"Content-Type": "application/json", "clienttime": query.Get("clienttime")}, &response); err != nil || response.Status != 1 || response.ErrCode != 0 || response.ErrorCode != 0 {
		return errConceptVerificationFlow
	}
	return nil
}

func (c *ConceptAPIClient) retryVerificationPlayback(ctx context.Context, state conceptSession, playback conceptVerificationPlayback) error {
	song := &model.Song{
		ID:      playback.SongID,
		AlbumID: playback.AlbumID,
		Extra: map[string]string{
			"album_id":       playback.AlbumID,
			"album_audio_id": playback.AlbumAudioID,
		},
	}
	switch playback.Endpoint {
	case conceptVerificationPlaybackV5:
		response, err := c.FetchSongURL(ctx, song, playback.Plan)
		if err != nil || response == nil || response.Status != 1 || len(response.URL) == 0 || strings.TrimSpace(response.URL[0]) == "" {
			return errConceptVerificationFlow
		}
		return nil
	case conceptVerificationPlaybackV6:
		response, err := c.FetchSongURLNew(ctx, song, playback.Plan)
		if err != nil || response == nil || response.Status != 1 || conceptSongURLNewError(response) != nil {
			return errConceptVerificationFlow
		}
		if _, ok := (&Client{}).resolveConceptSongURLNew(song, playback.Plan, response); !ok {
			return errConceptVerificationFlow
		}
		return nil
	default:
		return errConceptVerificationFlow
	}
}

func conceptVerificationCaptchaAppID(data map[string]json.RawMessage) string {
	for _, key := range []string{"captcha_app_id", "txappid", "appid"} {
		if value := conceptVerificationJSONText(data[key]); validConceptVerificationAppID(value) {
			return value
		}
	}
	rawURL := conceptVerificationJSONText(data["url"])
	if value := conceptVerificationKGCodeTXAppID(rawURL); value != "" {
		return value
	}
	if parsed, err := url.Parse(rawURL); err == nil {
		for _, key := range []string{"captcha_app_id", "txappid", "appid"} {
			if value := strings.TrimSpace(parsed.Query().Get(key)); validConceptVerificationAppID(value) {
				return value
			}
		}
	}
	return ""
}

func conceptVerificationType(data map[string]json.RawMessage) int {
	if verifyType := conceptVerificationJSONInt(data["v_type"]); verifyType != 0 {
		return verifyType
	}
	rawURL := conceptVerificationJSONText(data["url"])
	if conceptVerificationKGCodeTXAppID(rawURL) != "" {
		return conceptVerificationVType
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return 0
	}
	for _, key := range []string{"v_type", "verify_type"} {
		if verifyType, err := strconv.Atoi(strings.TrimSpace(parsed.Query().Get(key))); err == nil && verifyType != 0 {
			return verifyType
		}
	}
	for _, key := range []string{"type", "verify_type", "verifycode"} {
		if strings.EqualFold(strings.TrimSpace(parsed.Query().Get(key)), "KGCodeTX") {
			return conceptVerificationVType
		}
	}
	return 0
}

func conceptVerificationKGCodeTXAppID(value string) string {
	value = strings.TrimSpace(value)
	const prefix = "KGCodeTX|"
	if len(value) <= len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) {
		return ""
	}
	appID := strings.TrimSpace(value[len(prefix):])
	if !validConceptVerificationAppID(appID) {
		return ""
	}
	return appID
}

func conceptVerificationJSONText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value conceptJSONText
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(string(value))
}

func conceptVerificationJSONInt(raw json.RawMessage) int {
	value, err := strconv.Atoi(conceptVerificationJSONText(raw))
	if err != nil {
		return 0
	}
	return value
}

func validConceptVerificationProof(proof conceptVerificationProof) bool {
	fields := []struct {
		value string
		max   int
	}{
		{proof.Ticket, 8192},
		{proof.RandStr, 256},
		{proof.SID, 2048},
		{proof.EDT, 49152},
	}
	for _, field := range fields {
		value := strings.TrimSpace(field.value)
		if value == "" || len(value) > field.max || len(value) > conceptVerificationProofLimit {
			return false
		}
	}
	return true
}

func validConceptVerificationAppID(appID string) bool {
	appID = strings.TrimSpace(appID)
	if len(appID) < 6 || len(appID) > 12 {
		return false
	}
	for _, r := range appID {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
