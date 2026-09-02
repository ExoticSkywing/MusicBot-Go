package thirdparty

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

type fakeProvider struct {
	name  string
	info  *platform.DownloadInfo
	err   error
	calls *[]string
}

func (p *fakeProvider) Name() string { return p.name }

func (p *fakeProvider) Resolve(context.Context, string, string, platform.Quality) (*platform.DownloadInfo, error) {
	if p.calls != nil {
		*p.calls = append(*p.calls, p.name)
	}
	return p.info, p.err
}

func TestParseMode(t *testing.T) {
	tests := []struct {
		raw  string
		want Mode
	}{
		{raw: "", want: ModeDisabled},
		{raw: "disabled", want: ModeDisabled},
		{raw: "official-first", want: ModeOfficialFirst},
		{raw: "fallback", want: ModeOfficialFirst},
		{raw: "third_party_first", want: ModeThirdPartyFirst},
		{raw: "prefer", want: ModeThirdPartyFirst},
	}
	for _, test := range tests {
		got, err := ParseMode(test.raw)
		if err != nil {
			t.Fatalf("ParseMode(%q): %v", test.raw, err)
		}
		if got != test.want {
			t.Fatalf("ParseMode(%q) = %q, want %q", test.raw, got, test.want)
		}
	}
	if _, err := ParseMode("sometimes"); err == nil {
		t.Fatal("expected an error for an unknown mode")
	}
}

func TestParseProviderNamesPreservesPriorityAndDeduplicates(t *testing.T) {
	got := ParseProviderNames(" jbsou, source_b;JBSOU\nsource_c ")
	want := []string{"jbsou", "source_b", "source_c"}
	if len(got) != len(want) {
		t.Fatalf("names = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("names = %#v, want %#v", got, want)
		}
	}
}

func TestChainFallsBackInConfiguredOrder(t *testing.T) {
	calls := []string{}
	want := &platform.DownloadInfo{
		URL:     "https://example.test/M800track.mp3",
		Size:    1024,
		Format:  "mp3",
		Bitrate: 320,
		Quality: platform.QualityHigh,
	}
	chain := &Chain{
		providers: []provider{
			&fakeProvider{name: "source_a", err: errors.New("offline"), calls: &calls},
			&fakeProvider{name: "source_b", info: want, calls: &calls},
			&fakeProvider{name: "source_c", info: want, calls: &calls},
		},
		perProviderTimeout: time.Second,
	}
	got, err := chain.Resolve(context.Background(), "qqmusic", "track", platform.QualityHigh)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Fatalf("Resolve returned %#v, want %#v", got, want)
	}
	if len(calls) != 2 || calls[0] != "source_a" || calls[1] != "source_b" {
		t.Fatalf("provider calls = %#v, want [source_a source_b]", calls)
	}
}

func TestNewChainRejectsUnknownProvider(t *testing.T) {
	if _, err := NewChain([]string{"jbsou", "does_not_exist"}, time.Second, nil); err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
}
