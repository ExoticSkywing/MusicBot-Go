# i18n 约定

用户可见文本一律走 catalog，不硬编码。基础设施已就绪，本文是新增/修改文案时要遵守的约定。

## 调用方式

`package handler` 内有两个终端封装（见 `i18n_inject.go`）：

- `tr(ctx, "key")` — 纯文本，绝大多数情况用它。
- `tr(ctx, "key", map[string]any{"Name": x})` — 带模板变量。
- `trMd(ctx, "key", ...)` — 本地化并做 MarkdownV2 转义。仅当整条消息以
  `ParseMode: telego.ModeMarkdownV2` 发送、且该字符串是纯标签（不含有意为之的
  markdown）时使用。如果代码本身在手工拼 markdown 结构并转义动态部分，用 `tr`，
  把结构性 markdown 留在代码里（参考 `about.go` / `texts.go`）。

包级 API 还有 `Localizer.Tn(id, count, ...)` 可做复数，目前没有调用点；需要时在
`i18n_inject.go` 补一个 `trn` 封装即可。

`ctx` 来自每个 `Handle(ctx, b, update)` 并向下传递。路由会把请求的 localizer 注入
ctx，`tr` 因此自动按用户语言渲染。**不要**自己调 `i18n.For` / `i18n.Init`。

### 异步场景的 ctx

- 每个 handler 入口都有 `ctx`，往下传即可。
- 若结构体/闭包的生命周期超过本次请求（异步上传、续期），在创建时捕获 localizer：
  `loc := i18n.From(ctx)`，之后用
  `tr(i18n.WithLocalizer(context.Background(), loc), "key")`。
  参考 `music.go` 的 `uploadTask.loc` / `queuedStatus.loc`。
  内部函数若没有 ctx，就加一个 `ctx context.Context` 参数并更新调用方。

## Catalog 键规则

1. 按领域分片：`bot/i18n/locales/<domain>.<lang>.toml`（现有分片：admin、callback、
   favorite、guest、lyric、playlist、search、settings）。go-i18n 会自动合并
   `locales/*.toml`，**不要**去改 `en.toml` / `zh.toml` / `ja.toml` / `ru.toml` 这四个主
   catalog。
2. 键名 snake_case 并以领域名为前缀，如 `settings_title`、`settings_quality_lossless`。
   键在整个 catalog 内必须唯一，前缀即可保证。
3. 值写**纯文本**，绝不手工转义 MarkdownV2（不要出现 `\.`、`\-`）。转义只在输出边界
   由 `trMd` 完成。动态值用 `{{.Var}}` 占位，通过 map 参数传入。
4. 每个分片的全部语言文件必须有**完全相同**的键集合。支持的语言见
   `i18n.SupportedLanguages`（当前 `en`/`zh`/`ja`/`ru`，`en` 为回退语言，必须完整）。

## 不要本地化的东西

- 日志（`h.Logger.*`）、`fmt.Errorf` 包装的错误。
- 回调 DATA token（如 `"settings platform"`）和协议字面量。
- 只有真正展示给用户的文本才本地化：`SendMessage` / `EditMessageText` /
  `AnswerCallbackQuery` 的 Text、caption、按钮 Text。
- 少数展示字符串同时充当哨兵（被 `strings.Contains` 比对）。遇到与消息文本做比较的
  字符串要当心，不要两边都改成本地化值。

## 改完之后

1. `gofmt -w` 改过的文件。
2. `go build ./...` 必须通过。
3. `go test ./bot/telegram/handler/ ./bot/i18n/` 必须通过。
4. 确认分片键集合在各语言间对齐。
