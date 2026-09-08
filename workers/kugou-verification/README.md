# 酷狗人工验证 Worker

触发酷狗腾讯图形验证码时，Bot 向请求音乐的用户提供短期链接。用户手动完成官方验证码，Bot 从原服务器提交结果并检查播放是否恢复。页面显示验证通过后，用户回到 Bot 重发音乐请求。

Go 与 Pro 使用同一套协议。Worker 不需要连接 Bot 的入站端口，也不代理音乐下载。

## 数据边界

- 酷狗账号 ID、昵称、Cookie、Token、设备标识及风控事件编号只保存在 Bot 内部，创建任务 API 不接受这些字段。
- Worker 只保存随机管理 ID、公开链接令牌的 SHA-256、公开 CAPTCHA appid、到期时间、状态、测试标记和临时验证结果。公开接口只返回 appid、到期时间、状态及测试标记。
- 链接是 256 位随机令牌，默认 10 分钟有效，提交只接受一次。验证结果由 Bot 读取；处理完成即清除，过期记录由访问检查及每 15 分钟定时清理删除。D1 自身备份保留政策仍由 Cloudflare 控制。
- `BOT_API_SECRET` 是 Bot 与 Worker 的管理密钥，使用 Workers Secret。不要写入仓库、页面或公开链接。请求及应用日志默认关闭，页面使用 `Referrer-Policy: no-referrer`。
- 浏览器使用腾讯官方组件及浏览器验证数据。腾讯会接收完成验证码所需的浏览器信息；页面不读取酷狗登录资料。持有链接的人可完成这一次验证，请勿转发。

## 在自己的 Cloudflare 账户部署

需要 Node.js 24 或更新版本，以及已登录自己账户的 Wrangler。项目不需要安装运行时依赖。

```sh
cd workers/kugou-verification
npm test
wrangler login
wrangler d1 create musicbot-kugou-verification
```

复制 `wrangler.jsonc` 为已忽略的 `wrangler.local.jsonc`，将 `database_id` 换成上一步返回的 ID。需要时更改 Worker 名。执行：

```sh
wrangler d1 migrations apply musicbot-kugou-verification --remote --config wrangler.local.jsonc
wrangler deploy --config wrangler.local.jsonc
wrangler secret put BOT_API_SECRET --config wrangler.local.jsonc
```

将密码管理器生成的至少 32 字符随机密钥输入交互式 Secret 提示。首次发布到写入 Secret 之间，所有管理 API 拒绝访问。在 Bot 私有配置的 `[plugins.kugou]` 中加入：

```ini
verification_relay_url = https://musicbot-kugou-verification.YOUR_SUBDOMAIN.workers.dev
verification_relay_secret = 与 BOT_API_SECRET 相同的随机密钥
```

重启 Bot 或重载插件。URL 必须使用 HTTPS。真实任务仅在出现验证要求时生成；管理员也可创建下述独立测试任务。首页不会泄露待处理任务列表，也不用于发起新的酷狗请求。

使用自定义域名时，在自己的 `wrangler.local.jsonc` 中添加 `"routes": [{"pattern": "mbverify.obdo.cc", "custom_domain": true}]`，再次部署，并将 Bot 的 `verification_relay_url` 设为 `https://mbverify.obdo.cc`。Cloudflare 自动配置域名记录及 HTTPS 证书。域名须属于当前 Cloudflare 账户。

验证接口：`GET /health` 返回 `{"ok":true}`。这只证明 Worker 可访问；完整功能还需要 D1、管理密钥、Bot 和腾讯组件都正常。

## 手动测试

管理员在 Bot 中发送 `/login kugou verify-test`。Bot 回复一个有效期 10 分钟的一次性链接；打开后页面明确显示测试模式，点击开始并手动完成腾讯验证码。页面显示“测试完成，验证码结果已收到。未操作酷狗账号。”后即可关闭。

测试不需要让账号触发风控，不读取或修改酷狗账号，也不会向酷狗提交验证结果。它检查管理员命令、链接生成、浏览器组件及结果回传；测试成功不代表真实账号风控已解除。测试结果不保存 proof，也不进入正常验证协调器。过期后重新发送命令即可。

真实点歌触发时，原音乐请求消息显示“平台需要进行安全验证……”和短期链接。完成验证码后，页面会等待 Bot 向酷狗提交并复查播放；显示验证通过后再回 Bot 重新点歌。

## 协议与验证

Bot 使用 Bearer 密钥创建、轮询和完成任务：

| 接口 | 输入 / 结果 |
| --- | --- |
| `POST /api/challenges` | `{captcha_app_id, expires_in, test_mode?}` → `{id, url, expires_at}`（Unix 秒）；测试标记仅接受布尔值 |
| `GET /api/challenges/:id` | `{status, proof?}`；只有 `submitted` 状态返回 proof |
| `POST /api/challenges/:id/complete` | `{status: "verified" / "failed" / "expired"}`；清除 proof |
| `GET /v/:token/state` | 仅返回 `{captcha_app_id, status, expires_at, test_mode?}`；真实任务省略测试标记 |
| `POST /v/:token/submit` | 同源 JSON `{ticket, randstr, sid, edt}`；原子提交一次 |

普通任务提交后进入 `submitted`，须由 Bot 确认后才能进入 `verified`。测试任务收到格式有效的浏览器回调即进入 `verified`，仅表示回传测试完成，不检查腾讯票据真伪或酷狗接受情况；测试页面使用独立提示。测试模式只能由管理 API 在创建时指定，公开提交无法将真实任务改成测试任务。

`npm test` 使用 Node 内置 SQLite 验证真实 SQL 的原子提交、状态转换、授权、过期与公开数据范围。`node build.mjs` 将静态页面和浏览器 WASM 打包为一个 Module Worker，构建产物不入库。Tencent CAPTCHA 需要人工操作；自动测试不模拟绕过验证码。

## 第三方验证组件

`public/verifycode.js` 与 `public/verifycode_bg_ios.wasm` 来自 [MakcRe/KuGouMusicApi](https://github.com/MakcRe/KuGouMusicApi)，提交 `b8637b73fc54c786186c19d7512815ac5af542e8` 的 `public/verify-pkg/`，原样保留。其 MIT 许可证在 `public/VERIFYCODE-LICENSE.txt`。这里只使用真实浏览器 `EData`，没有引入该项目的模拟环境生成逻辑。腾讯 CAPTCHA SDK 从官方 `turing.captcha.qcloud.com/TCaptcha.js` 加载。

Cloudflare 官方参考：[Module Worker](https://developers.cloudflare.com/workers/runtime-apis/handlers/fetch/)、[D1 prepared statements](https://developers.cloudflare.com/d1/worker-api/prepared-statements/)、[上传元数据](https://developers.cloudflare.com/workers/configuration/multipart-upload-metadata/)。
