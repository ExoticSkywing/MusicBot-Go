# 第三方音源维护说明

这些适配器只补充 QQ、酷狗、酷我没有会员下载权限时的音频来源。
搜索、登录、歌曲信息、封面和 Telegram 文件缓存仍由原有流程负责。

## 配置边界

- 未配置 Cookie 或使用非会员 Cookie：按需设为 `third_party_first`。
- 有会员下载权限、只需官方流程：设为 `disabled`。新增音源不会收到请求。
- `official_first` 保留为可选策略，不作为会员账号的默认建议。
- 不自动查询会员状态，不依据 Cookie 是否存在推测会员权限。
- 配置 `third_party_providers` 的顺序就是尝试顺序；从列表移除某个源即可停用。
- 每个源受 `third_party_timeout` 和调用方上下文限制；不在适配器内层层重试。

建议来源列表（需要先启用对应模式）：

| 平台 | `third_party_providers` |
| --- | --- |
| QQ、酷狗 | `qqovo,lzmhhh,jbsou` |
| 酷我 | `nxinxz,jbsou` |

已有的 `jbsou` 单源配置继续有效，不自动改写。

## 协议参考与升级定位

参考项目：[CharlesPikachu/musicdl](https://github.com/CharlesPikachu/musicdl)。
本轮核对版本：`07e674a`（2026-09-13）；实测日期：2026-09-18。
这里是独立 Go 适配，不在运行时下载或执行该项目的 Python 代码。

| 本地文件 | musicdl 对应文件及方法 | 维护重点 |
| --- | --- | --- |
| `qqovo.go` | `sources/qq.py`、`sources/kugou.py` 的 `_parsewithqqovoapi` | 匿名 bootstrap、Cookie、签名格式、`url` 字段 |
| `lzmhhh.go` | 同上两文件的 `_parsewithlzmhhhapi` | POST 表单 `id/type`，QQ 为 `qq`、酷狗为 `kg`；`code/data` 字段 |
| `nxinxz.go` | `sources/kuwo.py` 的 `_parsewithnxinxzapi` | `kw.php` 的 `id/level/type` 参数和 `data.url` |
| `jbsou.go` | `common/jbsou.py`；`sources/kugou.py` 的 `_parsewithjbsouapi` | 精确 ID 查询、BOM、媒体重定向 |

上述相对路径位于 musicdl 的 `musicdl/modules/` 目录。
MusicBot-Go 的集成入口只有 `source.go` 中的 provider 注册；平台插件无需新增分支。
通用 JSON、媒体探测和地址校验在 `direct_http.go`，不要把某个站点的签名规则放进去。

有意保留的差异：

- musicdl 在配置 Cookie 后会跳过这些第三方策略；本项目继续以明确配置的模式为准。
- `qqovo` 每次取源使用独立匿名会话，不缓存会话密钥，不接收音乐平台 Cookie。
- `nxinxz` 使用已验证可用的 HTTPS API，而不是参考代码中的 HTTP。
- 只为已验证的 `fs.youthandroid2.kugou.com` 开放 HTTP 音频下载；API 始终使用 HTTPS。
  不关闭 TLS 证书校验，不扩大为任意 HTTP 域名；初始地址、重定向和实际下载都校验。
- 音频探测只读取 1 KB，核对内容和文件大小。质量依据实际返回的格式/文件名判断，
  不把请求的无损或 Hi-Res 直接当成返回质量，也不做跨歌曲 ID 的模糊搜索替换。

## 更新步骤

1. 获取 musicdl 更新，比较上表对应方法，不整体复制它的客户端或全部备用源。
2. 只修改发生协议变化的适配器，补充脱敏响应测试；密钥、Cookie、带签名 URL 不入库。
3. 运行 `go test ./plugins/thirdparty ./plugins/qqmusic ./plugins/kugou ./plugins/kuwo`，
   并运行 `go test -race ./plugins/thirdparty`。
4. 在实际部署网络中验证同一平台歌曲 ID 的解析和完整下载，再构建主机器人镜像。
   样本成功只说明当时可用，不能证明长期稳定；测试不要发送消息或清空现有缓存。
5. 同步本说明中的参考提交和适配差异。接口短暂不可用时，可先从配置列表移除该源。

本轮验收样本：QQ `000gt6Y92Dm4YQ`、酷狗 `4BE1D1AF233AA8519099FCDFAA0E205D`、
酷我 `320577` 为莫文蔚《电台情歌》；酷我 `166731` 为《忽然之间》。
这些是公开歌曲 ID，不含任何账号凭据。
