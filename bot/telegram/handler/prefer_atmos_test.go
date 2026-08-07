package handler

import (
	"context"
	"errors"
	"sync"
	"testing"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/liuran001/MusicBot-Go/plugins/applemusic"
)

type preferAtmosDownloadPlatform struct {
	*stubPlatform
	mu    sync.Mutex
	calls []platform.Quality
}

func (p *preferAtmosDownloadPlatform) GetTrack(context.Context, string) (*platform.Track, error) {
	available := true
	return &platform.Track{ID: "track", Title: "Atmos Track", AtmosAvailable: &available}, nil
}

func (p *preferAtmosDownloadPlatform) GetDownloadInfo(_ context.Context, _ string, quality platform.Quality) (*platform.DownloadInfo, error) {
	p.mu.Lock()
	p.calls = append(p.calls, quality)
	p.mu.Unlock()
	if quality == platform.QualityAtmos {
		return nil, platform.NewInvalidQualityError("applemusic", "track", quality)
	}
	return nil, errors.New("unexpected stereo resolve")
}

func (p *preferAtmosDownloadPlatform) downloadCalls() []platform.Quality {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]platform.Quality(nil), p.calls...)
}

func TestQualityIntentValue(t *testing.T) {
	tests := []struct {
		input    string
		quality  string
		explicit bool
	}{
		{"", "", false},
		{"auto-lossless", "lossless", false},
		{" AUTO-HIRES ", "hires", false},
		{"lossless", "lossless", true},
		{"auto-invalid", "auto-invalid", true},
	}
	for _, tt := range tests {
		quality, explicit := qualityIntentValue(tt.input)
		if quality != tt.quality || explicit != tt.explicit {
			t.Fatalf("qualityIntentValue(%q) = (%q, %v), want (%q, %v)", tt.input, quality, explicit, tt.quality, tt.explicit)
		}
	}
	if got := implicitQualityToken("lossless"); got != "auto-lossless" {
		t.Fatalf("implicitQualityToken(lossless) = %q", got)
	}
	if got := implicitQualityToken("invalid"); got != "" {
		t.Fatalf("implicitQualityToken(invalid) = %q, want empty", got)
	}
}

func TestPreferAppleMusicAtmosEnabled(t *testing.T) {
	ctx := context.Background()
	repo := newStubRepo()
	manager := newStubManager()
	apple := newStubPlatform("applemusic")
	apple.capabilities.Atmos = true
	manager.Register(apple)

	if preferAppleMusicAtmosEnabled(ctx, repo, manager, botpkg.PluginScopeUser, 42, "applemusic", false) {
		t.Fatal("preference must default to off")
	}
	if err := repo.SetPluginSetting(ctx, botpkg.PluginScopeUser, 42, "applemusic", applemusic.PreferAtmosKey, applemusic.PreferAtmosOn); err != nil {
		t.Fatal(err)
	}
	if !preferAppleMusicAtmosEnabled(ctx, repo, manager, botpkg.PluginScopeUser, 42, "applemusic", false) {
		t.Fatal("enabled implicit Apple Music request should prefer Atmos")
	}
	if preferAppleMusicAtmosEnabled(ctx, repo, manager, botpkg.PluginScopeUser, 42, "applemusic", true) {
		t.Fatal("explicit quality must override preference")
	}
	if preferAppleMusicAtmosEnabled(ctx, repo, manager, botpkg.PluginScopeUser, 42, "netease", false) {
		t.Fatal("preference must not leak to other platforms")
	}
	if preferAppleMusicAtmosEnabled(ctx, repo, manager, botpkg.PluginScopeGroup, 7, "applemusic", false) {
		t.Fatal("a user setting must not leak into an unset group scope")
	}
	if err := repo.SetPluginSetting(ctx, botpkg.PluginScopeGroup, 7, "applemusic", applemusic.PreferAtmosKey, applemusic.PreferAtmosOn); err != nil {
		t.Fatal(err)
	}
	if !preferAppleMusicAtmosEnabled(ctx, repo, manager, botpkg.PluginScopeGroup, 7, "applemusic", false) {
		t.Fatal("enabled group preference was not read from group scope")
	}
}

func TestPreferredAppleMusicQuality(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name      string
		track     *platform.Track
		baseline  platform.Quality
		prefer    bool
		want      platform.Quality
		automatic bool
	}{
		{"available", &platform.Track{AtmosAvailable: &yes}, platform.QualityLossless, true, platform.QualityAtmos, true},
		{"unavailable", &platform.Track{AtmosAvailable: &no}, platform.QualityLossless, true, platform.QualityLossless, false},
		{"unknown", &platform.Track{}, platform.QualityHiRes, true, platform.QualityHiRes, false},
		{"disabled", &platform.Track{AtmosAvailable: &yes}, platform.QualityLossless, false, platform.QualityLossless, false},
		{"already-atmos", &platform.Track{AtmosAvailable: &yes}, platform.QualityAtmos, true, platform.QualityAtmos, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, automatic := preferredAppleMusicQuality(tt.track, tt.baseline, tt.prefer)
			if got != tt.want || automatic != tt.automatic {
				t.Fatalf("got (%s, %v), want (%s, %v)", got, automatic, tt.want, tt.automatic)
			}
		})
	}
}

func TestFindInlineCachedSongPreferAtmosDoesNotUseStereoCache(t *testing.T) {
	ctx := context.Background()
	repo := newStubRepo()
	manager := newStubManager()
	apple := newStubPlatform("applemusic")
	apple.capabilities.Atmos = true
	manager.Register(apple)
	h := &MusicHandler{Repo: repo, PlatformManager: manager, DefaultQuality: "lossless"}

	const userID int64 = 88
	const trackID = "track"
	if err := repo.SetPluginSetting(ctx, botpkg.PluginScopeUser, userID, "applemusic", applemusic.PreferAtmosKey, applemusic.PreferAtmosOn); err != nil {
		t.Fatal(err)
	}
	repo.platformSongs["applemusic:"+trackID+":lossless"] = &botpkg.SongInfo{
		Platform: "applemusic", TrackID: trackID, Quality: "lossless", FileID: "stereo",
		QualityVerified: true, QualityRevision: botpkg.AppleMusicQualityRevision,
	}

	cached, quality, err := h.findInlineCachedSong(ctx, userID, 0, false, "applemusic", trackID, "auto-lossless")
	if err != nil {
		t.Fatal(err)
	}
	if cached != nil || quality != "atmos" {
		t.Fatalf("implicit preference returned cache=%v quality=%q, want nil/atmos", cached, quality)
	}

	repo.platformSongs["applemusic:"+trackID+":atmos"] = &botpkg.SongInfo{
		Platform: "applemusic", TrackID: trackID, Quality: "atmos", FileID: "spatial",
		QualityVerified: true, QualityRevision: botpkg.AppleMusicQualityRevision,
	}
	cached, quality, err = h.findInlineCachedSong(ctx, userID, 0, false, "applemusic", trackID, "auto-lossless")
	if err != nil {
		t.Fatal(err)
	}
	if cached == nil || cached.FileID != "spatial" || quality != "atmos" {
		t.Fatalf("Atmos cache = %#v quality=%q", cached, quality)
	}

	cached, quality, err = h.findInlineCachedSong(ctx, userID, 0, false, "applemusic", trackID, "lossless")
	if err != nil {
		t.Fatal(err)
	}
	if cached == nil || cached.FileID != "stereo" || quality != "lossless" {
		t.Fatalf("explicit lossless cache = %#v quality=%q", cached, quality)
	}

	const groupID int64 = -100777
	if err := repo.SetPluginSetting(ctx, botpkg.PluginScopeGroup, groupID, "applemusic", applemusic.PreferAtmosKey, applemusic.PreferAtmosOn); err != nil {
		t.Fatal(err)
	}
	cached, quality, err = h.findInlineCachedSong(ctx, 777, groupID, true, "applemusic", trackID, "auto-lossless")
	if err != nil {
		t.Fatal(err)
	}
	if cached == nil || cached.FileID != "spatial" || quality != "atmos" {
		t.Fatalf("group Atmos cache = %#v quality=%q", cached, quality)
	}
	cached, quality, err = h.findInlineCachedSong(ctx, 777, groupID, false, "applemusic", trackID, "auto-lossless")
	if err != nil {
		t.Fatal(err)
	}
	if cached == nil || cached.FileID != "stereo" || quality != "lossless" {
		t.Fatalf("group setting leaked to user scope: cache=%#v quality=%q", cached, quality)
	}
}

func TestPrepareInlineSongAutoAtmosFallsBackToBaselineCache(t *testing.T) {
	ctx := context.Background()
	repo := newStubRepo()
	manager := newStubManager()
	base := newStubPlatform("applemusic")
	base.capabilities.Atmos = true
	apple := &preferAtmosDownloadPlatform{stubPlatform: base}
	manager.Register(apple)

	const userID int64 = 99
	if err := repo.SetPluginSetting(ctx, botpkg.PluginScopeUser, userID, "applemusic", applemusic.PreferAtmosKey, applemusic.PreferAtmosOn); err != nil {
		t.Fatal(err)
	}
	repo.userSettings[userID] = &botpkg.UserSettings{UserID: userID, DefaultQuality: "lossless"}
	repo.platformSongs["applemusic:track:lossless"] = &botpkg.SongInfo{
		Platform: "applemusic", TrackID: "track", Quality: "lossless", FileID: "stereo",
		QualityVerified: true, QualityRevision: botpkg.AppleMusicQualityRevision,
	}
	h := &MusicHandler{Repo: repo, PlatformManager: manager, DefaultQuality: "hires"}

	song, err := h.prepareInlineSong(ctx, nil, userID, 0, false, "", "applemusic", "track", "", nil, nil)
	if err != nil {
		t.Fatalf("automatic preference returned error: %v", err)
	}
	if song == nil || song.FileID != "stereo" || song.Quality != "lossless" {
		t.Fatalf("fallback song = %#v", song)
	}
	if calls := apple.downloadCalls(); len(calls) != 1 || calls[0] != platform.QualityAtmos {
		t.Fatalf("download qualities = %v, want [atmos] before cached fallback", calls)
	}

	if _, err := h.prepareInlineSong(ctx, nil, userID, 0, false, "", "applemusic", "track", "atmos", nil, nil); !errors.Is(err, platform.ErrInvalidQuality) {
		t.Fatalf("explicit Atmos error = %v, want ErrInvalidQuality", err)
	}
	if calls := apple.downloadCalls(); len(calls) != 2 || calls[1] != platform.QualityAtmos {
		t.Fatalf("explicit Atmos unexpectedly fell back, calls = %v", calls)
	}
}
