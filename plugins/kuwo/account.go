package kuwo

import (
	"context"
	"strings"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// AccountStatus reports Kuwo's public official endpoint separately from the
// optional third-party audio source. Kuwo does not require an account for its
// normal audio path.
func (p *KuwoPlatform) AccountStatus(ctx context.Context) (platform.AccountStatus, error) {
	_ = ctx
	status := platform.AccountStatus{
		Platform:        p.Name(),
		DisplayName:     p.Metadata().DisplayName,
		Available:       true,
		NoLoginRequired: true,
	}
	if p == nil || p.client == nil {
		status.Available = false
		status.Summary = "- 状态: 插件未初始化"
		return status, nil
	}
	status.ThirdPartyAudioAvailable = p.thirdPartyAudioAvailable()
	status.Highlights = p.thirdPartyStatusLines()
	status.Summary = strings.Join([]string{"- 状态: 官方音源可用（无需登录）"}, "\n")
	return status, nil
}
