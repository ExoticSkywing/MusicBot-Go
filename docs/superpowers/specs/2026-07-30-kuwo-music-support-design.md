# 酷我音乐支持设计

> 状态：历史基线。生产验证后修订的音质选择、候选降级、FLAC
> 边界与完整性合同，以
> [Production Media Reliability Design](2026-07-30-production-media-reliability-design.md)
> 为准；本文与其冲突的旧描述均已被取代。

## 背景

MusicBot-Go 已通过静态插件注册机制接入网易云音乐、QQ 音乐、酷狗音乐等平台，但尚不能识别或处理酷我音乐链接。此次新增一个原生 `kuwo` 插件，在不要求用户登录、不依赖第三方聚合服务、也不绕过付费限制的前提下，为机器人提供可直接使用的酷我音乐能力。

## 目标

- 支持酷我音乐关键词搜索。
- 支持 PC 与移动端的酷我单曲链接，返回曲目元数据。
- 支持无需登录即可完整播放的公开曲目下载，优先取得并验证真实 FLAC 无损音频。
- 支持同步歌词，并在增强歌词接口失效时自动回退。
- 支持 PC、移动端与官方分享形式的歌单链接及分页取曲。
- 遵循现有插件的配置、代理、错误模型、平台元数据与统一下载接口。
- 对所有外部响应、媒体 URL 和资源大小设置明确边界，并通过单元测试、集成测试和可选现网测试验证。

## 非目标

- 不登录酷我账户，不接收或持久化用户提供的酷我 Cookie。
- 不绕过会员、付费、试听片段、地域或版权限制。
- 不把请求档位、详情中的 `hasLossless` 或响应声明值直接当作实际音质；媒体必须通过格式与内容校验。
- 不把移动端 `2000kflac` 档位宣称为恒定 2 Mbps 或 Hi-Res。
- 不实现听歌识曲、歌手详情或专辑详情。
- 不把旧 `antiserver.kuwo.cn` 或第三方聚合 API 作为降级链路。

## 方案比较

| 方案 | 优点 | 风险 | 结论 |
| --- | --- | --- | --- |
| 酷我匿名移动播放链路 | 公开客户端契约；现场可取得真实 FLAC、320 kbps MP3 与 128 kbps MP3 | 依赖固定移动客户端参数；付费曲会返回伪装成成功响应的短试听 | 作为下载主链路 |
| 酷我官方匿名 Web 链路 | 无额外运营方；搜索、详情、歌词和歌单均为结构化响应 | 高音质参数被忽略；详情和播放需要动态匿名会话签名 | 用于元数据并作为 128 kbps 下载兜底 |
| 旧 `antiserver.kuwo.cn` | 请求较简单 | 未文档化且历史实现无法证明真实码率；版权状态和稳定性较弱 | 不采用 |
| 第三方聚合 API | 接入代码短 | 增加隐私、限流、故障、许可和供应链风险；音质声明无法独立核验 | 不采用 |

## 功能边界

插件元数据如下：

- 名称：`kuwo`
- 显示名：`酷我`
- Emoji：`🎧`
- 别名：`kuwo`、`kw`、`酷我`、`酷我音乐`
- 群聊 URL 自动解析：允许，仅限声明的酷我官方主机

能力声明：

- 下载：支持
- 搜索：支持
- 歌词：支持
- 听歌识曲：不支持
- Hi-Res：支持；无损 FLAC：支持

音质按请求选择并按实际结果报告：

- 标准：移动端 `128kmp3`，失败后回退 Web 128 kbps MP3。
- 高品质：移动端 `320kmp3`，失败后依次回退 128 kbps。
- 无损：优先官方 `2000kflac`；实际为双声道 16-bit/44.1–48 kHz
  时报告 `QualityLossless`，若同一官方档实际为 24-bit/44.1–48 kHz，
  则按内容报告 `QualityHiRes`。
- Hi-Res：先走独立 `4000kflac` / `level=hires` 解析，再尝试官方
  `2000kflac`。独立链路只有在实际媒体满足 Hi-Res 校验时才报告
  `QualityHiRes`；全链路均不请求 `jymaster`、`20900kmflac` 或其他
  母带/超分档。
- FLAC 候选不可用后才依次回退 320 和 128 kbps。

免费样本 `41378936` 现场返回的 FLAC 为 27,383,481 字节、213 秒，平均约 1,028 kbps；实现用媒体总大小与曲目时长计算平均码率，不把接口声明的 `2000` 当作真实码率。付费样本 `228908` 会返回另一个 RID 的 11 秒、1 kbps MP3 试听，因此错配媒体本身绝不会交付。可选 Hi-Res 解析器或移动端候选发生 RID 错配时，只丢弃该候选并从详情阶段已验证的原始 RID 继续下一条独立解析链路；身份缺失/非法、明确付费或试听、以及时长明显缩短仍立即返回 `platform.ErrUnavailable`。只有在身份、完整时长和访问状态均正常时，当前候选的格式或码率不符合目标才属于可降级的候选失败。

## 组件设计

新增 `plugins/kuwo`：

- `client.go`：HTTP 请求、匿名会话、签名刷新、移动播放质量选择、响应边界、错误映射。
- `types.go`：酷我搜索、详情、播放、歌词和歌单响应结构。
- `convert.go`：将酷我数据转换为统一 `platform.Track`、`platform.Playlist` 和 `platform.Lyrics`。
- `lyrics.go`：增强歌词请求构造、解压、Base64、异或、GB18030 解码和逐字标记降级。
- `matcher.go`：单曲、歌单和带前缀文本的解析。
- `platform.go`：实现统一 `platform.Platform` 接口。
- `register.go`：读取配置、建立客户端、应用 API 代理并注册插件。

同时修改：

- `plugins/all/all.go`：静态导入 `plugins/kuwo`。
- `config_example.ini`：新增 `[plugins.kuwo]` 示例。
- `README.md`：把酷我音乐加入支持平台表，并说明匿名可取得通过完整解码校验的 FLAC 无损码流、音质降级与付费限制；不把容器/码流验证夸大为对录音母带来源的证明。

## HTTP 与会话

客户端使用配置项 `timeout`，默认 20 秒，并通过 `ResolveAPIProxyConfig("kuwo")` 复用项目现有代理能力。所有请求均继承调用方 `context.Context`。

主要端点：

- 搜索：`https://www.kuwo.cn/search/searchMusicBykeyWord`
- 首页会话：`https://www.kuwo.cn/`
- 曲目详情：`https://www.kuwo.cn/api/www/music/musicInfo`
- 移动播放主链路：`https://mobi.kuwo.cn/mobi.s`
- Web 播放兜底：`https://www.kuwo.cn/api/v1/www/music/playUrl`
- 歌单详情：`https://www.kuwo.cn/api/www/playlist/playListInfo`
- 增强歌词：`https://newlyric.kuwo.cn/newlyric.lrc`
- 移动歌词回退：`https://m.kuwo.cn/newh5/singles/songinfoandlrc`

搜索与移动播放使用公开匿名请求。详情、Web 播放和歌单请求先访问首页，读取 `Hm_Iuvt_cdb524f42f23cer9b268564v7y735ewrq2324` Cookie，再按酷我网页现行算法生成 `Secret` 请求头，并附带 UUID v4 `reqId` 与首页 `Referer`。

匿名会话在内存 Cookie Jar 中缓存 30 分钟，并由互斥锁保护。酷我可能在签名响应中轮换同名 Cookie，因此每个签名请求都必须从 Jar 重新读取当前 Cookie，并生成新的随机 nonce 与 `Secret`；不能缓存或复用旧 `Secret`。若签名接口返回 HTTP 401/403，或响应表示 `The request is illegal!`，客户端清除会话、重新获取一次并仅重试一次；其他失败不进行无界重试。

## 数据流

### 搜索

1. 对查询词去空白，空查询直接返回空结果。
2. 把请求数量限制在 1 到 50；统一层传入非正数时使用 10。
3. 请求搜索端点并限制响应体大小。
4. 忽略缺少合法数字 `rid` 或标题的条目。
5. 将 `MUSICRID`、歌名、歌手、专辑、时长和封面转换为 `platform.Track`。

### 曲目详情与下载

1. 仅接受 1 到 20 位数字曲目 ID。
2. 详情接口返回统一曲目元数据；明确不存在映射为 `ErrNotFound`。
3. 先获取详情，拒绝明确的付费、试听或不可在线播放状态，并记录规范 RID 与完整时长。终止字段只包括 `isListenFee=true`、`payInfo.cannotOnlinePlay=true/1` 和 `payInfo.listen_fragment=true/1`；`payInfo` 对象存在、`feeType`、`pay`、`hasLossless` 或未知字段本身不构成拒绝，以兼容免费曲目的 benign `payInfo`。
4. 按请求音质选择独立解析链路：`QualityHiRes` 先尝试
   `4000kflac` / `level=hires`，`QualityLossless` 和未命中的
   `QualityHiRes` 再请求官方 `2000kflac`；`QualityHigh` 请求
   `320kmp3`，`QualityStandard` 请求 `128kmp3`。
5. 移动端响应必须满足：`code=200`、返回 RID 等于请求 RID、规范化后的 `type` 字段存在且严格等于 `0`、时长与详情的差值不超过 `max(5 秒, 详情时长 × 5%)`、URL 属于严格允许的酷我媒体主机。可选 Hi-Res 或移动候选的 RID 错配只使当前候选失效，下一条解析必须重新使用详情阶段已验证的原始 RID；错配媒体不可交付。`type` 缺失、不可转换或非零，短时长、明确付费/不可播放和下架是终止失败；同 RID、完整时长、正常访问状态下的候选格式不符只是当前候选无效，可以尝试下一档。
6. 将移动端 HTTP 媒体 URL 在同一主机与路径下升级为 HTTPS 后执行有界 Range 探测。探测客户端在每次重定向前复用同一格式对应的 URL 校验器、重新附加媒体 User-Agent/Referer，并显式保留 10 跳上限。FLAC 请求 `bytes=0-41`，响应必须为 206，`Content-Range` 精确覆盖 `0-41`，总大小在 42 字节到 2 GiB 之间，并通过 43 字节读取上限取得恰好 42 字节。除 `fLaC` 外还必须解析首个 34 字节 STREAMINFO：元数据块类型与长度合法，块大小、采样率、声道、位深和总样本均在 FLAC 合法范围内，`总样本/采样率` 与详情时长满足相同容差。
7. MP3 首探测请求 `bytes=0-15`，要求精确 `Content-Range`、16 字节到 2 GiB 的一致总大小，并通过 17 字节读取上限取得恰好 16 字节。若直接以 MPEG 帧头开始，则必须解析并拒绝 reserved version/layer/sample-rate、free/bad bitrate index 和 reserved emphasis；若以 ID3v2 开始，则以同步安全整数解析标签长度（标签最大 16 MiB）及可选 footer。令 `offset = 10 + tagSize + footerSize`，必须满足 `offset+15 < total`；第二次精确请求 `bytes=offset-(offset+15)`，要求 206、`Content-Range` 起止正确且 `total` 与首响应相同，再以 17 字节上限取得恰好 16 字节并解析合法 MPEG 帧头。不能只凭 `ID3` 标签或同步字判断媒体格式。验证失败才尝试下一档。
8. 所有格式都按 `总字节数 × 8 ÷ 时长 ÷ 1000` 计算平均码率。官方 `2000kflac` 的双声道 16-bit/44.1–48 kHz 返回 `QualityLossless`，24-bit/44.1–48 kHz 返回 `QualityHiRes`，并拒绝该选择器上的更高采样率、位深或多声道媒体；独立 Hi-Res 流必须通过其实际内容门槛。320/128 MP3 的实测平均码率必须在目标值上下 20% 内，否则当前候选无效并继续降级。最终 `DownloadInfo.Bitrate` 使用实测值。
9. 若所有移动候选均因普通网络、协议、格式、码率或候选 RID 错配而失败，最终调用 Web `128kmp3`；后续请求始终重建自详情阶段确认的原始 RID，不能沿用错配响应。若身份缺失/非法，或明确识别到付费、试听、短时长、不可播放或下架，则立即返回 `ErrUnavailable`，不继续降级。HTTP 429、`context.Canceled` 和 `context.DeadlineExceeded` 同样立即终止并保留对应错误，不能被后续候选或 Web 兜底掩盖。
10. Web 的 `code=-1`、`code=-1001`、付费标记、试听标记和空 URL 均映射为 `ErrUnavailable`。Web 媒体 URL 必须为 HTTPS、无用户信息/端口/查询/片段、单级 `*-sycdn.kuwo.cn` 主机和 `.mp3` 路径。
11. `DownloadInfo.Headers` 携带已验证请求所用的 User-Agent 与 Referer。`DownloadInfo` 还提供可并发调用的无状态逐跳 URL 校验器；通用下载器在实际改写后的初始候选和每次重定向前执行它，并在允许的重定向上重新附加媒体 headers。只要带非空 headers 或校验器，下载就绕过仅按 URL 聚合的 inflight 去重，避免复用另一请求的凭据、headers 或更宽松策略。
12. 通用下载日志只记录媒体主机与已脱敏路径；解析、探测或下载错误写入日志前移除其中的 URL，不记录移动端签名路径或查询。

### 歌词

1. 首选 `newlyric.kuwo.cn` 增强歌词。
2. 请求明文使用固定异或密钥 `yeelion` 编码并做标准 Base64；结果作为无键的 opaque `RawQuery` 原样发送，不能再用 `url.Values` 或 `QueryEscape` 把 Base64 padding `=` 改写为 `%3D`。
3. 增强响应先验证多行 `tp=content` 信封及首个 `\r\n\r\n` 分隔符，再按 zlib、Base64、异或和严格 GB18030 顺序解码。原始响应与解压输出分别限制为 4 MiB 和 8 MiB。
4. 酷我的 `<开始,时长>` 或 `<开始,时长,标记>` 逐字字段允许有符号整数，但不直接写入项目现有 `RawYRC`、`RawQRC` 或 `RawLYS`，因为格式不兼容。插件只移除这两种精确标记，过滤 LRC 元数据，应用 `[offset:毫秒]` 后保留 `[mm:ss.xxx]` 行时间，构造稳定排序的同步行歌词和纯文本。
5. 增强歌词请求、解码或有效时间行解析失败时，回退到移动歌词 JSON 接口。移动请求只发送 `musicId`，不附加当前会导致业务 `status=301` 的 `httpsStatus=1`。
6. 歌词请求复制 API HTTP client 以继承 transport/代理，但必须设置 `Jar=nil`，且不发送 Cookie 或 `Secret`。调用取消、截止时间和 HTTP 429 立即返回，不继续跨主机回退。
7. 移动响应至少要有一个非空身份字段；`songinfo.id` 与 `musicrId` 中每个出现的非空值都必须能规范化为目标 RID 并完全一致。时间接受字符串或数字浮点秒，拒绝负数、非有限值和溢出值，并稳定排序。
8. 两条链路均无有效歌词、移动身份缺失/非法/错配或业务状态非 200 时返回 `ErrUnavailable`。

### 歌单

1. 从上下文读取 `PlaylistOffsetFromContext` 与 `PlaylistLimitFromContext`。
2. 先把负 offset 归零、把 limit 默认到 50 并限制在 1 到 100，再按酷我的 1 基页码转换；非页边界 offset 通过稳定丢弃首段并在需要时续取下一页，精确返回调用方要求的窗口。
3. 请求签名歌单接口，要求 `code=200`、非空 data 和响应歌单 ID 与请求完全一致；`code=-1` 映射为 `ErrNotFound`，空末页仍是合法成功。
4. 转换标题、`desc→info` 简介回退、`img700→img500→img300→img` 封面、`userName→uname` 创建者、非负总曲数和当前页曲目。曲目中的付费状态不影响浏览，非法 RID 条目被过滤。
5. 保留歌单的总曲数，同时只返回当前分页窗口；Telegram 的 collection lazy-loader 把 `kuwo` 视为分页平台，超过首个 50 首的歌单会继续按页请求。

## 链接与文本匹配

支持下列官方形式：

- `https://www.kuwo.cn/play_detail/<id>`
- `https://m.kuwo.cn/newh5/singles/songinfoandlrc?musicId=<id>`
- `https://www.kuwo.cn/playlist_detail/<id>`
- `https://m.kuwo.cn/h5app/playlist/<id>`
- `https://www.kuwo.cn/web/inventory/share?pid=<id>&type=2016`

主机比较忽略大小写并处理末尾点，但不接受 `kuwo.cn.evil.example` 一类后缀欺骗。文本匹配只接受 `kuwo:<id>`、`kw:<id>`、`酷我:<id>`、`酷我音乐:<id>` 或文本内的已知酷我 URL，不接管裸数字，避免与其他平台冲突。

## 错误与安全边界

- HTTP 429 映射为 `ErrRateLimited`。
- `context.Canceled` 与 `context.DeadlineExceeded` 原样保留并立即终止。
- 资源不存在映射为 `ErrNotFound`。
- 付费、试听、下架、地域限制、空播放地址和无歌词映射为 `ErrUnavailable`。
- 可选 Hi-Res 或移动播放候选返回 RID 错配时丢弃当前候选，随后从详情阶段已验证的原始 RID 重建请求；错配媒体永不交付。身份缺失/非法、`type` 缺失/无效/非零、短时长、明确付费/试听或非官方媒体主机时终止；同 RID、完整时长且访问状态正常的非期望格式或码率只拒绝当前候选并允许降级。
- 不支持的歌手、专辑和识曲接口映射为 `ErrUnsupported`。
- 网络、JSON、解压和编码错误保留原始原因并添加酷我操作上下文。
- Web JSON、移动 JSON、歌词密文和歌词解压结果分别设置 4 MiB、1 MiB、4 MiB 和 8 MiB 的读取上限；媒体探测每次最多读取 17 字节，媒体总大小上限为 2 GiB，防止无界读取、压缩炸弹和码率计算溢出。
- 日志不记录完整 Cookie、`Secret`、媒体签名路径、URL 查询信息或用户查询内容；含 `url.Error` 的解析/下载失败先统一移除 URL 再记录。
- 不向酷我以外的主机发送 Cookie 或 `Secret`。

## 配置

默认配置：

```ini
[plugins.kuwo]
enabled = true
timeout = 20
```

不需要用户凭据。代理继续使用全局 `api_proxy` 与 `[plugins.kuwo.api_proxy]` 的现有解析方式。

## 测试策略

单元测试全部使用 `httptest.Server` 或固定字节夹具，不依赖现网：

- URL、歌单 URL、带前缀文本与恶意主机匹配。
- `Secret` 固定向量、会话缓存、并发访问和非法签名仅重试一次。
- 搜索、详情、移动/Web 播放、付费、试听、限流、非法 JSON 和超大响应的错误映射。
- 128/320 MP3 与真实 FLAC 质量选择、降级顺序、精确 `Content-Range`/响应长度边界、FLAC STREAMINFO 与时长一致性、ID3 标签后完整合法 MPEG 帧头、平均码率计算，以及“320 请求实际返回 128”时的降级。
- 免费曲目携带 benign `payInfo` 时不得误判；`isListenFee`、`cannotOnlinePlay`、`listen_fragment`、身份缺失/非法和短试听必须立即终止；可选解析器或移动候选的 RID 错配必须拒绝该候选、重用原始 RID 继续，但绝不能交付错配媒体。
- 移动 `type` 的缺失、`null`、字符串/数字 `0`、非数值与非零变体；只有规范化后严格等于 `0` 的响应可继续。
- 429、调用方取消和截止时间到期不得继续请求下一档或 Web 兜底。
- 媒体 URL 白名单、HTTP 到 HTTPS 的同源升级、逐跳重定向校验与 SSRF 拒绝；第二个 ID3 Range 必须复用首段总长。
- 解析阶段和通用单线程/分段下载阶段使用相同 headers；允许重定向后的目标请求也保留 headers；以本地媒体服务器分别走通 multipart 与 single fallback。
- 同 URL 的两个并发受约束下载不得跨请求复用 headers 或 URL 校验策略；共享下载器竞态检测通过。
- 各端点的完整 JSON fixture 以字符串/数字/布尔/null/缺失/未知字段变体贯穿 decoder、转换与访问决策，而不只单测 `jsonScalar`。
- 增强歌词完整解码、GB18030、逐字标记移除、移动歌词回退和无歌词。
- 歌单分页、字段转换、缺失条目过滤。
- 平台能力、统一接口和静态注册。

可选现网测试以 `KUWO_E2E=1` 为门控，默认跳过：

- 搜索“好运来”能够返回曲目。
- 官方链接能匹配到免费样本 `41378936`，详情 RID、标题与时长合理；无损请求能取得 HTTPS FLAC，以小范围 `Range` 验证 `audio/x-flac`、STREAMINFO、真实时长和合理总大小，再经真实 `DownloadService` 完整下载并用 `ffmpeg -v error -xerror` 全量解码到 null sink，证明整条 FLAC 码流完整可解码。
- 同一样本的高品质与标准请求分别取得真实 320/128 kbps MP3。
- 强制调用 Web 128 兜底链路并验证 Web 签名、响应、实际 MP3 与正常访问语义，再把该 Web `DownloadInfo` 交给真实 `DownloadService` 完整下载。
- 付费样本 `228908` 必须返回 `ErrUnavailable`，并由内部类型化原因确认来自明确付费或试听/RID/时长信号，而不是任意解析退化。
- 公开歌单 `2952464073` 以非零 offset、小 limit 请求后，返回相同歌单 ID、合法窗口和数字曲目 ID。
- 免费样本能取得非空同步歌词，纯文本包含与曲目稳定关联的“好运来”；移动回退的响应身份由离线 fixture 严格校验。
- 从静态 `plugins/all` 导入后的注册表取得 `kuwo` factory，并用默认配置成功创建 contribution。
- 将现网移动 FLAC 和强制 Web Standard `DownloadInfo` 分别交给真实 `DownloadService`，验证 headers、逐跳策略、最终大小、FLAC 全量解码和 Web MPEG 内容。

最终验收命令：

```bash
go test ./plugins/kuwo -count=1
go test -race ./plugins/kuwo ./bot/platform -count=1
go test ./bot/platform ./plugins/all -count=1
go vet ./plugins/kuwo ./plugins/all
go test ./... -count=1
git diff --check
KUWO_E2E=1 go test ./plugins/kuwo -run E2E -count=1 -v
```

## 参考与许可

- [TuneWeave 酷我客户端](https://github.com/MOPELotus/TuneWeave/blob/82e911f7e8f0a915def93d99419aba7af32fd2c3/crates/tuneweave-provider-kuwo/src/client.rs)，MIT OR Apache-2.0。用于核对当前匿名会话、签名、端点与歌词协议。
- [lx-music-api-server 酷我播放实现](https://github.com/MeoProject/lx-music-api-server/blob/6cf2c74cc7de4f3d05cc423699577457dc754be9/modules/url/kw.py)，MIT。用于交叉核对移动端 `convert_url_with_sign` 参数；音质结论以 2026-07-30 的独立媒体 Range 实测为准。
- [Meting 酷我 Provider](https://github.com/metowolf/Meting/blob/1c2f4c98eed749200d9d7ff5cab329c4308f4268/src/providers/kuwo.js)，MIT。用于比较历史官方 Web 链路。
- [Meting-Agent 旧版实现](https://github.com/ELDment/Meting-Agent/blob/f91472c6636c555f06fa08cc1abebf34dc4b1db6/shared/meting/providers/kuwo.js)，MIT。仅用于评估并否决旧 `antiserver` 方案。

实现借鉴公开协议与现有开源实现，但音质与可用性结论始终以现网媒体内容校验为准，而不是相信请求档位或声明字段。
