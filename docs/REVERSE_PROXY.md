# 公网部署与 HTTPS 反向代理指南

MangaAlpha 内置了管理员强鉴权、登录限流、Argon2id 防 OOM 并发限制与同源 CSRF 防护，但应用本身提供的是 HTTP 服务。**如需将 MangaAlpha 暴露到公网，务必通过 HTTPS 反向代理（或 Tailscale / WireGuard 内网穿透）提供传输层加密**，切勿直接将明文 HTTP `25600` 端口裸露在公网上。

---

## 一、公网部署前置安全清单

1. **后端仅监听本机回环（防止绕过反代直连明文 HTTP）**
   - 直接运行二进制：启动时加 `--host 127.0.0.1 --port 25600`（或设置 `FK115_HOST=127.0.0.1`）。
   - Docker Compose：将端口映射改为仅绑定本机回环：
     ```yaml
     ports:
       - "127.0.0.1:25600:25600"
     ```
     （如果反向代理也在同一 Docker 网络内，甚至可以去掉 `ports:`，只让反代通过容器名 `http://fake-komga-115:25600` 转发。）
2. **消除首次启动抢注窗口**
   - 首次部署到公网时，建议在启动前通过环境变量预设管理员账号，避免服务先上线、未及时打开 `/setup` 时被他人抢注：
     ```bash
     FK115_ADMIN_USERNAME=your_admin_name
     FK115_ADMIN_PASSWORD=your_strong_password_at_least_12_chars
     ```
   - 该环境变量**仅在数据库尚未初始化时生效一次**，初始化完成后自动锁定 `/setup` 与 `/api/v1/claim`，之后重启会被忽略（初始化完成后可从配置中移除密码环境变量）。
3. **正确配置可信代理（`FK115_TRUSTED_PROXIES`）**
   - MangaAlpha 默认信任本机回环（`127.0.0.0/8`、`::1/128`）与常见 Docker/局域网私网段（`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`、`fc00::/7`），并仅对可信代理采信 `X-Forwarded-Proto` 与 `X-Forwarded-For`（用于自动给登录 Cookie 加上 `Secure` 标记、生成 HTTPS 油猴脚本地址、以及按真实访客 IP 执行登录防爆破限流）。
   - 若反代就在同一台宿主机，可进一步收紧为 `FK115_TRUSTED_PROXIES=127.0.0.1/32,::1/128`；若反代位于特定公网节点，可逗号分隔填入对应 IP/CIDR。
4. **反代转发必配项**
   - 透传 `Host`、`X-Real-IP`、`X-Forwarded-For`、`X-Forwarded-Proto`。
   - 对实时同步事件流 **`/api/v1/sse/`** 关闭代理缓冲并调大读超时（例如 `3600s`），避免 Mihon / Koharia 或多标签页实时推送频繁断连（MangaAlpha 已自带 `X-Accel-Buffering: no`，但 Nginx / NPM 仍建议显式放宽超时）。
5. **更轻量的免公网暴露方案**
   - 如果只需自己和家人在外网手机看漫画，不想把域名暴露给公网扫描器，推荐优先使用 **Tailscale**、**ZeroTier** 或 **WireGuard** 组网直连，无需开放公网端口。

---

## 二、Caddy（最简自动 HTTPS，推荐）

Caddy 会自动向 Let's Encrypt 申请与续期 TLS 证书，默认支持 HTTP/2、自动透传 `X-Forwarded-*` 且原生友好支持 SSE 长连接：

```caddyfile
manga.example.com {
    reverse_proxy 127.0.0.1:25600 {
        # 对 SSE 事件流禁用缓冲、实时刷新
        flush_interval -1
    }

    # 确认域名长期仅走 HTTPS 后，可取消下面注释启用 HSTS
    # header Strict-Transport-Security "max-age=31536000; includeSubDomains"
}
```

> 若 Caddy 与 MangaAlpha 运行在同一 Docker Compose 网络下，将 `127.0.0.1:25600` 替换为 `fake-komga-115:25600` 即可。

---

## 三、Nginx（经典生产配置）

```nginx
server {
    listen 80;
    listen [::]:80;
    server_name manga.example.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name manga.example.com;

    ssl_certificate     /etc/letsencrypt/live/manga.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/manga.example.com/privkey.pem;
    ssl_protocols       TLSv1.2 TLSv1.3;
    ssl_session_cache   shared:SSL:10m;
    ssl_session_timeout 1d;

    # 确认稳定使用 HTTPS 后可开启 HSTS（局域网明文 HTTP 切勿开启）
    # add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;

    # 通用代理头
    proxy_http_version 1.1;
    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    # 1. Komga SSE 实时事件流：关闭缓冲 + 长读超时
    location /api/v1/sse/ {
        proxy_pass         http://127.0.0.1:25600;
        proxy_set_header   Connection "";
        proxy_buffering    off;
        proxy_cache        off;
        chunked_transfer_encoding on;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }

    # 2. 主站、书架、内置阅读器与 Komga API
    location / {
        proxy_pass         http://127.0.0.1:25600;
        proxy_set_header   Connection "";
        # 网盘/WebDAV 流式索引或高清大图解压时留足读超时
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
        client_max_body_size 20m;
    }
}
```

---

## 四、Nginx Proxy Manager（NPM，NAS / Docker 常用）

在 NPM 管理界面新建 **Proxy Host**：

1. **Details 标签页**
   - **Domain Names**：`manga.example.com`
   - **Scheme**：`http`
   - **Forward Hostname / IP**：宿主机 IP 或同网络容器名 `fake-komga-115`
   - **Forward Port**：`25600`
   - 勾选 **Block Common Exploits**（可开启）
   - **不要**开启 Cache Assets（MangaAlpha 已针对 `/reader/assets/` 与封面精细管理缓存头）
2. **SSL 标签页**
   - 申请 Let's Encrypt 证书，勾选 **Force SSL**、**HTTP/2 Support**（稳定后可按需勾选 HSTS）。
3. **Advanced 标签页 → Custom Nginx Configuration**（补充 SSE 长超时与反代读超时）：
   ```nginx
   proxy_read_timeout 300s;
   proxy_send_timeout 300s;

   location /api/v1/sse/ {
       proxy_pass http://$server:$port;
       proxy_http_version 1.1;
       proxy_set_header Host $host;
       proxy_set_header X-Real-IP $remote_addr;
       proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
       proxy_set_header X-Forwarded-Proto $scheme;
       proxy_set_header Connection "";
       proxy_buffering off;
       proxy_cache off;
       proxy_read_timeout 3600s;
   }
   ```

---

## 五、Cloudflare Tunnel（`cloudflared`，无公网 IP 方案）

适合家庭宽带 / NAS 没有公网 IP、希望零端口暴露的场景：

1. 在 Cloudflare Zero Trust 后台创建 Tunnel，添加 Public Hostname：
   - **Domain**：`manga.example.com`
   - **Service**：`HTTP` → `127.0.0.1:25600`（Docker 同网填 `fake-komga-115:25600`）
2. 若使用本地 YAML 配置 `config.yml`：
   ```yaml
   tunnel: <YOUR-TUNNEL-ID>
   credentials-file: /etc/cloudflared/<YOUR-TUNNEL-ID>.json

   ingress:
     - hostname: manga.example.com
       service: http://127.0.0.1:25600
       originRequest:
         noTLSVerify: false
         keepAliveTimeout: 90s
     - service: http_status:404
   ```
3. **注意**：
   - Cloudflare 免费版默认对单次空闲连接有 100 秒超时，MangaAlpha 的 SSE 端点每 **25 秒**会自动发送一次 `: keepalive` 心跳，可稳定保活。
   - 如开启 Cloudflare 代理后希望登录限流识别真实公网访客 IP，可在 Cloudflare Transform Rules / 网关中确保 `X-Forwarded-For` 透传，并保持 `cloudflared` 运行在本机回环或私网段。

---

## 六、上线后快速自检

部署完 HTTPS 反代后，建议顺手确认以下 4 项：

1. 访问 `https://manga.example.com/health` 返回 `{"status":"ok"}`。
2. 登录后在浏览器开发者工具 → Application → Cookies 检查 `fk115_session` 与 `KOMGA-SESSION` 均已带有 **`HttpOnly`**、**`SameSite=Lax`** 且 **`Secure`** 为勾选状态（若 `Secure` 未勾选，说明反代未透传 `X-Forwarded-Proto: https` 或反代 IP 不在 `FK115_TRUSTED_PROXIES` 中）。
3. 在「管理后台 → Bangumi」点击「一键安装油猴脚本」，确认生成脚本内的服务器地址为 `https://manga.example.com` 且包含 `// @connect manga.example.com`。
4. 使用公网测试确认 `http://<服务器公网IP>:25600` **无法直接连入**（已被防火墙或 `127.0.0.1` 绑定拦截）。
