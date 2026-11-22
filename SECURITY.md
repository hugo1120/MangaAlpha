# Security

## 安全模型（v2）

**MangaAlpha** 采用单管理员强鉴权模型：

- **首次初始化强制设密**：新安装或从 `v1.x` 升级后首次打开服务时，所有业务页面与 `/api/v1/*`、`/admin/api/*` 均处于锁定状态（页面重定向至 `/setup`，接口返回 `401 SETUP_REQUIRED`），直到用户完成自定义管理员用户名与密码设置；初始化完成后 `/setup` 与 `/api/v1/claim` 永久锁定。公网无人值守部署也支持通过可选环境变量 `FK115_ADMIN_USERNAME` + `FK115_ADMIN_PASSWORD` 在首次启动时直接完成初始化并锁定，消除抢注窗口。
- **密码与凭据哈希存储**：管理员密码采用 `argon2id` 哈希存储（带并发信号量上限防止高并发 OOM DoS，并对无效用户名做等耗时校验防止用户名枚举）；浏览器会话 Cookie（`fk115_session` / `KOMGA-SESSION`，`HttpOnly` + `SameSite=Lax`，HTTPS 下自动启用 `Secure`）与 `X-API-Key` 在 SQLite 中仅保存 `SHA-256` 摘要，不存明文。
- **防爆破限流、CSRF 与安全响应头**：登录接口与 HTTP Basic Auth 内置按客户端 IP 的失败限流与渐进退避（配合 `FK115_TRUSTED_PROXIES` 可信代理白名单防止公网伪造 `X-Forwarded-For` / `X-Forwarded-Proto`）；Cookie 写操作自动校验同源 `Origin` / `Sec-Fetch-Site`；全站输出 `X-Content-Type-Options: nosniff`、`X-Frame-Options: SAMEORIGIN` 与 `Referrer-Policy: same-origin`（不在应用层强制下发 HSTS，确保局域网明文 HTTP 零影响）。
- **外部客户端认证**：支持 Mihon / Koharia 使用 HTTP Basic Auth 或 `X-API-Key` 请求头接入；修改主密码时会自动吊销其他活跃会话。
- **移除外部跨域白名单**：内置网页书架与网页阅读器均同源托管于同一端口，不再开放外部跨域 `CORS` 接口。
- **本地敏感凭据边界**：为了能够访问上游存储源与 Bangumi 追踪同步，115 Access/Refresh Token、短期 `downurl`、WebDAV 账号密码以及 Bangumi Personal Access Token 保存在本地 SQLite 数据库中（权限 `0600`）。GET 管理接口（包括 `/admin/api/settings`）不会返回这些凭据原文，日志中也不会记录 Token、密码、Cookie、API Key 或完整 `downurl`。

如需跨公网访问，请务必配合 HTTPS 反向代理或 Tailscale/WireGuard 组网工具，具体配置与安全清单详见 **[docs/REVERSE_PROXY.md](docs/REVERSE_PROXY.md)**。

## 敏感文件

以下内容不得提交到 Git：

- `data/` 及其中的 SQLite 数据库、Bangumi 离线索引库、缓存、封面和日志
- `user_info*`
- `.env*`、本地配置文件、Access Token、Refresh Token、WebDAV 密码、API Key
- 真实 `downurl`、Cookie、Authorization Header
- 包含私人漫画目录名或账号信息的调试输出
- Android 签名库、签名私钥或密码（公开证书摘要可以公开）

## 报告漏洞

请不要在公开 Issue 中提交可用 Token、真实 `downurl`、密码或其他个人信息。请先构造不含敏感数据的最小复现；若问题涉及隐私信息，请通过 GitHub 仓库所有者的私下渠道联系。
