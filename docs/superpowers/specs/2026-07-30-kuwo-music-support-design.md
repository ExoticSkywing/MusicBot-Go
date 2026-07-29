# 酷我音乐支持设计

## 背景

MusicBot-Go 已通过静态插件注册机制接入网易云音乐、QQ 音乐、酷狗音乐等平台，但尚不能识别或处理酷我音乐链接。此次新增一个原生 `kuwo` 插件，在不要求用户登录、不依赖第三方聚合服务、也不绕过付费限制的前提下，为机器人提供可直接使用的酷我音乐能力。

## 目标

- 支持酷我音乐关键词搜索。
- 支持 PC 与移动端的酷我单曲链接，返回曲目元数据。
- 支持无需登录即可完整播放的公开曲目下载。
- 支持同步歌词，并在增强歌词接口失效时自动回退。
- 支持 PC、移动端与官方分享形式的歌单链接及分页取曲。
- 遵循现有插件的配置、代理、错误模型、平台元数据与统一下载接口。
- 对所有外部响应、媒体 URL 和资源大小设置明确边界，并通过单元测试、集成测试和可选现网测试验证。

## 非目标

- 不登录酷我账户，不接收或持久化酷我 Cookie。
- 不绕过会员、付费、试听片段、地域或版权限制。
- 不宣称或伪造 320 kbps、无损、Hi-Res 等匿名接口实际未提供的音质。
- 不实现听歌识曲、歌手详情或专辑详情。
- 不把旧 `antiserver.kuwo.cn` 或第三方聚合 API 作为降级链路。

## 方案比较

| 方案 | 优点 | 风险 | 结论 |
| --- | --- | --- | --- |
| 酷我官方匿名 Web 链路 | 无额外运营方；搜索、详情、播放和歌单均为结构化响应；可按实际响应报告音质 | 接口未公开承诺稳定；详情和播放需要动态匿名会话签名 | 采用 |
| 旧 `antiserver.kuwo.cn` | 请求较简单 | 未文档化且历史实现无法证明真实码率；版权状态和稳定性较弱 | 不采用 |
| 第三方聚合 API | 接入代码短 | 增加隐私、限流、故障、许可和供应链风险；音质声明无法独立核验 | 不采用 |

## 功能边界

插件元数据如下：

- 名称：`kuwo`
- 显示名：`酷我音乐`
- Emoji：`🎧`
- 别名：`kuwo`、`kw`、`酷我`、`酷我音乐`
- 群聊 URL 自动解析：允许，仅限声明的酷我官方主机

能力声明：

- 下载：支持
- 搜索：支持
- 歌词：支持
- 听歌识曲：不支持
- Hi-Res：不支持

匿名播放接口现场只返回真实的 128 kbps MP3，因此无论用户请求标准、高品质、无损还是 Hi-Res，插件都返回实际的 `QualityStandard`、`128` kbps 和 `mp3`，由现有统一层向用户呈现降级结果。付费内容、试听片段或没有完整播放 URL 的曲目返回 `platform.ErrUnavailable`。

## 组件设计

新增 `plugins/kuwo`：

- `client.go`：HTTP 请求、匿名会话、签名刷新、响应边界、错误映射。
- `types.go`：酷我搜索、详情、播放、歌词和歌单响应结构。
- `convert.go`：将酷我数据转换为统一 `platform.Track`、`platform.Playlist` 和 `platform.Lyrics`。
- `lyrics.go`：增强歌词请求构造、解压、Base64、异或、GB18030 解码和逐字标记降级。
- `matcher.go`：单曲、歌单和带前缀文本的解析。
- `platform.go`：实现统一 `platform.Platform` 接口。
- `register.go`：读取配置、建立客户端、应用 API 代理并注册插件。

同时修改：

- `plugins/all/all.go`：静态导入 `plugins/kuwo`。
- `config_example.ini`：新增 `[plugins.kuwo]` 示例。
- `README.md`：把酷我音乐加入支持平台表，并说明匿名 128 kbps 与付费限制。

## HTTP 与会话

客户端使用配置项 `timeout`，默认 20 秒，并通过 `ResolveAPIProxyConfig("kuwo")` 复用项目现有代理能力。所有请求均继承调用方 `context.Context`。

主要端点：

- 搜索：`https://www.kuwo.cn/search/searchMusicBykeyWord`
- 首页会话：`https://www.kuwo.cn/`
- 曲目详情：`https://www.kuwo.cn/api/www/music/musicInfo`
- 播放地址：`https://www.kuwo.cn/api/v1/www/music/playUrl`
- 歌单详情：`https://www.kuwo.cn/api/www/playlist/playListInfo`
- 增强歌词：`https://newlyric.kuwo.cn/newlyric.lrc`
- 移动歌词回退：`https://m.kuwo.cn/newh5/singles/songinfoandlrc`

搜索使用公开匿名请求。详情、播放和歌单请求先访问首页，读取 `Hm_Iuvt_cdb524f42f23cer9b268564v7y735ewrq2324` Cookie，再按酷我网页现行算法生成 `Secret` 请求头，并附带 UUID v4 `reqId` 与首页 `Referer`。

匿名会话在内存中缓存 30 分钟，并由互斥锁保护。若签名接口返回 HTTP 401/403，或响应表示 `The request is illegal!`，客户端清除会话、重新获取一次并仅重试一次；其他失败不进行无界重试。

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
3. 播放接口固定请求 `br=128kmp3`。
4. `code=-1`、`code=-1001`、付费标记、试听标记、空 URL 均映射为 `ErrUnavailable`。
5. 媒体 URL 必须是 HTTPS、不得含用户信息、显式端口、查询或片段，主机必须为 `*.kuwo.cn` 下以 `-sycdn` 结尾的单级子域名，路径必须以 `.mp3` 结尾。
6. 返回的 `DownloadInfo` 始终如实标记为 MP3、128 kbps、标准音质；不预先声称未知文件大小。

### 歌词

1. 首选 `newlyric.kuwo.cn` 增强歌词。
2. 请求参数使用固定异或密钥 `yeelion` 编码；响应按 `tp=content` 信封、zlib、Base64、异或和 GB18030 顺序解码。
3. 酷我的 `<开始,时长>` 逐字标记不直接写入项目现有 `RawYRC`、`RawQRC` 或 `RawLYS` 字段，因为格式不兼容。插件移除逐字标记，保留 `[mm:ss.xxx]` 行时间，构造同步行歌词和纯文本。
4. 增强歌词请求或解码失败时，回退到移动歌词 JSON 接口。
5. 两条链路均无有效歌词时返回 `ErrUnavailable`。

### 歌单

1. 从上下文读取 `PlaylistOffsetFromContext` 与 `PlaylistLimitFromContext`。
2. 将偏移量转换为酷我页码，并把单次页大小限制在 1 到 100。
3. 请求签名歌单接口，转换标题、简介、封面、创建者、总曲数和当前页曲目。
4. 保留歌单的总曲数，同时只返回当前分页窗口，避免一次加载超大歌单。

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
- 资源不存在映射为 `ErrNotFound`。
- 付费、试听、下架、地域限制、空播放地址和无歌词映射为 `ErrUnavailable`。
- 不支持的歌手、专辑和识曲接口映射为 `ErrUnsupported`。
- 网络、JSON、解压和编码错误保留原始原因并添加酷我操作上下文。
- JSON 和歌词响应分别设置有限读取上限；解压后的歌词再次设置上限，防止压缩炸弹。
- 日志不记录完整 Cookie、`Secret`、媒体 URL 查询信息或用户查询内容。
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
- 搜索、详情、播放、付费、限流、非法 JSON 和超大响应的错误映射。
- 媒体 URL 白名单与 SSRF 拒绝。
- 增强歌词完整解码、GB18030、逐字标记移除、移动歌词回退和无歌词。
- 歌单分页、字段转换、缺失条目过滤。
- 平台能力、统一接口和静态注册。

可选现网测试以 `KUWO_E2E=1` 为门控，默认跳过：

- 搜索“好运来”能够返回曲目。
- 免费样本 `41378936` 能取得 HTTPS MP3，并用小范围 `Range` 请求验证 `audio/mpeg`。
- 付费样本 `228908` 必须返回 `ErrUnavailable`。
- 公开歌单 `2952464073` 能取得非空分页。
- 免费样本能取得非空同步歌词。

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

- [TuneWeave 酷我客户端](https://github.com/MOPELotus/TuneWeave/blob/325bbd144b5b3ce18b70e6d54463bc8080ee0c52/crates/tuneweave-provider-kuwo/src/client.rs)，MIT OR Apache-2.0。用于核对当前匿名会话、签名、端点与歌词协议。
- [Meting 酷我 Provider](https://github.com/metowolf/Meting/blob/1c2f4c98eed749200d9d7ff5cab329c4308f4268/src/providers/kuwo.js)，MIT。用于比较历史官方 Web 链路。
- [Meting-Agent 旧版实现](https://github.com/ELDment/Meting-Agent/blob/f91472c6636c555f06fa08cc1abebf34dc4b1db6/shared/meting/providers/kuwo.js)，MIT。仅用于评估并否决旧 `antiserver` 方案。

实现只借鉴公开协议和算法行为，不复制受版权保护的大段代码。开源代码许可证不授予音乐内容权利；运行时始终尊重酷我服务端返回的付费与可用性状态。
