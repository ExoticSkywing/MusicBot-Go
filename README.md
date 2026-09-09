# MusicBot-Go

多平台音乐下载 / 分享的 Telegram Bot。发链接或搜索即可下载音乐、歌词与封面，带缓存、限流和插件化扩展。

> 基于 [XiaoMengXinX/Music163bot-Go](https://github.com/XiaoMengXinX/Music163bot-Go) 重构，改为插件化架构以支持多平台。仓库原有代码采用 GPL-3.0；`plugins/musiclib/internal` 中移植的上游代码采用 AGPL-3.0-only，详见下方说明。

## 支持平台

| 平台 | 下载 | 搜索 | 歌词 | Hi-Res / 无损 | 识曲 |
|------|:--:|:--:|:--:|:--:|:--:|
| 网易云音乐 | ✓ | ✓ | ✓ | ✓ | ✓ |
| QQ 音乐 | ✓ | ✓ | ✓ | ✓ | — |
| 酷狗音乐 | ✓ | ✓ | ✓ | ✓ | — |
| 酷我音乐 | ✓ | ✓ | ✓ | ✓ | — |
| 汽水音乐 | ✓ | ✓ | ✓ | ✓ | — |
| 咪咕音乐 | ✓ | ✓ | ✓ | 依音源 | — |
| 千千音乐 | ✓ | ✓ | ✓ | 依音源 | — |
| 5sing | ✓ | ✓ | ✓ | — | — |
| Jamendo | ✓ | ✓ | — | 依音源 | — |
| JOOX | 受限 ⁴ | ✓ | ✓ | — | — |
| 哔哩哔哩 | ✓ | ✓ | ✓ | ✓ ¹ | — |
| Apple Music | ✓ | ✓ | ✓ | ✓ ² | — |
| YouTube Music | ✓ | ✓ | ✓ | — | — |
| Spotify | ✓ ³ | ✓ | ✓ | — | — |

¹ 哔哩哔哩取 Dash 流里的 FLAC / Dolby 音轨（音频 id 30251 / 30250 / 30280），视稿件是否提供。

² Apple Music 的 AAC 256k 开箱即用；无损 / Hi-Res / Atmos 需额外的解密服务，见 [Apple Music 无损](#apple-music-无损hi-resatmos)。

³ Spotify 下载需要 `sp_dc` 加自备的 Widevine L3 设备文件（仓库不内置），上限 AAC 256k；只配 Web API 时仅提供搜索与元数据。

⁴ JOOX 的旧详情接口已失效，插件回退到官网页面解析。当前匿名抽测只获得试听片段，插件会拒绝将其作为完整歌曲；仅在官网明确提供完整音频时返回下载地址，完整下载尚未实测确认。

咪咕、千千、5sing、Jamendo 和 JOOX 的实现移植自
[guohuiyuan/music-lib](https://github.com/guohuiyuan/music-lib/tree/3b22e851f4fa2f55ceab943fa846a71536fed4f9)，支持歌曲和集合链接；5sing 不提供专辑，Jamendo 上游不提供歌词。这些第三方公开接口、地区限制和账号权益可能变化，表格描述的是当前适配器能力，不代表所有接口或歌曲都已在每个地区实时验证。实现、验证方式和已知限制见 [music-lib 平台移植说明](plugins/musiclib/README.md)。

`plugins/musiclib/internal` 保留上游 AGPL-3.0-only 许可证及来源声明；仓库其余已有文件继续使用各自原许可证。分发或通过网络提供组合程序时，还需遵守该 AGPL 组件适用于组合程序的网络源码提供要求，许可证文本见 [plugins/musiclib/internal/LICENSE](plugins/musiclib/internal/LICENSE)。

## 快速开始

### Docker（推荐）

镜像由 CI 自动构建并推送到 GHCR：

- `ghcr.io/liuran001/musicbot-go:latest` —— 含精简版 ffmpeg（仅运行时所需共享库），支持 `/recognize` 听歌识曲。识曲指纹编码已用纯 Go（wazero + afp.wasm）实现，无需 Node.js。

所有运行数据（配置、数据库、缓存、脚本）放在一个挂载目录里：

```bash
mkdir -p docker-data
cp config_example.ini docker-data/config.ini
# 编辑 docker-data/config.ini，至少填 BOT_TOKEN

docker run -d --name musicbot-go --restart unless-stopped \
  -w /app/workdir -v "$(pwd)/docker-data:/app/workdir" \
  -e TZ=Asia/Shanghai \
  ghcr.io/liuran001/musicbot-go:latest -c /app/workdir/config.ini
```

或用仓库自带的 `docker-compose.yml`（本地构建）：

```bash
docker compose up -d --build
```

> 不需要识曲时，建议在配置里显式 `EnableRecognize = false`。

### 裸机运行

需要 Go 1.26+ 和 ffprobe（所有平台发送音频前均需完整性校验）；用 `/recognize` 还需 ffmpeg（识曲指纹编码已用纯 Go 实现，无需 Node.js）。Docker 镜像已内置 ffmpeg / ffprobe。

```bash
go build -o MusicBot-Go
./MusicBot-Go -c config.ini
```

## 配置

复制 `config_example.ini` 为 `config.ini`，按注释填写。最少只需一个 Bot Token：

```ini
BOT_TOKEN = YOUR_BOT_TOKEN   # 必填
BotAdmin  = 123456789        # 管理员 Telegram ID（逗号分隔），管理命令需要
```

各平台凭证写在对应的 `[plugins.<name>]` 段，例如：

```ini
[plugins.netease]
music_u = YOUR_MUSIC_U_COOKIE      # 网易云无损需要

[plugins.qqmusic]
cookie = YOUR_QQMUSIC_COOKIE       # 高音质 / Hi-Res 需要

[plugins.applemusic]
media_user_token = YOUR_TOKEN      # 登录 music.apple.com 后从浏览器 Cookie 复制

[plugins.spotify]
sp_dc    = YOUR_SP_DC_COOKIE       # 下载需要；另需自备 .wvd（见下方注释）
wvd_path = /path/to/device.wvd     # Widevine L3 设备文件，仓库不内置
```

酷我、汽水、哔哩哔哩和 YouTube Music 支持匿名访问，但不保证每首歌曲均可取得完整音频；YouTube Music 配置 Cookie 可解锁 256k 并降低限流概率。咪咕、千千、5sing、Jamendo 和 JOOX 也允许在各自配置段中填写可选 Cookie，实际可用性取决于地区、歌曲权益和第三方接口状态。

所有平台均拒绝试听片段：已知试听标记会在解析时拦截，下载后还会遍历实际音频包并与目录完整时长对照。时长缺失、明显不符或无法完成校验时停止发送，不会把试听文件作为降级结果。升级前未通过该校验的音频缓存会自动重新下载校验，正常短歌曲不按固定时长拒绝。

完整选项（并发、缓存、限流、代理、日志、各平台细节等）见 `config_example.ini` 的注释，每一项都有说明。

> 支持运行时 Cookie 导入的平台可以使用管理员命令 `/login <平台> cookie <cookie>`（会回写 `config.ini`）。新增的五个 music-lib 平台暂不实现运行时登录；请在对应 `[plugins.<name>]` 段填写 Cookie 后执行 `/reload`。
>
> 现有配置只要包含任意 `[plugins.*]` 段，程序便只加载显式列出的插件。升级后需要把希望启用的 `[plugins.migu]`、`[plugins.qianqian]`、`[plugins.fivesing]`、`[plugins.jamendo]`、`[plugins.joox]` 段加入原配置；完整示例见 `config_example.ini`。

## 命令

**通用命令**

| 命令 | 说明 |
|------|------|
| `/music <URL 或关键词>` | 下载音乐；直接发音乐链接也会自动识别下载 |
| `/search <关键词>` | 搜索并选择下载 |
| `/lyric <URL>` | 获取歌词 |
| `/fav` | 收藏歌曲 / 查看收藏列表 |
| `/recognize` | 回复一条语音消息识别歌曲（需 `EnableRecognize`） |
| `/settings` | 默认平台、音质与歌词格式（支持私聊 / 群聊维度） |
| `/status` | 查看统计与各平台账号状态 |
| `/queue` | 查看当前下载、发送和 Telegram API 队列 |
| `/cancel` | 取消自己正在进行的下载与发送 |
| `/about` · `/help` | 关于 / 帮助 |

也支持 Inline 模式（`@bot 关键词`）和直接粘贴链接。命令菜单、帮助文本和 Bot 简介按客户端语言本地化，内置简体中文、English、日本語、Русский，可用 `[bot_profile.<lang>]` 段覆盖名称与简介。

**管理员命令**（需在 `BotAdmin` 中）

| 命令 | 说明 |
|------|------|
| `/login <平台> cookie <cookie>` | 导入平台 Cookie |
| `/login kugou qr` | 扫码登录酷狗概念版 |
| `/login <平台> check` · `/login check` | 检查单个 / 全部平台账号 |
| `/login <平台> renew` · `/login renew` | 手动续期 |
| `/login <平台> auto on\|off\|status [秒]` | 自动续期开关 |
| `/login applemusic lang [语言]` | 查看 / 设置 Apple Music 账号目录的回退语言 |
| `/reload` | 重载配置与动态脚本插件 |
| `/rmcache <平台>\|all` | 清除 Telegram 文件 ID 缓存（不操作临时媒体目录） |
| `/wl add\|del\|list [chatID]` | 白名单管理（需 `EnableWhitelist = true`） |

## Apple Music 无损（Hi-Res/Atmos）

Apple Music 歌曲的歌名、歌手和专辑名优先跟随机器人当前语言（中文、英语、日语、俄语），从对应地区的公开目录查询等价歌曲；无需额外地区账号或会员。找不到匹配时保留原信息。播放 ID、下载账号和歌词请求不变。

音频缓存也按语言保存：已有目标语言版本时直接复用；目标语言元数据与原文件缓存的歌名、歌手、专辑完全一致时，只补充缓存语言标记并复用原 FileID，不取回或上传文件。名称有变化时，优先从 Telegram 取回音频，只重写歌名、歌手和专辑标签，再上传为新的语言版本。取回或校验失败才从 Apple 重新下载。原语言版本保留，歌词、封面和其他标签不改动，不转码；重写前后会校验时长和音频数据哈希。旧缓存按需处理，不会批量重下载。

Apple Music 的解密分两档：

- **AAC 256k —— 开箱即用。** 插件内置原生 Go Widevine 解密，填好 `media_user_token` 即可，无需任何额外服务。
- **无损 ALAC / Hi-Res 24bit / Dolby Atmos —— 需要外部 wrapper。** 这些音质走 FairPlay，Apple 不对 Widevine 放行，必须经
  [WorldObservationLog/wrapper](https://github.com/WorldObservationLog/wrapper) 解密。请求高于 AAC 的音质时若 wrapper 不可用，会自动回退到 AAC 256k。

启用无损（Docker）：

1. **构建 wrapper 镜像。** 上游不发布镜像，仓库提供了手动工作流：进入 GitHub → Actions → **Build Apple Music Wrapper Image** → Run，它会从上游 Release 取预编译二进制打包并推到 `ghcr.io/<你的用户名>/musicbot-wrapper`（仅 x86_64）。
2. **登录 wrapper。** 在 `docker-compose.yml` 的 `wrapper` 服务里填一个**有订阅的 Apple ID**（`USERNAME` / `PASSWORD`）。它模拟安卓客户端，登录是设备级的，**无法复用 bot 的 `media_user_token`**——两套独立凭证，都要有。首次启动会自动登录（含 2FA），会话持久化到挂载卷，之后可清空账密。
3. **指向 wrapper。** 在 `config.ini` 设 `wrapper_host = wrapper`（compose 服务名），`docker compose up -d`。

> 2FA：首次启动后看 wrapper 日志，出现 `Waiting for input...` 时把收到的 6 位验证码写入挂载目录的 `data/com.apple.android.music/files/2fa.txt`（60 秒内）。
>
> **裸机**：自行运行 wrapper（见其仓库），把 `wrapper_host` 指向它的地址（如 `127.0.0.1`，端口 10020/20020/30020）。

## 插件开发

两种方式：

- **动态脚本插件**（无需重新编译）：源码放 `plugins/scripts/<name>/`，在 `config.ini` 加 `[plugins.<name>]` 段，管理员 `/reload` 即可热加载。最小入口见 [`plugins/scripts/README.md`](plugins/scripts/README.md)。
- **静态插件**（编译进二进制，能力最全）：实现 `platform.Platform` 接口并注册，见 [`plugins/README.md`](plugins/README.md)。

架构设计见 [`ARCHITECTURE.md`](ARCHITECTURE.md)。
