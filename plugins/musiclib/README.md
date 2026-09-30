# music-lib 平台移植

这个静态插件注册 `migu`、`qianqian`、`fivesing`、`jamendo`、`joox` 五个独立平台，使用 MusicBot 的搜索、链接下载、歌词、歌单和专辑入口。5sing 不提供专辑接口；Jamendo 的上游歌词方法是空实现，因此不暴露歌词能力。歌手页和听歌识曲暂不支持。

已有配置需要加入对应的 `[plugins.<平台>]` 段；完整示例见根目录 `config_example.ini`。每个平台支持 `cookie`、`timeout` 和通用 `api_proxy_*` 设置。Cookie 通过配置文件设置，修改后执行 `/reload`。媒体下载代理仍由全局 `DownloadProxy` 控制。

## 来源与本地改动

接口实现来自 [guohuiyuan/music-lib](https://github.com/guohuiyuan/music-lib/tree/3b22e851f4fa2f55ceab943fa846a71536fed4f9)，固定提交为 `3b22e851f4fa2f55ceab943fa846a71536fed4f9`。移植范围保存在 `internal/`；上游 AGPL v3 许可证保存在 [`internal/LICENSE`](internal/LICENSE)。这些文件保留原许可证，未改动仓库原有 GPL v3 文件的许可证；组合程序须遵守相应 AGPL v3 要求。

选择本地移植是因为上游使用全局 HTTP 客户端，不能接收请求上下文和各平台代理。酷狗另外通过 Go module 引用 music-lib，当前版本为 `v1.1.0`；升级该依赖不会自动更新这里的五个平台，本地移植代码需要单独核对上游并保留上述适配。

2026-10-01 核对上游 `28e1080ba416` 后，补充移植咪咕 `newRateFormats` 与旧音质字段的合并去重、按音质而非文件大小选择版本、Hi-Res 格式识别，以及普通 content ID 的下载解析。目录中的大小与码率对应同一个版本。保留本地请求上下文、代理、试听拒绝和权限判定逻辑；上游 3D 加密音频解密尚未接入，本地仍优先选择可处理的普通音频。

- 每次 Bot 操作创建独立源实例，共享该平台的 HTTP transport，传入请求上下文；超时覆盖整个操作。
- HTTP 请求支持取消、代理和响应大小限制，移除上游默认实例和全局调用入口。
- 咪咕下载保留客户端 transport/timeout 并读取重定向；接口错误或缺失重定向不会变成下载地址。
- 千千只接受完整音频的 `path`，忽略 `trail_audio_info` 试听地址。
- JOOX 的旧详情接口返回 404 时，回退官网页面中的歌曲信息和歌词；去除了上游硬编码账号 Cookie。只有页面明确表明可播放且不是试听时才接受媒体地址。
- 下载返回实际音频格式和可获得的码率；上游选择最佳可用版本，不保证能满足指定无损音质。
- 歌单/专辑返回稳定的歌曲 ID，并遵守 Bot 的分页参数。上游部分集合需要完整读取后分页，大集合可能触发配置的超时。

平台的公开接口、地区可用性和账号权益会变化；接入能力不代表每首歌曲均可匿名下载。千千和 Jamendo 的歌单搜索在上游也标记为不稳定；Bot 当前通过公开集合链接访问它们。

## 验证

不访问外网的接口、路由和转换测试：

```sh
go test -race ./plugins/musiclib/... ./plugins/all
```

使用已有配置进行真实下载回归时，可运行 `plugins/all` 中的可选测试：

```sh
MUSICBOT_LIVE_CONFIG=/private/config.ini MUSICBOT_LIVE_PLATFORMS=migu,spotify \
  go test ./plugins/all -run '^TestLiveConfiguredDownloads$' -v -parallel=1 -timeout=10m
```

需要 ffprobe 和可用的账号配置。测试使用临时配置副本、关闭后台续期，不启动 Bot 或发送 Telegram 消息；会真实下载并校验完整音频，再验证标签读写。服务器运行时建议将线上配置目录只读挂载到独立测试容器，限制内存和 CPU，并在结束后移除容器及测试文件。

可选的真实公开接口检查会搜索三个候选结果、下载完整音频文件，并用生产环境相同的 `ffprobe` 音频包时长校验与目录时长对照。检查不发送 Telegram 消息，不使用账号 Cookie；单个文件上限为 256 MiB，运行时会消耗相应流量和下载时间：

```sh
MUSICLIB_LIVE=1 go test ./plugins/musiclib -run '^TestLivePlatforms$' -v -count=1
```

线上检查失败时需区分接口变化、网络/地区限制和歌曲权益问题；默认测试不依赖这些外部服务。

2026-09-08 的匿名完整下载抽测结果如下，目录时长与 `ffprobe` 累加的音频包时长均通过生产校验：

- 咪咕：5,249,778 字节 MP3，目录 5 分 28 秒，音频包 5 分 28.066 秒。
- 千千：6,623,496 字节 FLAC，目录 50 秒，音频包 50.769 秒。
- 5sing：5,291,407 字节 MP3，目录 2 分 12 秒，音频包 2 分 12.256 秒。
- Jamendo：13,606,952 字节 MP3，目录 8 分 25 秒，音频包 8 分 25.539 秒。

这四个平台均验证了无需 Cookie 的完整歌曲下载。JOOX 官网可匿名提供歌曲信息和歌词，但测试歌曲只提供约 33 秒试听；完整音频下载未验证，线上测试确认该试听会被拒绝。

### 咪咕匿名备用入口（2026-10-01）

Android 入口优先，无地址时尝试 PC v1。匿名 PQ 请求再增加 PC v2/H5 兜底；高音质请求及 ZQ/ZQ24 重试仍先收集，再按音质选择候选。返回地址须通过对应格式的音频头校验；出现试听提示时，CDN 文件大小须达到该档目录大小的 90%，缺少大小证据或明确的试听路径均拒绝。新增匿名入口不发送账号 Cookie、不使用共享 Cookie Jar；取消和证书错误立即中止回退。

`AB CD 01` 响应封装解码参考 [Domdkw/miguMusic-api-enhanced](https://github.com/Domdkw/miguMusic-api-enhanced/tree/97bde9ce79067e436025241dfb8e37fc15e9530c) 的 `url_v2.ts`，经 MusicDownloader 提交 `de6eabd` 适配，MIT 版权声明保存在 [internal/migu/MRC_LICENSE](internal/migu/MRC_LICENSE)。离线回归使用 `go test ./plugins/musiclib/internal/migu`。

可选的备用入口实测在本地拦截较早入口和其他音质候选，曲库、指定备用入口及整曲文件均真实联网，不加载账号或发送 Telegram 消息。需安装 ffprobe/ffmpeg：

```sh
MIGU_FALLBACK_LIVE=1 go test ./plugins/musiclib/internal/migu -run '^TestLiveAnonymousFallbacks$' -v -count=1
```

本轮 PC v2《成都》下载 5,249,778 字节，H5 周杰伦《晴天》下载 4,317,311 字节，均为 128 kbps MP3；音频包时长与目录相符，ffmpeg 全曲解码通过。
