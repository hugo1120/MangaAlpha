# Security

## 安全模型（v3）

**MangaAlpha** 采用 **1 主管理员 + N 普通用户** 的多用户分权强鉴权模型：

- **首次初始化强制设密**：新安装或从 `v1.x` 升级后首次打开服务时，所有业务页面与 `/api/v1/*`、`/admin/api/*` 均处于锁定状态（页面重定向至 `/setup`，接口返回 `401 SETUP_REQUIRED`），直到完成主管理员用户名与密码初始化；初始化完成后 `/setup` 与 `/api/v1/claim` 永久锁定。公网无人值守部署可通过可选环境变量 `FK115_ADMIN_USERNAME` + `FK115_ADMIN_PASSWORD` 在首次启动时直接完成初始化并锁定，消除抢注窗口。
- **多用户分权与数据隔离（`UserScope`）**：
  - 禁止公开自助注册，普通用户仅由主管理员在 `/admin` 创建与授权（禁止普通用户自行修改用户名）；所有后台管理接口、元数据写接口、存储源/任务/日志、明文加密密码本及 `/api/agent/v1/*` 均受 `requireAdmin` 强制拦截（返回 `403`）。
  - 支持按账号配置书库白名单（空白名单严格 fail-closed 返回空结果）与 NSFW 授权开关（普通用户仅在 `allow_nsfw = 1 && show_nsfw = 1` 时可见 NSFW 内容）；每位用户的阅读进度、进度重置墓碑、阅读历史、收藏夹、阅读列表、客户端设置、会话、API Key、油猴配对与 Bangumi 进度追踪器均完全隔离。
  - 前端 SPA 与内置阅读器写请求附带 `X-FK115-User-Id` 校验，防止同浏览器切换账号后错写数据；受权限保护的漫画页图与封面统一返回 `Cache-Control: private, no-store`；未登录态与公开只读接口不暴露服务端版本号或代号。
- **密码与凭据哈希存储**：密码采用 `argon2id` 哈希存储（带并发信号量上限防止高并发 OOM DoS，并对无效用户名做等耗时校验防止用户名枚举）；浏览器会话 Cookie（`fk115_session` / `KOMGA-SESSION`，`HttpOnly` + `SameSite=Lax`，HTTPS 下自动启用 `Secure`）与 `X-API-Key` / `Authorization: Bearer` 在 SQLite 中仅保存 `SHA-256` 摘要，不存明文；重置密码时自动吊销其他活跃会话与旧 API Key。
- **防爆破限流、CSRF 与安全响应头**：登录接口与 HTTP Basic Auth 内置双层（Per-IP + Per-Account）失败限流与渐进退避（配合 `FK115_TRUSTED_PROXIES` 可信代理白名单防止公网伪造 `X-Forwarded-For` / `X-Forwarded-Proto`）；Browser Session 与 Basic Auth 状态变更请求校验同源 `Origin` / `Sec-Fetch-Site`；全站输出 `X-Content-Type-Options: nosniff`、`X-Frame-Options: SAMEORIGIN` 与 `Referrer-Policy: same-origin`（不在应用层强制下发 HSTS，确保局域网明文 HTTP 零影响）。
- **移除外部跨域白名单**：内置网页书架与网页阅读器均同源托管于同一端口，不再开放外部跨域 `CORS` 接口。
- **外部元数据 SSRF 防护与脱敏边界**：
  - 元数据市场清单、离线包下载、低速在线补全与外部封面抓取统一经防 SSRF HTTP 客户端：仅允许 `http/https`，严格拦截所有字面量私网/回环/链路本地/CGNAT/Fake-IP URL 与内网主机名（`localhost`、`*.local`、`*.internal` 等）；直连模式下对合法公网域名的本地 DNS 解析防 DNS Rebinding（放行标准透明代理 Fake-IP 段 `198.18.0.0/15`、`fc00::/18` 并跳过 `0.0.0.0`/`::` 占位，仍拦截真实内网/回环/链路本地/CGNAT/`fd00::/8` 混合解析）；重定向限 5 跳且跨域/降级自动剥离 `Authorization`、`Proxy-Authorization`、`Cookie`。
  - Bangumi NSFW 授权搜索与封面拉取落实 5 道防泄露防线：Bearer 凭证仅发往 `https://api.bgm.tv`、重定向自动剥离认证头、先校验用户 `UserScope` 再拉图、OAuth 刷新隔离至主管理员账号、响应头强制 `Cache-Control: private, no-store`。
  - 社区映射包导出与全平台诊断日志导出均执行严格脱敏过滤（剥离 URL、私有 IP、凭据、Token 与私人文件路径），映射导出默认排除 NSFW 与隐藏书库。
- **本地敏感凭据边界**：为了访问上游存储源、Bangumi 追踪同步与已解锁加密漫画包，115 Access/Refresh Token、短期 `downurl`、WebDAV 账号密码、Bangumi OAuth/PAT 凭据及已记住的加密归档密码保存在本地 SQLite 数据库中（权限 `0600`）。普通 GET 管理接口（如 `/admin/api/settings`）与诊断导出绝不返回凭据原文，加密归档明文密码本仅允许主管理员查看与清空，日志中不记录 Token、密码、Cookie、API Key 或完整 `downurl`。

如需跨公网访问，请务必配合 HTTPS 反向代理或 Tailscale/WireGuard 组网工具，具体配置与安全清单详见 **[docs/REVERSE_PROXY.md](docs/REVERSE_PROXY.md)**。

## 敏感文件

以下内容不得提交到 Git：

- `data/` 及其中的 SQLite 数据库、Bangumi 离线索引库、扩展元数据与 Sidecar、缓存、封面和日志
- `user_info*`
- `.env*`、本地配置文件、Access Token、Refresh Token、WebDAV 密码、API Key、加密压缩包密码本
- 真实 `downurl`、Cookie、Authorization Header
- 包含私人漫画目录名或账号信息的调试输出
- Android 签名库、签名私钥或密码（公开证书摘要可以公开）

## 报告漏洞

请不要在公开 Issue 中提交可用 Token、真实 `downurl`、密码或其他个人信息。请先构造不含敏感数据的最小复现；若问题涉及隐私信息，请通过 GitHub 仓库所有者的私下渠道联系。
