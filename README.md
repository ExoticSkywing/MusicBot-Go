# MusicBot-Go

多平台音乐下载与分享的 Telegram Bot。支持发送链接自动解析或关键词搜索下载歌曲、歌词与专辑封面，具备完整的音质匹配、试听拦截、限流调度与插件化扩展能力。

---

## 目录
- [项目介绍](#项目介绍)
- [平台支持](#平台支持)
- [部署说明](#部署说明)
- [开发说明](#开发说明)

---

## 项目介绍

MusicBot-Go 是一个专为 Telegram 设计的高性能多平台音乐机器人，基于 [XiaoMengXinX/Music163bot-Go](https://github.com/XiaoMengXinX/Music163bot-Go) 进行插件化架构重构，支持对接国内外主流音乐流媒体及开放音乐平台。

### 核心特性
- **多平台聚合**：集成 15+ 主流音乐与音频平台，覆盖检索、直链解析、歌词同步与封面提取。
- **高保真音质**：支持 Standard、High、Lossless (FLAC)、Hi-Res 以及 Dolby Atmos 等规格，智能按平台最优能力提供。
- **音频严格校验**：内置全平台防试听拦截与真实数据包时长/哈希校验机制，彻底杜绝片头试听文件混入。
- **听歌识曲**：基于纯 Go（wazero + WebAssembly）实现音频指纹编码，无需额外依赖 Node.js 运行时即可提供识别服务。
- **完善的并发与流控**：内置令牌桶限流、任务优先级队列与 Telegram 发送队列，防止 API 触发风控或超时。
- **多语言与本地化**：支持简体中文、English、日本語、Русский 等多语言交互，并可根据语言偏好回退歌曲元数据。
- **双层插件化架构**：支持编译期注入的静态核心插件，以及免重启、热重载的动态脚本插件。

---

## 平台支持

| 平台 | 下载 | 搜索 | 歌词 | Hi-Res / 无损 | 识曲 | 说明 |
|:---|:---:|:---:|:---:|:---:|:---:|:---|
| **网易云音乐** | ✓ | ✓ | ✓ | ✓ | ✓ | 支持 VIP 无损、听歌识曲 |
| **QQ 音乐** | ✓ | ✓ | ✓ | ✓ | — | 支持 Cookie 鉴权及 Hi-Res 解析 |
| **酷狗音乐** | ✓ | ✓ | ✓ | ✓ | — | 支持移动端备用无会话降级与扫码登录 |
| **酷我音乐** | ✓ | ✓ | ✓ | ✓ | — | 自动支持移动端无损备用入口 |
| **汽水音乐** | ✓ | ✓ | ✓ | ✓ | — | 支持匿名访问与完整解析 |
| **咪咕音乐** | ✓ | ✓ | ✓ | 依音源 | — | 移植自 music-lib，增加 PC v2/H5 备用入口 |
| **千千音乐** | ✓ | ✓ | ✓ | 依音源 | — | 移植自 music-lib |
| **5sing 原创** | ✓ | ✓ | ✓ | — | — | 移植自 music-lib |
| **Jamendo** | ✓ | ✓ | — | 依音源 | — | 移植自 music-lib（上游无独立歌词） |
| **JOOX** | 受限 | ✓ | ✓ | — | — | 仅在官方页面提供完整音频时允许下载，杜绝片段 |
| **哔哩哔哩** | ✓ | ✓ | ✓ | ✓ | — | 提取 Dash 流的 FLAC / Dolby 优质音轨 |
| **Apple Music** | ✓ | ✓ | ✓ | ✓ | — | AAC 256k 开箱即用；Hi-Res/Atmos 需搭配解密服务 |
| **YouTube Music** | ✓ | ✓ | ✓ | — | — | 支持匿名解析；配置 Cookie 可解锁 256k 并降低风控 |
| **Spotify** | ✓ | ✓ | ✓ | — | — | 需配置 `sp_dc` 与 Widevine L3 设备文件（`.wvd`） |
| **抖音原声** | ✓ | — | — | — | — | 支持原声与分享短链，提供 128k MP3（过滤片段） |

> **说明事项**：
> 1. **试听拦截**：所有平台均内置试听特征识别与数据包解包校验。若音源仅提供试听切片或时长异常，将自动终止发送，不会将试听片段作为降级结果。
> 2. **账号与音质**：部分平台（如酷我、汽水、B站、YouTube Music）支持匿名访问；配置账号凭证（Cookie/Token）后可解锁高规格音质并提升解析成功率。
> 3. **第三方组件许可**：部分平台适配器移植自 [guohuiyuan/music-lib](https://github.com/guohuiyuan/music-lib)，`plugins/musiclib/internal` 遵循 AGPL-3.0 协议，其余已有代码遵循 GPL-3.0。

---

## 部署说明

### 1. 运行环境准备

- **Docker 部署（推荐）**：仅需安装 Docker 与 Docker Compose。官方镜像已预装精简版 `ffmpeg` 与 `ffprobe`。
- **裸机部署**：
  - Go 1.26.7+
  - `ffprobe`（必选，用于音频时长与完整性硬校验）
  - `ffmpeg`（可选，使用 `/recognize` 识曲功能时必需）

---

### 2. 部署方式

#### 方式 A：Docker 快速启动（推荐）

拉取预构建镜像运行（以挂载外部数据目录为例）：

```bash
mkdir -p docker-data
cp config_example.ini docker-data/config.ini
# 编辑 docker-data/config.ini，填写 BOT_TOKEN 等必要参数

docker run -d \
  --name musicbot-go \
  --restart unless-stopped \
  -w /app/workdir \
  -v "$(pwd)/docker-data:/app/workdir" \
  -e TZ=Asia/Shanghai \
  ghcr.io/liuran001/musicbot-go:latest -c /app/workdir/config.ini
```

#### 方式 B：Docker Compose 编排

使用仓库自带的 `docker-compose.yml` 运行：

```bash
# 1. 准备配置文件
mkdir -p docker-data
cp config_example.ini docker-data/config.ini
# 编辑 docker-data/config.ini

# 2. 启动服务
docker compose up -d
```

#### 方式 C：裸机编译运行

```bash
# 1. 克隆代码并编译
git clone https://github.com/liuran001/MusicBot-Go.git
cd MusicBot-Go
go build -o MusicBot-Go

# 2. 准备配置并运行
cp config_example.ini config.ini
# 编辑 config.ini
./MusicBot-Go -c config.ini
```

---

### 3. 配置说明

复制 `config_example.ini` 为 `config.ini`，核心配置项如下：

```ini
BOT_TOKEN = YOUR_BOT_TOKEN        # 必填，从 @BotFather 获取
BotAdmin  = 123456789             # 管理员 Telegram 账号 ID（多个用英文逗号分隔）
EnableRecognize = true            # 是否启用听歌识曲功能（不需要可设为 false）
```

各平台凭据在 `[plugins.<平台名>]` 节点下按需配置，例如：

```ini
[plugins.netease]
music_u = YOUR_MUSIC_U_COOKIE     # 网易云无损音质凭证

[plugins.qqmusic]
cookie = YOUR_QQMUSIC_COOKIE      # QQ 音乐高音质 / Hi-Res 凭证

[plugins.applemusic]
media_user_token = YOUR_TOKEN     # Apple Music Web 凭证（AAC 256k 即开即用）

[plugins.spotify]
sp_dc    = YOUR_SP_DC_COOKIE      # Spotify 账号 Cookie
wvd_path = /path/to/device.wvd    # Widevine L3 设备私钥文件路径
```

> **提示**：
> - 完整配置字段（并发度、缓存周期、限流阈值、网络代理等）请参阅 `config_example.ini` 中的详细注释。
> - 若需要启用 Apple Music 的 Hi-Res / Dolby Atmos 无损规格，可在 `docker-compose.yml` 中启动 `wrapper` 服务，并将配置中的 `wrapper_host` 指向该服务（普通 AAC 256k 音质无需外部 wrapper，内置原生解密）。

---

### 4. 常用交互命令

**通用命令**：
- `/music <链接/关键词>`：下载单曲或指定搜索词音乐（直接发送平台链接亦可自动解析）。
- `/search <关键词>`：在默认或指定平台中搜索并呈现选择列表。
- `/lyric <链接>`：提取并返回指定歌曲歌词。
- `/recognize`：引用回复一条语音或音频消息执行听歌识曲。
- `/fav`：收藏当前歌曲或管理个人收藏列表。
- `/settings`：配置个人或群组的默认首选平台、音质偏好与歌词格式。
- `/queue`：查看当前等待下载、转码与发送的任务队列。
- `/cancel`：取消当前正在为自己处理中的下载或发送任务。
- `/status`：查看系统负载、统计数据及各平台账号连通状态。

**管理员命令**（仅 `BotAdmin` 授权用户可用）：
- `/login <平台> cookie <cookie>`：动态导入并持久化更新指定平台的 Cookie。
- `/login kugou qr`：扫码登录酷狗概念版。
- `/login <平台> check`：检查各平台账号登录与凭证有效状态。
- `/login <平台> renew`：手动续期平台会话。
- `/login <平台> auto on|off|status`：设置平台自动续期开关。
- `/login applemusic lang [语言]`：查看或设置 Apple Music 回退语言。
- `/reload`：平滑重载配置文件与动态脚本插件。
- `/rmcache <平台|all>`：清理 Telegram File ID 缓存。
- `/wl add|del|list <chatID>`：白名单使用权限管理（需启用 `EnableWhitelist`）。

---

## 开发说明

MusicBot-Go 提供了高度解耦的插件体系，开发者可以方便地添加新平台支持或扩展解析功能：

### 1. 动态脚本插件（免编译）
适用于轻量扩展、第三方解析接入或快速实验：
- 编写脚本文件放置于 `plugins/scripts/<插件名>/` 目录下。
- 在 `config.ini` 中追加 `[plugins.<插件名>]` 配置段。
- 发送管理员指令 `/reload` 即可实时热加载生效，无需停止或重新编译主程序。
- 脚本接口规范与最小示例请参阅 [`plugins/scripts/README.md`](plugins/scripts/README.md)。

### 2. 静态核心插件（原生 Go）
适用于高性能、具备复杂鉴权或原生音视频流处理的平台：
- 在 `plugins/` 下新建模块，实现 `platform.Platform` 接口并调用注册方法注册至核心引擎。
- 支持完整参与生命周期管理、配置映射、会话持久化与状态检测。
- 详细接口定义与适配指引请参阅 [`plugins/README.md`](plugins/README.md)。

### 3. 架构设计与深入开发
- 项目整体架构、并发模型、队列与缓存分层机制请参阅 [`ARCHITECTURE.md`](ARCHITECTURE.md)。
