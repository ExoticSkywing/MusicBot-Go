# 酷我菜单展示名修正实现计划

> **面向 AI 代理的工作者：** 使用 `subagent-driven-development` 执行本计划，并按 TDD 的红灯、绿灯顺序完成。

**目标：** 将平台选择菜单中的酷我展示名从“酷我音乐”缩短为“酷我”。

**架构：** 只修改 `KuwoPlatform.Metadata()` 返回的 `DisplayName`。内部平台名 `kuwo`、输入别名 `酷我音乐`、链接匹配、下载和音质逻辑全部保持不变。

**技术栈：** Go、项目现有 `platform.Meta` 与 Go 测试。

---

### 任务 1：修正菜单展示名

**文件：**

- 修改：`plugins/kuwo/platform_test.go`
- 修改：`plugins/kuwo/platform.go`
- 修改：`docs/superpowers/specs/2026-07-30-kuwo-music-support-design.md`

- [ ] **步骤 1：编写失败的测试**

将 `TestPlatformMetadataAndCapabilities` 的期望展示名改为：

```go
DisplayName: "酷我",
```

其余元数据保持不变，尤其保留：

```go
Aliases: []string{"kuwo", "kw", "酷我", "酷我音乐"},
```

- [ ] **步骤 2：运行测试验证失败**

运行：

```bash
go test ./plugins/kuwo -run '^TestPlatformMetadataAndCapabilities$' -count=1
```

预期：FAIL，实际元数据的 `DisplayName` 仍为“酷我音乐”。

- [ ] **步骤 3：编写最小实现**

在 `plugins/kuwo/platform.go` 中只修改：

```go
DisplayName: "酷我",
```

并将设计文档中的菜单显示名同步为“酷我”；正式平台名称及输入别名仍可写作“酷我音乐”。

- [ ] **步骤 4：运行验证**

运行：

```bash
go test ./plugins/kuwo -run '^TestPlatformMetadataAndCapabilities$' -count=1
go test ./plugins/kuwo ./plugins/all ./bot/telegram/handler -count=1
go test ./... -count=1
go vet ./...
git diff --check
```

预期：全部退出码为 0。

- [ ] **步骤 5：提交**

仅暂存上述三个实现/文档文件与本计划文件，提交：

```bash
git commit -m "fix(kuwo): shorten platform display name"
```
