# 歌词转换约定

平台逐词歌词先转为统一的毫秒时间模型，再输出各目标格式。

- YRC：时间标签在词前，词时间为绝对时间。
- QRC、LYS：时间标签在词后；LYS 从词时间推导行时间。
- KRC：词时间相对行首。导出为真正的二进制文件（krc1 + zlib + XOR），
  Convert 的返回字符串必须原样写入文件，不能作为 UTF-8 文本重新编码。
  逐行输入会生成每行一个时间段；翻译和罗马音依照 Options 写入 language 元数据。
- LRC：支持多个行首时间标签、不同小数精度、空白结束行和 BOM。
  翻译先按最接近的时间匹配，容差 10ms；剩余条目允许 500ms 的最近匹配，
  不重复使用译文，也不将模糊匹配的译文附到制作人员信息上。
- SPL：行首时间兼作首词起点；保留延迟起唱、词间停顿和显式行尾。
- ELRC：补齐末词结束时间，用相邻时间标签保留词间停顿。
- SRT：只有逐词来源时直接使用原始毫秒起止时间，避免先转 LRC 丢失结束时间。
- ASS：将停顿编码为空的卡拉 OK 时间段，按累计时间取整以避免舍入漂移。
- TTML：读取 Apple/AMLL 的绝对歌曲时间、混合文本、嵌套 span、dur、
  时钟时间及 ms/s/m/h 时间单位。词间空格不会因 XML 压缩而消失。
  原始 TTML 导出保持原文；转为其他格式时只取主唱文本。
  此解析器不是通用 TTML 渲染器，不实现相对父元素的时间轴、帧率/tick、
  完整样式、对唱角色和背景人声的跨格式保真转换。
- LQE：有逐词来源时声明 lys，只有逐行来源时声明 lrc。

参考：

- [SPL 官方语法](https://moriafly.com/standards/spl.html)
- [AMLL TTML 规范](https://github.com/amll-dev/amll-ttml-db/blob/main/instructions/ttml-specification-en.md)
- [KRC 解码参考](https://docs.rs/kugou_sdk/latest/src/kugou_sdk/lyric/decode.rs.html)

回归验证：

    go test ./bot/lyric ./bot/platform ./plugins/qqmusic
    go test ./bot/telegram/handler -run 'Lyric|Lyrics'
