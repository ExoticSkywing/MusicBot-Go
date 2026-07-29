# 酷我音乐支持实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 为 MusicBot-Go 增加开箱即用的酷我音乐搜索、单曲、真实 FLAC/320/128 音质、同步歌词和歌单支持。

**架构：** 新增静态 `plugins/kuwo` 插件。搜索、详情、歌单和 Web 兜底使用酷我匿名 Web 链路；下载优先使用已现场验证的公开移动端 `mobi.s` 链路，并通过 RID、时长、试听类型、Range 响应和媒体魔数确认实际音质，失败时按 FLAC → 320 → 128 降级。

**技术栈：** Go 1.26、`net/http`、`net/url`、`encoding/json`、`crypto/rand`、`compress/zlib`、`golang.org/x/text/encoding/simplifiedchinese`、标准库 `testing`/`httptest`。

---

## 文件结构

- 创建 `plugins/kuwo/types.go`：可兼容字符串/数字/布尔的上游 JSON 标量和响应类型。
- 创建 `plugins/kuwo/matcher.go`：单曲、歌单与带平台前缀文本解析。
- 创建 `plugins/kuwo/session.go`：匿名 Web Cookie、逐请求 `Secret`、缓存与一次刷新。
- 创建 `plugins/kuwo/client.go`：有限 HTTP 读取、搜索、详情、Web 播放和歌单。
- 创建 `plugins/kuwo/convert.go`：上游数据到统一平台模型的纯转换。
- 创建 `plugins/kuwo/media.go`：移动播放质量梯队、HTTPS 升级、Range 探测和真实音质判定。
- 创建 `plugins/kuwo/lyrics.go`：增强歌词解码、逐字标记降级与移动歌词回退。
- 创建 `plugins/kuwo/platform.go`：完整实现 `platform.Platform` 和 matcher 接口。
- 创建 `plugins/kuwo/register.go`：插件注册、超时和 API 代理。
- 创建对应的 `*_test.go`：先失败后实现；现网契约单独放在 `e2e_test.go` 并默认跳过。
- 修改 `bot/platform/types.go`：为特定来源增加不可序列化的逐跳下载 URL 校验器。
- 修改 `bot/download/service.go` 及测试：初始候选和每次重定向都执行校验器，并验证 headers 可贯穿 HEAD、Range 和 GET。
- 修改 `bot/telegram/handler/music.go` 及测试：下载 URL 调试日志只保留主机，移除签名路径。
- 修改 `plugins/all/all.go`、`config_example.ini` 和 `README.md` 完成静态接入与用户说明。

### 任务 1：严格输入匹配与弹性响应类型

**文件：**

- 创建：`plugins/kuwo/types.go`
- 创建：`plugins/kuwo/matcher.go`
- 创建：`plugins/kuwo/matcher_test.go`
- 创建：`plugins/kuwo/types_test.go`

- [ ] **步骤 1：编写失败的 matcher 与 JSON 标量测试**

`matcher_test.go` 至少包含：

```go
func TestURLMatcher(t *testing.T) {
	m := NewURLMatcher()
	tests := []struct {
		input, want string
		ok          bool
	}{
		{"https://www.kuwo.cn/play_detail/41378936", "41378936", true},
		{"https://m.kuwo.cn/newh5/singles/songinfoandlrc?musicId=41378936", "41378936", true},
		{"https://www.kuwo.cn.evil.example/play_detail/41378936", "", false},
		{"https://www.kuwo.cn/play_detail/not-a-track", "", false},
	}
	for _, tt := range tests {
		got, ok := m.MatchURL(tt.input)
		if got != tt.want || ok != tt.ok {
			t.Errorf("MatchURL(%q) = %q, %v; want %q, %v", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestPlaylistAndTextMatcher(t *testing.T) {
	m := NewURLMatcher()
	for input, want := range map[string]string{
		"https://www.kuwo.cn/playlist_detail/2952464073": "2952464073",
		"https://m.kuwo.cn/h5app/playlist/2952464073":   "2952464073",
		"https://www.kuwo.cn/web/inventory/share?pid=2952464073&type=2016": "2952464073",
	} {
		if got, ok := m.MatchPlaylistURL(input); !ok || got != want {
			t.Errorf("MatchPlaylistURL(%q) = %q, %v", input, got, ok)
		}
	}
	text := NewTextMatcher()
	if got, ok := text.MatchText("酷我:41378936"); !ok || got != "41378936" {
		t.Fatalf("prefixed text = %q, %v", got, ok)
	}
	if _, ok := text.MatchText("41378936"); ok {
		t.Fatal("bare numeric ID must not match")
	}
	if got, ok := text.MatchText("分享 https://www.kuwo.cn/play_detail/41378936"); !ok || got != "41378936" {
		t.Fatalf("embedded URL = %q, %v", got, ok)
	}
}
```

`types_test.go` 将同一字段分别以 `200`、`"200"`、`true`、`"true"` 和 `null` 解码，断言 `jsonScalar.Int64`、`String`、`Bool` 返回一致的规范值；超大整数、数组和对象必须返回转换失败。

- [ ] **步骤 2：运行测试验证正确失败**

运行：

```bash
go test ./plugins/kuwo -run 'Test(URLMatcher|PlaylistAndTextMatcher|JSONScalar)' -count=1
```

预期：FAIL，包、`NewURLMatcher` 或 `jsonScalar` 尚不存在。

- [ ] **步骤 3：实现 matcher 与弹性类型**

`matcher.go` 使用 `url.Parse`，只接受规范化后的 `kuwo.cn`、`www.kuwo.cn`、`m.kuwo.cn`；ID 必须匹配 `^\d{1,20}$`。文本前缀使用：

```go
var kuwoTextPattern = regexp.MustCompile(`(?i)^\s*(?:kuwo|kw|酷我|酷我音乐)\s*[:：]\s*(\d{1,20})\s*$`)
```

`MatchText` 先匹配该前缀，再从文本中提取第一个 HTTP(S) URL 并委托 `URLMatcher.MatchURL`；仍然不接受裸数字。

`types.go` 以保留原始 JSON 的标量处理上游漂移：

```go
type jsonScalar struct {
	raw json.RawMessage
}

func (s *jsonScalar) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		s.raw = nil
		return nil
	}
	if trimmed[0] == '[' || trimmed[0] == '{' {
		return fmt.Errorf("kuwo: scalar cannot be composite")
	}
	s.raw = append(s.raw[:0], trimmed...)
	return nil
}
```

实现 `String() (string, bool)`、`Int64() (int64, bool)` 和 `Bool() (bool, bool)`，只接受 JSON 字符串、数字和布尔值，拒绝溢出与尾随数据。搜索、详情、播放、移动播放、歌词和歌单响应的 ID、分页、时长、码率、类型、付费状态与 `code` 均使用 `jsonScalar`，不固定为单一 JSON 原生类型。

- [ ] **步骤 4：运行聚焦测试验证通过**

运行：`go test ./plugins/kuwo -run 'Test(URLMatcher|PlaylistAndTextMatcher|JSONScalar)' -count=1`

预期：PASS。

- [ ] **步骤 5：提交**

```bash
git add plugins/kuwo/types.go plugins/kuwo/types_test.go plugins/kuwo/matcher.go plugins/kuwo/matcher_test.go
git commit -m "feat(kuwo): parse inputs and flexible responses"
```

### 任务 2：匿名 Web 会话、搜索与曲目详情

**文件：**

- 创建：`plugins/kuwo/session.go`
- 创建：`plugins/kuwo/session_test.go`
- 创建：`plugins/kuwo/client.go`
- 创建：`plugins/kuwo/client_test.go`
- 创建：`plugins/kuwo/convert.go`

- [ ] **步骤 1：编写失败的签名、会话、搜索和详情测试**

固定签名向量：

```go
func TestBuildSecretFixedVector(t *testing.T) {
	got := buildSecret("0123456789abcdef0123456789abcdef", 12345678)
	const want = "1361b99125125e1ce61cc1f328ded44b38fec3e403cfaa3031b91afbe5e200ea00bc614e"
	if got != want {
		t.Fatalf("buildSecret() = %q, want %q", got, want)
	}
}
```

再用 `httptest.Server` 验证：

- 首页 Cookie 缺失、非 ASCII、含控制字符、短于 16 或长于 128 时失败。
- 有效 Cookie 在 30 分钟内存于 `http.CookieJar`；并发请求在竞态检测下安全。
- 每个签名请求都从 Jar 重新读取当前 Cookie，并生成新的 8 位十进制 nonce；首个签名响应通过 `Set-Cookie` 轮换值后，第二个请求必须使用新 Cookie 生成 Secret。
- 响应非法时清理 Jar 中会话、重新访问首页并仅重试一次。
- 搜索包含 `pn=0`、受限 `rn`、`all` 和搜索页 Referer。
- 搜索 limit 非正数时使用 10，大于 50 时限制为 50；空白查询不发请求并返回空结果。
- 搜索和详情都能兼容字符串/数字 ID、时长和状态；`MUSIC_41378936` 规范为 `41378936`。
- 详情中的 `isListenFee`、`payInfo` 或试听标记被保留到内部 `trackAccess`，供下载任务使用。
- 搜索与详情分别使用完整响应 fixture 做表驱动变体：关键标量覆盖字符串、数字、布尔、`null`、缺失和未知字段；测试必须贯穿 JSON decoder、Track 转换和 `trackAccess`，而不是只调用 `jsonScalar`。

- [ ] **步骤 2：运行测试验证正确失败**

运行：

```bash
go test ./plugins/kuwo -run 'Test(BuildSecret|Session|Search|GetTrack|TrackAccess)' -count=1
```

预期：FAIL，签名、Client 和转换函数尚不存在。

- [ ] **步骤 3：实现逐请求签名与有限 JSON 客户端**

`session.go` 使用既定网页算法：

```go
const (
	secretMultiplier = int64(9253)
	secretIncrement  = int64(23)
	secretModulus    = int64(2147483647)
	secretSeed       = int64(59910100)
	sessionTTL             = 30 * time.Minute
)
```

`buildSecret(cookie string, nonce int)` 按字节异或并在末尾附加 `%08x` nonce。`randomNonce()` 使用 `crypto/rand.Int` 生成 `[10000000, 100000000)` 内的十进制整数。Cookie 只接受 16 到 128 个 ASCII 字母数字字符。

`Client` 包含带 `net/http/cookiejar` 的 API HTTP client、媒体探测 HTTP client、可注入端点、logger、时钟、会话互斥锁和过期时间。`NewClient(timeout, logger)` 使用官方端点；`SetAPIProxy` 替换 transport 时保留或重新附加同一个 Jar。未导出的依赖注入构造函数用于替换 HTTP transport 与端点，而不是向生产 API 添加测试专用方法。

实现：

```go
func (c *Client) SetAPIProxy(cfg httpproxy.Config) error
func (c *Client) Search(ctx context.Context, query string, limit int) ([]platform.Track, error)
func (c *Client) GetTrack(ctx context.Context, trackID string) (*platform.Track, error)
func (c *Client) getTrackDetail(ctx context.Context, trackID string) (*trackDetail, trackAccess, error)
```

JSON 响应通过 `io.LimitReader(body, 4<<20+1)` 读取，超过 4 MiB 拒绝；HTTP 429 映射为 `platform.NewRateLimitedError("kuwo")`。签名请求从 Jar 读取最新 Cookie，添加逐请求 Secret、首页 Referer、UUID v4 `reqId` 和 User-Agent；响应中的 Cookie 轮换交给 Jar 接收。401、403 或 `The request is illegal!` 只触发一次刷新。

`convert.go` 实现 RID 规范化、艺术家拆分、时长解析和统一 Track 转换。曲目 URL 固定为 `https://www.kuwo.cn/play_detail/<id>`。

- [ ] **步骤 4：运行聚焦与竞态测试**

运行：

```bash
go test ./plugins/kuwo -run 'Test(BuildSecret|Session|Search|GetTrack|TrackAccess)' -count=1
go test -race ./plugins/kuwo -run 'TestSession' -count=1
```

预期：全部 PASS，竞态检测无报告。

- [ ] **步骤 5：提交**

```bash
git add plugins/kuwo/session.go plugins/kuwo/session_test.go plugins/kuwo/client.go plugins/kuwo/client_test.go plugins/kuwo/convert.go
git commit -m "feat(kuwo): add signed search and track client"
```

### 任务 3：真实 FLAC、320/128 音质与安全降级

**文件：**

- 创建：`plugins/kuwo/media.go`
- 创建：`plugins/kuwo/media_test.go`
- 修改：`plugins/kuwo/client.go`
- 修改：`plugins/kuwo/client_test.go`
- 修改：`bot/platform/types.go`
- 修改：`bot/download/service.go`
- 创建：`bot/download/service_url_validator_test.go`
- 修改：`bot/telegram/handler/music.go`
- 创建：`bot/telegram/handler/music_url_log_test.go`

- [ ] **步骤 1：编写失败的质量梯队和媒体探测测试**

质量梯队测试：

```go
func TestMobileQualityCandidates(t *testing.T) {
	tests := []struct {
		quality platform.Quality
		want    []string
	}{
		{platform.QualityStandard, []string{"128kmp3"}},
		{platform.QualityHigh, []string{"320kmp3", "128kmp3"}},
		{platform.QualityLossless, []string{"2000kflac", "320kmp3", "128kmp3"}},
		{platform.QualityHiRes, []string{"2000kflac", "320kmp3", "128kmp3"}},
	}
	for _, tt := range tests {
		got := mobileQualityCandidates(tt.quality)
		if gotNames := candidateNames(got); !slices.Equal(tt.want, gotNames) {
			t.Fatalf("quality %v = %v, want %v", tt.quality, gotNames, tt.want)
		}
	}
}
```

媒体测试以自定义 `RoundTripper` 响应官方形状，不断言测试替身本身：

- `2000kflac` 返回请求 RID、213 秒、`.flac` URL；HTTPS Range 返回 206、`Content-Range: bytes 0-41/27383481`、`audio/x-flac` 和 42 字节合法 FLAC+STREAMINFO，预期 `Format:"flac"`、`QualityLossless`、`Size:27383481`、平均码率约 1028，并从采样率/总样本得到与详情一致的实际时长。
- FLAC 只有 `fLaC`、STREAMINFO 长度错误、零采样率/总样本、非法声道/位深或 STREAMINFO 时长与详情不符都必须失败。
- `320kmp3` 与 `128kmp3` 分别返回 8,525,534 和 3,410,341 字节、ID3 后合法 MPEG 帧，预期实际品质与码率正确。
- `320kmp3` 响应若实际总大小只有 3,410,341 字节，计算得到约 128 kbps，必须拒绝该候选并降级，不能报告 High/320。
- 只有 `ID3` 标签、只有同步字、reserved MPEG version/layer/sample-rate、free/bad bitrate index 或 reserved emphasis 的响应必须失败。
- ID3 测试用同步安全整数编码标签大小：令 `offset = 10 + tagSize + footerSize`，断言第二次请求精确为 `bytes=offset-(offset+15)`、206、`Content-Range` 使用相同总长，且响应体恰好 16 字节；offset 超界、总长漂移、截断或多出第 17 字节都失败。
- 请求 Hi-Res 仍返回 `QualityLossless`，不返回 `QualityHiRes`。
- FLAC 候选同 RID、完整时长但实际 MP3 时继续尝试 320；移动 API 网络失败最终调用 Web 128。
- RID 错配，`type` 缺失、`null`、不可转换或非零，1 kbps、11 秒相对 269 秒详情、明确付费状态均立即返回 `ErrUnavailable`，不得降级成试听；字符串 `"0"` 与数字 `0` 是仅有的合法 `type`。
- 免费曲目即使带有 `payInfo`、`feeType`、`pay`、`hasLossless` 或未知字段，只要 `isListenFee=false`、`cannotOnlinePlay=0`、`listen_fragment=0`，仍可下载；三个明确拒绝字段中任一为真则终止。
- HTTP 429、`context.Canceled` 和 `context.DeadlineExceeded` 均立即终止，断言不会请求下一候选或 Web 兜底。
- URL 用户信息、端口、查询、片段、伪造域名、非 `.flac`/`.mp3` 后缀、Range 200、不精确的 `Content-Range`、截断/超长响应、总大小超过 2 GiB、错误媒体结构或缺少总大小均被拒绝。
- 通用下载器在初始 URL 和每一次 30x 重定向前调用 `DownloadInfo.ValidateURL`；重定向到外部主机必须在发出目标请求前失败。
- 媒体探测自身的每一次 30x 重定向也调用同一个格式感知 URL 校验器；外部主机、路径后缀错配和第 11 跳均失败。
- 本地媒体服务器分别走通通用 `DownloadService` 的 multipart（HEAD+并发 Range）与 single fallback（HEAD+GET），断言 `User-Agent`/`Referer` 在初始请求和允许的同源重定向目标上都存在、最终文件字节完整。
- 同一 URL 的两个并发 `DownloadInfo` 使用不同 headers、均无 validator，以及使用不同 validator 的两组测试都执行自己的策略，不能通过 inflight 复用 leader；在 `go test -race` 下通过。
- `downloadURLForLog` 对带签名路径的 URL 只返回 `https://host/[redacted]`，非法 URL 返回 `[redacted]`；`downloadErrorForLog` 将嵌套 `url.Error` 和普通错误字符串里的全部 HTTP(S) URL 替换为 `[redacted-url]`。

- [ ] **步骤 2：运行测试验证正确失败**

运行：

```bash
go test ./plugins/kuwo -run 'Test(MobileQuality|ResolveDownload|ProbeMedia|RejectPreview|ValidateMedia)' -count=1
go test ./bot/download -run 'Test.*(URLValidator|Headers|ConstrainedInflight)' -count=1
go test ./bot/telegram/handler -run 'TestDownload(URL|Error)ForLog' -count=1
```

预期：FAIL，移动播放与媒体探测尚不存在。

- [ ] **步骤 3：实现公开移动端播放契约**

`media.go` 固定使用：

```text
GET https://mobi.kuwo.cn/mobi.s
user=359307055300426
source=kwplayer_ar_5.1.0.0_B_jiakong_vh.apk
type=convert_url_with_sign
sig=0
network=WIFI
f=web
User-Agent: okhttp/3.10.0
```

每个候选补充 `rid`、`br` 与 `format`：

```go
type mobileQuality struct {
	br      string
	format  string
	bitrate int
	quality platform.Quality
}
```

移动 API 的响应体上限为 1 MiB。返回 RID 必须等于请求 ID；规范化后的 `type` 必须存在且严格等于 `0`，缺失、`null`、转换失败或非零均为终止性 `ErrUnavailable`；与详情时长的差值超过 `max(5 秒, 详情时长 × 5%)`、明显付费状态也同样终止。详情访问状态的精确拒绝字段是 `isListenFee=true`、`payInfo.cannotOnlinePlay=true/1` 和 `payInfo.listen_fragment=true/1`；`payInfo` 对象、`feeType`、`pay`、`hasLossless` 和未知字段存在本身不是拒绝条件。仅在 RID、完整时长和访问状态均正常时，格式或码率不符合当前候选才允许继续下一档。移动与 Web 播放都用完整响应 fixture 覆盖关键字段的字符串/数字/布尔/`null`/缺失/未知对象变体和错误 envelope。

候选循环把 HTTP 429 映射为 `platform.NewRateLimitedError("kuwo")` 并立即返回；`context.Canceled` 与 `context.DeadlineExceeded` 也立即原样返回。RID 错配、试听、短时长、明确付费/不可播放、下架以及 Web `code=-1/-1001` 都是终止错误。只有普通传输错误、协议错误、格式/码率不符和空候选地址属于可降级错误；终止错误绝不被后续候选或 Web 兜底掩盖。

媒体 URL 只接受 `kuwo.cn` 下单级标签，标签必须以 `kw-` 开头或以 `-sycdn` 结尾；拒绝用户信息、显式端口、查询与片段。把移动接口的 `http` URL 同源升级为 `https` 后探测，不把完整直链写入日志。

`probeMedia` 使用最终下载所需的 `User-Agent: okhttp/3.10.0` 和 `Referer: https://www.kuwo.cn/`。探测时复制媒体 `http.Client` 并安装格式感知 `CheckRedirect`：每一跳都重新校验 HTTPS、酷我媒体主机与 `.flac`/`.mp3` 后缀，重新附加媒体 headers，并显式在 `len(via) >= 10` 时拒绝；第 11 个目标 handler 不得命中。

FLAC 首探测为 `Range: bytes=0-41`，要求 206、精确 `Content-Range: bytes 0-41/<total>`、`total` 在 `[42, 2<<30]` 内，并通过 `io.LimitReader(resp.Body, 43)` 要求恰好 42 字节。解析 `fLaC` 后的首个元数据块，要求类型 0、长度 34；校验最小/最大 block size、20 位采样率、1–8 声道、4–32 bit 位深、36 位总样本均合法，并要求 `totalSamples/sampleRate` 与详情时长满足相同容差。

MP3 首探测为 `Range: bytes=0-15`，要求 206、精确 `Content-Range: bytes 0-15/<total>`、`total` 在 `[16, 2<<30]` 内，并以 17 字节上限取得恰好 16 字节。MPEG 帧解析验证 11 位同步、非 reserved version/layer/sample-rate、bitrate index 不为 free/invalid、emphasis 非 reserved。若是 ID3v2，则解析 10 字节头里的四字节同步安全标签大小和可选 footer，拒绝非同步安全位或超过 16 MiB 的标签。令 `offset = 10 + tagSize + footerSize`，拒绝 `offset+15 >= total`；请求精确 `Range: bytes=offset-(offset+15)`，要求 206、`Content-Range: bytes offset-(offset+15)/total` 且 `total` 与首响应相同，再以 17 字节上限取得恰好 16 字节并解析完整合法 MPEG 帧头。仅有 `ID3` 或同步字不能通过。所有格式的平均码率：

```go
func averageBitrateKbps(size int64, duration time.Duration) int {
	if size <= 0 || duration <= 0 {
		return 0
	}
	return int(math.Round(float64(size) * 8 / duration.Seconds() / 1000))
}
```

320/128 MP3 的平均码率必须分别落在 `[256,384]` 与 `[102,154]` kbps；不满足时当前候选失败。`DownloadInfo.Bitrate` 使用计算值，不使用移动 API 声明值。

在 `platform.DownloadInfo` 新增：

```go
ValidateURL func(rawURL string) error `json:"-"`
```

`ValidateURL` 必须是可并发调用的无状态纯函数。`DownloadService` 在遍历每个候选、完成 `rewriteNeteaseHost` 后对实际 `baseURL` 调用它，并把校验器和媒体 headers 作为不可变下载策略放入请求 context。`NewDownloadService` 只在构造时安装一次 `http.Client.CheckRedirect`：从 request context 读取策略，每次重定向先执行校验器、重新附加 headers，再显式执行 `len(via) >= 10` 限制；绝不能为单次下载修改共享 client。multipart 的 HEAD、Range 与单 GET 都继承同一 context 和 headers。

当前 inflight 只按初始 URL 聚合，不能安全区分 headers 和函数策略；`len(info.Headers) > 0 || info.ValidateURL != nil` 的下载在完成初始校验后绕过 inflight 聚合，直接执行自己的 `downloadToPath`。本地并发测试分别验证同 URL、不同 headers 且无 validator，以及同 URL、不同 validator 时互不复用；重定向测试覆盖目标未命中、允许目标 headers 贯穿和第 11 跳未命中。不改动生产代码来专门暴露测试方法。

实现统一入口：

```go
func (c *Client) GetDownloadInfo(ctx context.Context, trackID string, quality platform.Quality) (*platform.DownloadInfo, error)
```

先取详情并按上述精确字段拒绝明确付费/试听，再按质量梯队解析移动播放。内部使用可被 `errors.Is` 检查的原因哨兵 `errPaidTrack`、`errPreviewMedia`、`errTrackIdentityMismatch`、`errTrackDurationMismatch`，并同时包装公共 `platform.ErrUnavailable`；这样现网测试能证明拒绝来自明确访问/试听信号，而不会把任意解析失败误当成功。所有非终止候选失败后，Web 兜底固定请求 `128kmp3` 并同样探测 MP3。成功的 `DownloadInfo` 填充 URL、Size、Format、实测 Bitrate、实际 Quality、媒体 headers、格式感知的 `ValidateURL`，并把 `ExpiresAt` 设置为解析时间后 10 分钟。

在 handler 中将原始 `info.URL` 日志参数替换为 `downloadURLForLog(info.URL)`；该纯函数只接受带 host 的 HTTP(S)，并只保留 scheme、host 和固定的 `/[redacted]`。`downloadErrorForLog(err)` 移除错误链文本里的全部 HTTP(S) URL；获取下载信息、实际下载和后台下载失败的日志均使用它，返回给调用方的原始错误不变。

- [ ] **步骤 4：运行聚焦和竞态测试**

运行：

```bash
go test ./plugins/kuwo -run 'Test(MobileQuality|ResolveDownload|ProbeMedia|RejectPreview|ValidateMedia)' -count=1
go test -race ./plugins/kuwo -run 'TestResolveDownload' -count=1
go test ./bot/download -run 'Test.*(URLValidator|Headers|ConstrainedInflight)' -count=1
go test -race ./bot/download -run 'Test.*(URLValidator|ConstrainedInflight)' -count=1
go test ./bot/telegram/handler -run 'TestDownload(URL|Error)ForLog' -count=1
```

预期：全部 PASS。

- [ ] **步骤 5：提交**

```bash
git add plugins/kuwo/media.go plugins/kuwo/media_test.go plugins/kuwo/client.go plugins/kuwo/client_test.go bot/platform/types.go bot/download/service.go bot/download/service_url_validator_test.go bot/telegram/handler/music.go bot/telegram/handler/music_url_log_test.go
git commit -m "feat(kuwo): resolve verified lossless audio"
```

### 任务 4：增强歌词与身份校验回退

**文件：**

- 修改：`plugins/kuwo/client.go`
- 创建：`plugins/kuwo/lyrics.go`
- 创建：`plugins/kuwo/lyrics_test.go`

- [ ] **步骤 1：编写失败的歌词协议测试**

先验证请求明文经循环异或 `yeelion`、标准 Base64 后的固定向量：

```text
228908:
DBYAHlReXEpRUEAeCgxVEgAORRgLG0MXCRgaCwoRAB5UAwEaBAkEBhwaXxcAHVReSAsMAVEkOj0wJjpeW1dXSV1DABsMFkRU

41378936:
DBYAHlReXEpRUEAeCgxVEgAORRgLG0MXCRgaCwoRAB5UAwEaBAkEBhwaXxcAHVReSAsMAVEkOj0wJjpYWFxZQVxWWk8DHBodWF0=
```

通过 `httptest` 断言后一个值原样位于 `r.URL.RawQuery`，末尾 `=` 没有变成 `%3D`。

歌词解码使用完整固定响应夹具，而不是只构造单行信封：

```text
dHA9Y29udGVudA0KcGF0aD1maXh0dXJlDQpscmN4PTENCg0KeJwVy0ELgjAchvEP5CFFCzp4qNHfUlSc+m56myOLkmwFTfv02e2Bh9/pEhSRbSHdgecR3bL9WMg5pQN/d8zugkakrrwPPTAdpbeOcxo0x/Nc4tO/PKjlxTVNbd2ZWYBrgS2rCKjNo2v81FlcJb1JAtAZZbpcoqBr/3dA4nwTo/yNCkazUg4xLmwY/gCBODGK
```

该 Base64 表示的完整响应解密后为：

```text
[kuwo:104]
[ti:Fixture]
[00:00.000]<0,500>好<500,500>运<1000,500>来
[00:02.000]<2000,1000,0>祝你好运来ḿ
```

预期 `Plain == "好运来\n祝你好运来ḿ"`，同步行时间为 0 和 2 秒且不含二元/三元逐字标记；`ḿ` 的 GB18030 编码能防止实现误用 GBK。

移动回退夹具必须带目标身份：

```json
{
  "status": 200,
  "data": {
    "songinfo": {"id": "41378936", "musicrId": "MUSIC_41378936"},
    "lrclist": [
      {"time": "0.5", "lineLyric": "第一句"},
      {"time": "2.0", "lineLyric": "第二句"}
    ]
  }
}
```

增强接口失败或成功解码但没有有效时间行时预期回退。移动请求只能有 `musicId=<RID>`，不能附加 `httpsStatus=1`，也不能携带 Cookie 或 `Secret`。`songinfo.id` 或 `musicrId` 与请求 ID 错配、所有身份均缺失/空、任一非空身份无法规范化、两端均无内容时必须返回 `ErrUnavailable`。移动歌词使用完整 envelope fixture 覆盖 `status`、身份、时间与歌词字段的字符串/数字/`null`/缺失/未知字段变体；一个身份缺失而另一个匹配可接受，所有出现的非空身份都必须一致。

边界测试还要覆盖：多行信封、损坏 zlib/Base64/GB18030、4 MiB 原始响应与 8 MiB 解压上限、LRC 元数据过滤、二元/三元有符号标记、`[offset:毫秒]`、乱序和同时间稳定排序、负数/NaN/Inf/溢出时间、取消/截止时间/429 不触发移动回退。

- [ ] **步骤 2：运行测试验证正确失败**

运行：`go test ./plugins/kuwo -run 'Test(DecodeWordLyrics|GetLyrics|RejectMismatchedLyrics)' -count=1`

预期：FAIL，歌词协议尚未实现。

- [ ] **步骤 3：实现有界解码、逐字降级和回退**

实现：

```go
func buildWordLyricQuery(trackID string) string
func decodeWordLyrics(body []byte) (string, error)
func parseTimedLyrics(raw string) *platform.Lyrics
func (c *Client) GetLyrics(ctx context.Context, trackID string) (*platform.Lyrics, error)
```

请求明文为：

```go
"user=12345,web,web,web&requester=localhost&req=1&rid=MUSIC_" + trackID + "&lrcx=1"
```

请求异或 `yeelion` 后做标准 Base64，并直接赋给 URL 的 `RawQuery`；不得再 URL 编码。响应总读取上限 4 MiB；在首个 `\r\n\r\n` 后对 payload 做 zlib 解压并把输出限制为 8 MiB，再 Base64、异或和严格 GB18030 解码。只移除正则 `<-?\d+,-?\d+(?:,-?\d+)?>`，过滤 `[kuwo:]`、`[ver:]`、`[ti:]`、`[ar:]`、`[al:]`、`[by:]` 等元数据，应用 `[offset:毫秒]`，不写入不兼容的 `RawYRC`、`RawQRC` 或 `RawLYS`。

在 `kuwoEndpoints` 增加可注入的 `wordLyric` 和 `mobileLyric`。歌词 HTTP 请求在 `clientMu` 下取得 API client 快照后复制 client、设置 `Jar=nil`，保留 transport/timeout/代理但不发送会话 Cookie 或 `Secret`。

移动歌词只在增强链普通失败或无有效行时调用，请求只发送 `musicId`。必须把 `songinfo.id` 和 `musicrId` 中出现的每个非空值规范化并与请求 ID 比较，并要求至少一个有效身份；时间以浮点秒解析，拒绝负数、非有限值和 `time.Duration` 溢出，按毫秒转换后稳定排序。调用取消、截止时间和 HTTP 429 立即返回。

- [ ] **步骤 4：运行歌词测试**

运行：`go test ./plugins/kuwo -run 'Test(DecodeWordLyrics|GetLyrics|RejectMismatchedLyrics)' -count=1`

预期：PASS。

- [ ] **步骤 5：提交**

```bash
git add plugins/kuwo/client.go plugins/kuwo/lyrics.go plugins/kuwo/lyrics_test.go
git commit -m "feat(kuwo): decode synchronized lyrics"
```

### 任务 5：歌单、统一平台与插件注册

**文件：**

- 修改：`plugins/kuwo/client.go`
- 修改：`plugins/kuwo/client_test.go`
- 创建：`plugins/kuwo/platform.go`
- 创建：`plugins/kuwo/platform_test.go`
- 创建：`plugins/kuwo/register.go`

- [ ] **步骤 1：编写失败的平台能力和歌单分页测试**

平台能力测试：

```go
func TestPlatformCapabilities(t *testing.T) {
	p := NewPlatform(&Client{})
	if p.Name() != "kuwo" || !p.SupportsDownload() || !p.SupportsSearch() || !p.SupportsLyrics() || p.SupportsRecognition() {
		t.Fatalf("unexpected capability methods")
	}
	got := p.Capabilities()
	if !got.Download || !got.Search || !got.Lyrics || got.Recognition || got.HiRes {
		t.Fatalf("unexpected capabilities: %+v", got)
	}
	meta := p.Metadata()
	if meta.DisplayName != "酷我音乐" || !meta.AllowGroupURL || !slices.Contains(meta.Aliases, "kw") {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
}
```

歌单测试用 `platform.WithPlaylistOffset(ctx, 100)` 和 `platform.WithPlaylistLimit(ctx, 25)`，断言上游收到 `pn=5`、`rn=25`，结果保留总数、当前页曲目、创建者和规范 URL。完整歌单 envelope 与曲目 fixture 覆盖分页、总数、ID、时长和状态的字符串/数字/布尔/`null`/缺失/未知字段变体；无合法 ID 的条目被过滤，而错误 envelope 不能转换成空成功。

再断言：

- `KuwoPlatform.GetDownloadInfo(ctx, id, quality)` 把请求质量传给 Client，并返回 Client 确认的实际品质。
- `KuwoPlatform.GetPlaylist(ctx, id)` 从 context 读取分页后调用四参数 Client helper。
- `GetArtist`、`GetAlbum`、`RecognizeAudio` 满足 `ErrUnsupported`。
- nil Client 的可用能力返回 `ErrUnavailable`，不会 panic。

- [ ] **步骤 2：运行测试验证正确失败**

运行：`go test ./plugins/kuwo -run 'Test(Platform|GetPlaylist)' -count=1`

预期：FAIL，平台适配器与歌单方法尚不存在。

- [ ] **步骤 3：实现歌单 helper 与精确平台接口**

Client helper 的签名：

```go
func (c *Client) GetPlaylist(ctx context.Context, playlistID string, offset, limit int) (*platform.Playlist, error)
```

`limit<=0` 使用 50，限制在 1 到 100；页码为 `offset/limit + 1`。保留总曲数但只返回当前页。

`KuwoPlatform` 必须精确实现：

```go
func (p *KuwoPlatform) GetDownloadInfo(ctx context.Context, trackID string, quality platform.Quality) (*platform.DownloadInfo, error)
func (p *KuwoPlatform) GetPlaylist(ctx context.Context, playlistID string) (*platform.Playlist, error)
```

后者从 context 读取 offset/limit。元数据：

```go
platform.Meta{
	Name:          "kuwo",
	DisplayName:   "酷我音乐",
	Emoji:         "🎧",
	Aliases:       []string{"kuwo", "kw", "酷我", "酷我音乐"},
	AllowGroupURL: true,
	GroupURLHosts: []string{"kuwo.cn", "www.kuwo.cn", "m.kuwo.cn"},
}
```

Hi-Res 能力为 false，因为最高实测是普通 FLAC；无损仍通过实际 `DownloadInfo.QualityLossless` 提供。

`register.go` 读取 `timeout`，非正数使用 20 秒，调用 `NewClient` 和 `SetAPIProxy(cfg.ResolveAPIProxyConfig("kuwo"))`，以 `platformplugins.Register("kuwo", buildContribution)` 注册。

- [ ] **步骤 4：运行插件和竞态测试**

运行：

```bash
go test ./plugins/kuwo -count=1
go test -race ./plugins/kuwo ./bot/platform -count=1
```

预期：全部 PASS。

- [ ] **步骤 5：提交**

```bash
git add plugins/kuwo/client.go plugins/kuwo/client_test.go plugins/kuwo/platform.go plugins/kuwo/platform_test.go plugins/kuwo/register.go
git commit -m "feat(kuwo): add playlists and platform adapter"
```

### 任务 6：静态接入、用户文档与现网契约测试

**文件：**

- 创建：`plugins/kuwo/e2e_test.go`
- 修改：`plugins/all/all.go`
- 创建：`plugins/all/kuwo_registration_test.go`
- 修改：`config_example.ini`
- 修改：`README.md`

- [ ] **步骤 1：编写默认跳过的现网契约测试**

门控：

```go
func requireKuwoE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("KUWO_E2E") != "1" {
		t.Skip("set KUWO_E2E=1 to run live Kuwo contract tests")
	}
}
```

现网测试分别验证：

- 搜索“好运来”至少返回一项 `Platform=="kuwo"`、合法数字 RID、标题（或标题与副标题组合）包含“好运来”的曲目；其规范曲目 URL 必须被 matcher 回解为同一 RID。
- 将 `https://www.kuwo.cn/play_detail/41378936` 经 matcher 得到同一 ID，再调用 `GetTrack`，断言 RID、非空标题与合理时长。
- 免费 `41378936` 请求无损，返回 `QualityLossless`、`flac`、大小大于 20 MiB、码率大于 700 kbps；媒体小 Range 包含合法 STREAMINFO，且采样率/总样本所得时长与详情一致。随后把该 live `DownloadInfo` 原样交给真实 `download.DownloadService` 完整下载，断言字节数与 Size 一致，并执行 `ffmpeg -v error -xerror -i <file> -f null -` 全量解码；`KUWO_E2E=1` 下缺少 ffmpeg 或任一帧损坏都直接 FAIL。
- 同一曲目请求 High/Standard 分别得到 MP3 与实际 320/128 档位。
- 直接调用内部 Web 128 解析入口，强制验证签名 `playUrl` 兜底、实际合法 MP3、128 档位和正常访问语义；再把 Web `DownloadInfo` 原样交给真实 `DownloadService` 完整下载约 3.4 MiB，断言 Size 并在跳过 ID3 后解析合法 MPEG 帧。不能因移动 128 成功而跳过 Web 现网覆盖。
- 付费 `228908` 返回 `ErrUnavailable`，并 `errors.Is` 匹配 `errPaidTrack`、`errPreviewMedia`、`errTrackIdentityMismatch` 或 `errTrackDurationMismatch` 中至少一个，证明不是任意解析退化；绝不返回 11 秒试听。
- 歌单 `2952464073` 先请求 offset 0/limit 2，再通过 `WithPlaylistOffset(ctx, 2)`、`WithPlaylistLimit(ctx, 2)` 请求第二页（按 `pn = offset/limit + 1` 得到 `pn=2`）；断言两次返回 Playlist ID 完全一致、每页最多两个合法数字曲目 ID，且第二页非空并不同于第一页。
- 免费样本歌词非空、含同步行，且 `Plain` 包含稳定关联文本“好运来”；移动回退的 `songinfo.id/musicrId` 身份由离线 fixture 覆盖。

现网子测试共享一个 Client 和 `t.TempDir()`、串行执行且不使用 `t.Parallel()`，复用已解析的 FLAC/Web DownloadInfo，避免为同一验证重复请求或下载。`plugins/all/kuwo_registration_test.go` 是离线测试：断言静态导入后 `platformplugins.Get("kuwo")` 存在，并用空白配置成功创建含 `Platform.Name()=="kuwo"` 的 contribution。

- [ ] **步骤 2：运行默认模式确认全部跳过**

运行：

```bash
go test ./plugins/kuwo -run E2E -count=1 -v
go test ./plugins/all -run KuwoRegistration -count=1
```

预期：E2E PASS 并显示 SKIP、不访问网络；注册测试 PASS。

- [ ] **步骤 3：完成静态注册和配置文档**

在 `plugins/all/all.go` 按字母顺序加入：

```go
_ "github.com/liuran001/MusicBot-Go/plugins/kuwo"
```

在 `config_example.ini` 的平台配置区加入独立行：

```ini
# 酷我音乐（无需 Cookie；公开曲目支持实际 FLAC / 320 / 128 音质）
[plugins.kuwo]
# 是否启用该插件（默认: true）
enabled = true
# 请求超时（秒，默认: 20）
# timeout = 20
# api_proxy_enabled = false
# api_proxy_type = http
# api_proxy_host = 127.0.0.1
# api_proxy_port = 7890
```

README 支持矩阵加入：

```markdown
| 酷我音乐 | ✓ | ✓ | ✓ | 无损 | — |
```

表下说明公开移动端链路会验证并在 E2E 中完整解码 FLAC 无损码流，也会验证实际 320/128；请求 Hi-Res 时最高返回实得 FLAC；服务端只给试听或 RID 错配时拒绝。明确这是媒体容器/码流与完整性验证，不声称证明录音母带来源。

- [ ] **步骤 4：格式化并运行离线验证**

运行：

```bash
gofmt -w plugins/kuwo plugins/all/all.go
go test ./bot/platform ./bot/download ./bot/telegram/handler ./plugins/all ./plugins/kuwo -count=1
go vet ./plugins/kuwo ./plugins/all
go test ./... -count=1
git diff --check
```

预期：全部命令退出码为 0。

- [ ] **步骤 5：运行单次现网契约矩阵**

运行：`KUWO_E2E=1 go test ./plugins/kuwo -run E2E -count=1 -v -timeout=5m`

预期：链接详情、搜索、完整下载并全量解码的 FLAC、移动 320/128、强制 Web 128 完整下载、真实 DownloadService、付费试听拒绝原因、歌单身份/分页和歌词语义全部 PASS。外部服务临时限流时只保留单次脱敏诊断，不用批量请求掩盖失败。

- [ ] **步骤 6：提交**

```bash
git add plugins/kuwo/e2e_test.go plugins/all/all.go plugins/all/kuwo_registration_test.go config_example.ini README.md
git commit -m "docs(kuwo): enable and document the plugin"
```

### 任务 7：最终独立审查与交付验证

**文件：**

- 检查：`plugins/kuwo/*.go`
- 检查：`plugins/all/all.go`
- 检查：`config_example.ini`
- 检查：`README.md`
- 检查：`docs/superpowers/specs/2026-07-30-kuwo-music-support-design.md`

- [ ] **步骤 1：规格与真实性审查**

逐项核对搜索、链接、FLAC/320/128、歌词、歌单、代理、错误和现网测试。生产代码不得把请求档位或响应声明直接当成媒体品质；确认每个下载成功路径都经过 RID、时长、试听类型、官方媒体主机、Range、总大小和 magic 校验。

运行聚焦扫描：

```bash
rg -n 'antiserver|nobb|4000kflac|RawYRC|RawQRC|RawLYS|FIXME|待办' plugins/kuwo
```

预期：生产代码不依赖旧反代或第三方聚合，不报告 Hi-Res，不遗留占位标记；歌词原始格式字段仅可出现在解释其不兼容性的测试中。

- [ ] **步骤 2：执行最终验证矩阵**

运行：

```bash
go test ./plugins/kuwo -count=1
go test -race ./plugins/kuwo ./bot/platform -count=1
go test ./bot/platform ./plugins/all -count=1
go vet ./plugins/kuwo ./plugins/all
go test ./... -count=1
git diff --check
KUWO_E2E=1 go test ./plugins/kuwo -run E2E -count=1 -v
git status --short
```

预期：测试、竞态、vet、格式与现网契约均通过；状态仅允许任务账本等已忽略临时文件。

- [ ] **步骤 3：请求整分支独立审查**

以 `d48cd79` 为 merge base 生成审查包，向最终审查者提供设计、计划、实现报告和完整验证结果。阻断或重要发现由一个修复子智能体集中处理，按 TDD 补充回归测试后重新审查。

- [ ] **步骤 4：确认范围与提交历史**

运行：

```bash
git log --oneline d48cd79..HEAD
git diff --stat d48cd79..HEAD
```

预期：只包含酷我设计、计划、插件、静态注册、配置与 README 变更，没有主工作区 `.omc/` 或其他无关文件。
