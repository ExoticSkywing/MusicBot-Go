# 酷我四档联网实测（2026-10-03）

在本机使用 MusicBot-Go 的公开 `Client.GetDownloadInfo` 入口及项目下载器实测陈慧娴《早机》（RID `246052537`，目录时长 274 秒）。新建客户端，没有读取账号配置；匿名音源接口不携带账号 Cookie。四档均完整下载，再执行 `VerifyFullAudio`、`ffprobe` 和 `ffmpeg -v error -xerror -i FILE -f null -`。

## 最终结果

| 请求档位 | 实际音源请求 | 返回标记 | 下载文件音频参数 | 文件字节数 | 完整性检查时长 |
| --- | --- | --- | --- | ---: | ---: |
| 高品 / standard | `128kmp3` | standard | MP3 128 kbps，44.1 kHz，双声道 | 4,385,847 | 274.072024 秒 |
| 超品 / high | `320kmp3` | high | MP3 320 kbps，44.1 kHz，双声道 | 10,964,316 | 274.072024 秒 |
| 无损 / lossless | `2000kflac` | lossless | FLAC 24-bit / 48 kHz，双声道 | 53,907,591 | 274.024638 秒 |
| Hi-Res / hires | `4000kflac` | hires | FLAC 24-bit / 96 kHz，双声道 | 101,237,682 | 274.027849 秒 |

四次音源解析均仅请求表中对应的一个档位，没有回退或调用旧 FLAC 接口。四份文件均通过完整时长检查及 FFmpeg 全文件解码，退出码为 0。FLAC 字节数为项目下载器移除酷我尾封装后的最终大小，与 MusicDownloader 的同曲验证记录一致。测试文件放在 Go 测试临时目录，测试结束后自动清理；不记录签名 URL。

## 联网发现并修复的问题

第一轮中，两档 FLAC 已成功；两档 MP3 被歌曲目录的 `isListenFee=true` 提前拦截，根本没有发起音源请求。该曲目录还带有 `listen_fragment="1"`，但直接验证匿名接口确认它实际返回完整的 128k/320k MP3。

现在目录付费／试听标记不再提前阻止移动端 MP3 解析。返回音源仍必须通过 RID、非试听类型、完整时长、实际格式和码率校验；真正的试听和短音频仍拒绝。带这些目录标记且移动端全部失败时，不使用缺少歌曲身份和试听字段的网页 MP3 回退，并保留实际解析失败原因。显示名称未修改。

新增离线回归覆盖带目录付费／试听标记的两档完整 MP3，以及接口真正返回试听或短音频时的拒绝行为。旧的联网端到端测试也改为验证实际音源，而不是假设目录付费标记必然意味着没有完整音源。

## 复现

在 `E:\MusicBot-Go` 中执行（需要 FFmpeg 和 FFprobe）：

```powershell
$env:MUSICBOT_KUWO_QUALITY_LIVE='1'
go test ./plugins/kuwo -run '^TestKuwoFourQualitiesLive$' -count=1 -v -timeout 16m
Remove-Item Env:\MUSICBOT_KUWO_QUALITY_LIVE
```

最终联网测试四个子项全部通过，共约 26 秒。此结果针对本次样本和网络环境，不代表所有歌曲都提供全部四档。
