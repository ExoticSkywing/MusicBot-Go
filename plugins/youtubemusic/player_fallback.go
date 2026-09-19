package youtubemusic

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
)

// A generic unavailable response can depend on the egress IP even when the
// same video/client/cookie works on the other family. Only retry that narrow
// response, not explicit account, age, country, bot or HTTP rate-limit errors.
func playerCanRetryIP(pr *playerResponse) bool {
	return pr != nil && strings.EqualFold(strings.TrimSpace(pr.PlayabilityStatus.Status), "UNPLAYABLE") &&
		strings.EqualFold(strings.TrimSpace(pr.PlayabilityStatus.Reason), "This video is not available")
}

// retryUsed belongs to a single player() invocation, so all client profiles and
// visitor refreshes together can add at most one request. Shared clients and
// visitor state are never replaced when a different family succeeds.
func (c *Client) playerOnceWithIPFallback(ctx context.Context, videoID, visitor string, profile playerClientProfile, retryUsed *bool) (*playerResponse, error) {
	var family atomic.Int32
	traceCtx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			host, _, err := net.SplitHostPort(info.Conn.RemoteAddr().String())
			if err != nil {
				return
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return
			}
			if ip.To4() != nil {
				family.Store(4)
			} else {
				family.Store(6)
			}
		},
	})
	pr, err := c.playerOnce(traceCtx, videoID, visitor, profile)
	if err != nil || *retryUsed || !playerCanRetryIP(pr) || !c.directFallbackAllowed(innerTubeBaseVideo+"/player") {
		return pr, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Reuse the direct-only family clients already used by search. Use the
	// actual connection, not prefer_ipv6: the initial dial may have fallen back.
	var alternate *http.Client
	var from, to string
	switch family.Load() {
	case 4:
		alternate, from, to = c.searchIPv6Client, "ipv4", "ipv6"
	case 6:
		alternate, from, to = c.searchIPv4Client, "ipv6", "ipv4"
	}
	if alternate == nil {
		return pr, nil
	}
	*retryUsed = true
	if c.logger != nil {
		c.logger.Info("youtubemusic: unavailable player; trying alternate IP family", "video_id", videoID, "client", profile.context().Client.ClientName, "from", from, "to", to)
	}
	retried, retryErr := c.playerOnceWithClient(ctx, alternate, videoID, visitor, profile)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if retryErr != nil {
		if c.logger != nil {
			// Do not expose request URLs, cookies or visitor tokens in logs.
			c.logger.Warn("youtubemusic: alternate player failed", "video_id", videoID, "family", to, "error_type", fmt.Sprintf("%T", retryErr))
		}
		return pr, nil
	}
	playable := hasDirectAudio(retried)
	if c.logger != nil {
		c.logger.Info("youtubemusic: alternate player completed", "video_id", videoID, "family", to, "direct_audio", playable)
	}
	if playable {
		return retried, nil
	}
	// An unsuccessful alternate route must not discard existing metadata or
	// replace the primary failure; the original profile fallback can continue.
	return pr, nil
}
