# 贡献指南（Contributing）

欢迎向 **MangaAlpha** 提交 Issue 与 Pull Request！为降低沟通与审查成本，开始编码前建议快速浏览本指南与 [AGENTS.md](./AGENTS.md)。

## 提交 PR 的基本原则

1. **一个需求 / 修复对应一个独立 PR**：请勿将多个不相关的新功能揉进同一个巨型 PR，便于逐项审查、自动化测试与按需回滚。
2. **较大功能先开 Issue 对齐**：涉及数据库表结构变更、新外部依赖、鉴权模型或 UI 大改的需求，建议先提 Issue 简单沟通方案再开工。
3. **保持纯 Go（`CGO_ENABLED=0`）**：禁止引入需要 CGO 的依赖，确保 Linux / macOS / Windows 静态编译与 Android `gomobile` 打包不受影响。
4. **遵守多用户与权限隔离红线**：
   - 任何读写个人数据、目录列表、搜索、详情、封面或 SSE 的代码，必须尊重 `database.UserScope`（`user_id`、书库白名单、`allow_nsfw` / `show_nsfw`）；
   - 后台管理与元数据修改接口必须经过 `requireAdmin` 校验；
   - SQLite 迁移必须幂等、事务安全，不破坏既有单管理员旧库原地升级。
5. **关注大库性能**：部分用户的真实书库达到 10 万系列 / 50 万+单册，请避免在列表或搜索热路径引入 N+1 查询或无必要全表回填。
6. **保持前端视觉风格统一**：
   - 必须使用 `internal/httpserver/static/ui/app.css` 中已定义的双主题 CSS 变量（`--bg`、`--bg-card`、`--bg-input`、`--border`、`--text`、`--text-secondary`、`--accent`、`--radius` 等），严禁硬编码写死色值，确保日间（`[data-theme="light"]`）与夜间模式均对比度正常；
   - 优先复用现有组件样式（`.card`、`.btn` / `.btn-primary` / `.btn-sm` / `.btn-danger`、`.badge`、`.field`、`.check-row`、`.table-wrap` + `.data-table`、`openModal()`、`toast()`），不引入外部 CSS 框架、图标库或外部 CDN；
   - 表格外层包裹 `.table-wrap`，确保手机竖屏（`360px ~ 390px`）下无横向溢出或按钮挤压。
7. **不要提交私有数据与编译产物**：
   - 严禁提交真实 115 / Bangumi Token、密码、内网 IP、本机路径或真实私人书库数据；
   - 不要提交 `internal/httpserver/static/reader_dist/` 下的构建产物（如需改动内置阅读器，仅提交 `web/reader/` 源码变更，CI 会自动重编校验）。

## 本地开发与自检命令

提交 PR 前请在本地跑通以下检查（与 GitHub Actions CI 对齐）：

```bash
# 仅在修改了 web/reader/ 时需要重新编译阅读器检查
bash scripts/build-reader.sh

# 前端 JS 语法与脚本单测
node --check internal/httpserver/static/ui/app.js
node --test scripts/*.test.cjs

# Go 格式化、竞态单测、静态检查与跨平台编译校验
gofmt -w cmd internal
test -z "$(gofmt -l cmd internal)"
go test -count=1 -race ./...
go vet ./...
GOOS=windows GOARCH=amd64 go vet ./...
CGO_ENABLED=0 go build -o /tmp/mangaalpha ./cmd/server
```

## 审查与合并说明

- 所有外部 PR 均会经过 CI 自动化测试、代码安全与隔离审查及无头浏览器端到端回归验证；
- 维护者可能会在合并前对格式、边界测试或文案做小幅整理，或将多个已验收 PR 汇入开发分支统一发布下一个版本。
