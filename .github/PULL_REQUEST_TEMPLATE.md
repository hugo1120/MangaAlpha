## 变更摘要

<!-- 简明说明本 PR 解决的问题或新增的能力（建议一个 PR 只聚焦一个独立需求或修复） -->

## 主要改动

- 

## 测试与验证

<!-- 说明你本地测试的场景与结果 -->

- [ ] 已运行 `gofmt -l cmd internal` 无未格式化文件
- [ ] 已运行 `go test -count=1 -race ./...` 与 `go vet ./...` 全部通过
- [ ] 若改动前端 UI，已运行 `node --check internal/httpserver/static/ui/app.js`
- [ ] 若改动 `web/reader/`，已运行 `bash scripts/build-reader.sh` 通过 TypeScript 与 Vite 构建
- [ ] 若涉及目录/搜索/进度/接口/UI，已确认遵守多用户 `UserScope`、书库白名单与 NSFW 隔离规则
- [ ] 已确认不包含任何真实 Token、密码、内网 IP、本机路径或私有测试数据
