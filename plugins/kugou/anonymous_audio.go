package kugou

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"

	"github.com/guohuiyuan/music-lib/model"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const anonymousPlayURL = "https://m.kugou.com/app/i/getSongInfo.php"

// Request contract adapted from guohuiyuan/music-lib kugou.fetchSongInfo,
// as validated by MusicDownloader. The mobile endpoint serves standard MP3 even for some HQ hashes. Always
// request the catalog's standard hash and verify the rendition actually served.
func (c *Client) resolveAnonymousAudio(ctx context.Context, song *model.Song) (*model.Song, error) {
	sessionCookie, sessionToken := c.baseCookie(), ""
	if c.concept != nil {
		sessionToken = c.concept.Snapshot().Token
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hash := normalizeHash(firstNonEmpty(song.Extra["file_hash"], song.Extra["hash"], song.ID))
	if hash == "" || song.Duration <= 0 {
		return nil, platform.NewUnavailableError("kugou", "track", song.ID)
	}
	client := *c.htmlHTTPClient()
	client.Jar = nil
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, anonymousPlayURL+"?"+url.Values{"cmd": {"playInfo"}, "hash": {hash}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", kugouPlaylistAndroidUA)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kugou: anonymous audio HTTP %d", resp.StatusCode)
	}
	var data struct {
		kugouMobilePlayInfoResponse
		Hash     string          `json:"hash"`
		FreePart json.RawMessage `json:"is_free_part"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data); err != nil {
		return nil, err
	}
	if (data.Status != 1 && data.Status != 200) || data.ErrCode != 0 || data.URL == "" || (data.Hash != "" && normalizeHash(data.Hash) != hash) {
		return nil, platform.NewUnavailableError("kugou", "track", song.ID)
	}
	if len(data.FreePart) > 0 && string(data.FreePart) != "0" && string(data.FreePart) != `"0"` && string(data.FreePart) != "false" {
		return nil, platform.ErrIncompleteAudio
	}
	duration := normalizeGatewayDuration(parseKugouInt(data.Timelength))
	if duration <= 0 || math.Abs(float64(duration-song.Duration)) > math.Max(3, float64(song.Duration)*0.02) {
		return nil, platform.ErrIncompleteAudio
	}
	size, bitrate, err := probeAnonymousMP3(ctx, &client, data.URL, song.Duration)
	if err != nil {
		return nil, err
	}
	if c.baseCookie() != sessionCookie || (c.concept != nil && c.concept.Snapshot().Token != sessionToken) {
		return nil, errors.New("kugou: account changed during audio resolution")
	}
	resolved := cloneSongWithHash(song, hash)
	resolved.URL, resolved.Ext, resolved.Size, resolved.Bitrate = data.URL, "mp3", size, bitrate
	quality := platform.QualityStandard
	if bitrate >= 256 {
		quality = platform.QualityHigh
	}
	extra := ensureSongExtra(resolved)
	extra["resolved_quality"] = quality.String()
	extra["play_url"] = data.URL
	return resolved, nil
}

func probeAnonymousMP3(ctx context.Context, client *http.Client, rawURL string, duration int) (int64, int, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return 0, 0, errors.New("kugou: invalid anonymous media URL")
	}
	readRange := func(offset int64) ([]byte, int64, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+4095))
		req.Header.Set("User-Agent", kugouPlaylistAndroidUA)
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		var size int64
		if resp.StatusCode == http.StatusPartialContent {
			var start, end int64
			if _, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &size); err != nil || start != offset || end < start || size <= end {
				return nil, 0, errors.New("kugou: invalid media range")
			}
		} else if resp.StatusCode == http.StatusOK && offset == 0 {
			size = resp.ContentLength
		} else {
			return nil, 0, errors.New("kugou: media range unavailable")
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return data, size, err
	}
	data, size, err := readRange(0)
	if err != nil {
		return 0, 0, err
	}
	var offset int64
	if len(data) >= 10 && string(data[:3]) == "ID3" {
		for _, b := range data[6:10] {
			if b >= 128 {
				return 0, 0, errors.New("kugou: invalid ID3 size")
			}
		}
		offset = 10 + int64(data[6])<<21 + int64(data[7])<<14 + int64(data[8])<<7 + int64(data[9])
		if data[3] == 4 && data[5]&16 != 0 {
			offset += 10
		}
		if offset > 16<<20 || offset+4 >= size {
			return 0, 0, errors.New("kugou: invalid ID3 size")
		}
		if offset+4 > int64(len(data)) {
			var nextSize int64
			data, nextSize, err = readRange(offset)
			if err != nil {
				return 0, 0, err
			}
			if size != nextSize {
				return 0, 0, errors.New("kugou: media changed during probe")
			}
		} else {
			data = data[offset:]
		}
	}
	if len(data) < 4 {
		return 0, 0, errors.New("kugou: missing MP3 frame")
	}
	header := binary.BigEndian.Uint32(data[:4])
	version, layer, index, sample := header>>19&3, header>>17&3, header>>12&15, header>>10&3
	if header>>21 != 0x7ff || version == 1 || layer != 1 || index == 0 || index == 15 || sample == 3 {
		return 0, 0, errors.New("kugou: invalid MP3 frame")
	}
	rates := [...]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	if version != 3 {
		rates = [...]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	}
	bitrate := rates[index]
	if duration <= 0 || size <= offset || float64(size-offset)*8/float64(bitrate*1000) < float64(duration)*0.9 {
		return 0, 0, platform.ErrIncompleteAudio
	}
	return size, bitrate, nil
}
