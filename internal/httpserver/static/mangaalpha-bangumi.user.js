// ==UserScript==
// @name         MangaAlpha Bangumi 一键跳转
// @namespace    https://github.com/xJogger/MangaAlpha
// @version      2.7.0
// @description  在 Bangumi 条目页右侧栏自动探测本地 MangaAlpha 收录状态，并一键直达对应漫画。
// @match        https://bgm.tv/subject/*
// @match        https://bangumi.tv/subject/*
// @match        https://chii.in/subject/*
// @grant        GM_xmlhttpRequest
// @grant        GM_getValue
// @grant        GM_setValue
// @grant        GM_registerMenuCommand
// __MANGAALPHA_CONNECT_LINE__
// @connect      *
// @run-at       document-end
// ==/UserScript==

(function () {
  'use strict';

  const DEFAULT_SERVER_URL = '__MANGAALPHA_SERVER_URL__';
  const DEFAULT_API_KEY = '__MANGAALPHA_API_KEY__';

  const m = location.pathname.match(/^\/subject\/(\d+)\/?$/);
  if (!m) return;
  const subjectId = m[1];

  const titleEl = document.querySelector('#headerSubject h1.nameSingle a') || document.querySelector('#headerSubject h1.nameSingle');
  const pageTitle = (titleEl && titleEl.textContent ? titleEl.textContent : '').trim();

  function cleanServerUrl(raw) {
    return String(raw || '').trim().replace(/\/+$/, '');
  }

  function validEmbedded(val) {
    const s = String(val || '').trim();
    return s && !s.includes('__MANGAALPHA_') ? s : '';
  }

  // Persist embedded credentials on first install and automatically sync when the user
  // explicitly installs a rotated key via "一键安装（自动生成并轮换专属 Key）".
  // When updating via "仅更新脚本（沿用已保存 Key）", embeddedKey is empty so existing
  // GM_getValue('api_key') and GM_getValue('server_url') remain untouched.
  (function syncEmbeddedConfig() {
    const embeddedUrl = cleanServerUrl(validEmbedded(DEFAULT_SERVER_URL));
    const embeddedKey = validEmbedded(DEFAULT_API_KEY);
    const savedUrl = cleanServerUrl(GM_getValue('server_url', ''));
    const savedKey = String(GM_getValue('api_key', '') || '').trim();
    const seededKey = String(GM_getValue('seeded_api_key', '') || '').trim();

    if (!savedUrl && embeddedUrl) {
      GM_setValue('server_url', embeddedUrl);
      GM_setValue('seeded_server_url', embeddedUrl);
    }
    if (embeddedKey && (!savedKey || embeddedKey !== seededKey)) {
      GM_setValue('api_key', embeddedKey);
      GM_setValue('seeded_api_key', embeddedKey);
      if (embeddedUrl) {
        GM_setValue('server_url', embeddedUrl);
        GM_setValue('seeded_server_url', embeddedUrl);
      }
    }
  })();

  function getServerUrl() {
    const saved = cleanServerUrl(GM_getValue('server_url', ''));
    if (saved) return saved;
    return cleanServerUrl(validEmbedded(DEFAULT_SERVER_URL));
  }

  function getApiKey() {
    const saved = String(GM_getValue('api_key', '') || '').trim();
    if (saved) return saved;
    return validEmbedded(DEFAULT_API_KEY);
  }

  function promptConfig() {
    const curUrl = getServerUrl();
    const nextUrl = window.prompt('请输入 MangaAlpha 服务器地址（例如 http://192.168.1.10:25600 或 https://manga.example.com）：', curUrl);
    if (nextUrl === null) return;
    GM_setValue('server_url', cleanServerUrl(nextUrl));
    const curKey = getApiKey();
    const nextKey = window.prompt('请输入 MangaAlpha API Key（留空则仅显示跳转按钮，不自动探测收录状态）：', curKey);
    if (nextKey !== null) {
      GM_setValue('api_key', nextKey.trim());
    }
    render();
  }

  if (typeof GM_registerMenuCommand === 'function') {
    GM_registerMenuCommand('设置 MangaAlpha 服务器地址与 API Key', promptConfig);
    GM_registerMenuCommand('清空 MangaAlpha 脚本配置', () => {
      GM_setValue('server_url', '');
      GM_setValue('api_key', '');
      GM_setValue('seeded_server_url', '');
      GM_setValue('seeded_api_key', '');
      render();
    });
  }

  function injectStyles() {
    if (document.getElementById('mangaalpha-bgm-style')) return;
    const style = document.createElement('style');
    style.id = 'mangaalpha-bgm-style';
    style.textContent = `
      #mangaalpha-bgm-panel {
        margin-top: 10px;
        box-sizing: border-box;
      }
      #mangaalpha-bgm-panel .ma-head {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 6px;
        margin: 0 0 8px 0;
        padding: 0 0 6px 0;
        border-bottom: 1px solid rgba(127, 127, 127, 0.22);
      }
      #mangaalpha-bgm-panel .ma-head-title {
        font-size: 13px;
        font-weight: 700;
        color: #333;
        display: inline-flex;
        align-items: center;
        gap: 5px;
      }
      #mangaalpha-bgm-panel .ma-gear {
        background: transparent;
        border: 1px solid rgba(127, 127, 127, 0.28);
        color: #666;
        border-radius: 4px;
        padding: 1px 6px;
        font-size: 11px;
        line-height: 1.5;
        cursor: pointer;
      }
      #mangaalpha-bgm-panel .ma-gear:hover {
        color: #111;
        border-color: rgba(127, 127, 127, 0.55);
      }
      #mangaalpha-bgm-panel .ma-body {
        display: flex;
        flex-direction: column;
        gap: 7px;
        font-size: 12px;
        line-height: 1.5;
      }
      #mangaalpha-bgm-panel .ma-cta {
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 5px;
        width: 100%;
        box-sizing: border-box;
        padding: 7px 10px;
        border-radius: 6px;
        text-decoration: none;
        font-size: 12.5px;
        font-weight: 600;
        line-height: 1.4;
        text-align: center;
        border: 1px solid transparent;
        cursor: pointer;
        transition: background-color 0.15s ease, border-color 0.15s ease, opacity 0.15s ease;
      }
      #mangaalpha-bgm-panel .ma-cta-found {
        background: #16a34a;
        color: #fff !important;
        border-color: #15803d;
      }
      #mangaalpha-bgm-panel .ma-cta-found:hover {
        background: #15803d;
      }
      #mangaalpha-bgm-panel .ma-cta-muted {
        background: rgba(100, 116, 139, 0.12);
        color: #334155 !important;
        border-color: rgba(100, 116, 139, 0.30);
      }
      #mangaalpha-bgm-panel .ma-cta-muted:hover {
        background: rgba(100, 116, 139, 0.20);
      }
      #mangaalpha-bgm-panel .ma-cta-warn {
        background: rgba(217, 119, 6, 0.12);
        color: #92400e !important;
        border-color: rgba(217, 119, 6, 0.38);
      }
      #mangaalpha-bgm-panel .ma-cta-warn:hover {
        background: rgba(217, 119, 6, 0.20);
      }
      #mangaalpha-bgm-panel .ma-meta {
        font-size: 11.5px;
        color: #555;
        word-break: break-word;
      }
      #mangaalpha-bgm-panel .ma-versions {
        display: flex;
        flex-direction: column;
        gap: 5px;
      }
      #mangaalpha-bgm-panel .ma-ver {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 6px;
        padding: 5px 8px;
        border-radius: 5px;
        text-decoration: none;
        font-size: 11.5px;
        line-height: 1.4;
        background: rgba(22, 163, 74, 0.09);
        color: #166534 !important;
        border: 1px solid rgba(22, 163, 74, 0.28);
      }
      #mangaalpha-bgm-panel .ma-ver:hover {
        background: rgba(22, 163, 74, 0.16);
      }
      #mangaalpha-bgm-panel .ma-ver-name {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        min-width: 0;
        font-weight: 500;
      }
      #mangaalpha-bgm-panel .ma-ver-cnt {
        flex: 0 0 auto;
        opacity: 0.85;
        font-size: 11px;
      }
      #mangaalpha-bgm-panel.ma-fallback-bar {
        width: 100%;
        max-width: 1200px;
        margin: 8px auto 4px;
        padding: 10px 12px;
        border-radius: 8px;
        background: #fff;
        border: 1px solid rgba(15, 23, 42, 0.12);
      }
      html[data-theme="dark"] #mangaalpha-bgm-panel .ma-head-title {
        color: #e5e7eb;
      }
      html[data-theme="dark"] #mangaalpha-bgm-panel .ma-gear {
        color: #cbd5e1;
        border-color: rgba(148, 163, 184, 0.35);
      }
      html[data-theme="dark"] #mangaalpha-bgm-panel .ma-gear:hover {
        color: #fff;
        border-color: rgba(203, 213, 225, 0.65);
      }
      html[data-theme="dark"] #mangaalpha-bgm-panel .ma-meta {
        color: #a3a3a3;
      }
      html[data-theme="dark"] #mangaalpha-bgm-panel .ma-cta-found {
        background: #15803d;
        border-color: #22c55e;
        color: #ffffff !important;
      }
      html[data-theme="dark"] #mangaalpha-bgm-panel .ma-cta-found:hover {
        background: #16a34a;
      }
      html[data-theme="dark"] #mangaalpha-bgm-panel .ma-cta-muted {
        background: rgba(148, 163, 184, 0.16);
        color: #e2e8f0 !important;
        border-color: rgba(148, 163, 184, 0.36);
      }
      html[data-theme="dark"] #mangaalpha-bgm-panel .ma-cta-muted:hover {
        background: rgba(148, 163, 184, 0.26);
      }
      html[data-theme="dark"] #mangaalpha-bgm-panel .ma-cta-warn {
        background: rgba(245, 158, 11, 0.18);
        color: #fcd34d !important;
        border-color: rgba(251, 191, 36, 0.45);
      }
      html[data-theme="dark"] #mangaalpha-bgm-panel .ma-cta-warn:hover {
        background: rgba(245, 158, 11, 0.28);
      }
      html[data-theme="dark"] #mangaalpha-bgm-panel .ma-ver {
        background: rgba(34, 197, 94, 0.16);
        color: #86efac !important;
        border-color: rgba(74, 222, 128, 0.40);
      }
      html[data-theme="dark"] #mangaalpha-bgm-panel .ma-ver:hover {
        background: rgba(34, 197, 94, 0.26);
      }
      html[data-theme="dark"] #mangaalpha-bgm-panel.ma-fallback-bar {
        background: #2c2c2c;
        border-color: #444;
      }
    `;
    (document.head || document.documentElement).appendChild(style);
  }

  function mountPanel() {
    const existing = document.getElementById('mangaalpha-bgm-panel');
    if (existing) existing.remove();

    const panel = document.createElement('div');
    panel.id = 'mangaalpha-bgm-panel';

    const interestWrapper = document.querySelector('#panelInterestWrapper');
    if (interestWrapper) {
      panel.className = 'SidePanel png_bg';
      const shareBtn = interestWrapper.querySelector('.shareBtn');
      if (shareBtn && shareBtn.parentElement === interestWrapper) {
        shareBtn.insertAdjacentElement('afterend', panel);
      } else {
        interestWrapper.appendChild(panel);
      }
      return panel;
    }

    const sideCol = document.querySelector('#columnSubjectInB');
    if (sideCol) {
      panel.className = 'SidePanel png_bg';
      sideCol.insertBefore(panel, sideCol.firstChild);
      return panel;
    }

    const header = document.querySelector('#headerSubject');
    if (!header) return null;
    panel.className = 'ma-fallback-bar';
    header.appendChild(panel);
    return panel;
  }

  injectStyles();
  const panel = mountPanel();
  if (!panel) return;

  function buildPanelFrame() {
    panel.textContent = '';
    const head = document.createElement('div');
    head.className = 'ma-head';

    const title = document.createElement('span');
    title.className = 'ma-head-title';
    title.textContent = 'MangaAlpha 本库';
    head.appendChild(title);

    const gear = document.createElement('button');
    gear.type = 'button';
    gear.className = 'ma-gear';
    gear.textContent = '⚙ 配置';
    gear.title = '配置 MangaAlpha 服务器地址与 API Key';
    gear.onclick = promptConfig;
    head.appendChild(gear);

    const body = document.createElement('div');
    body.className = 'ma-body';

    panel.appendChild(head);
    panel.appendChild(body);
    return body;
  }

  function makeLink(text, href, kind, title) {
    const a = document.createElement('a');
    a.textContent = text;
    a.href = href;
    a.target = '_blank';
    a.rel = 'noopener noreferrer';
    a.className = `ma-cta ma-cta-${kind}`;
    if (title) a.title = title;
    return a;
  }

  function makeActionBtn(text, onClick, kind) {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.textContent = text;
    btn.className = `ma-cta ma-cta-${kind}`;
    btn.onclick = onClick;
    return btn;
  }

  function makeMeta(text) {
    const div = document.createElement('div');
    div.className = 'ma-meta';
    div.textContent = text;
    return div;
  }

  function render() {
    const body = buildPanelFrame();
    const serverUrl = getServerUrl();
    const apiKey = getApiKey();

    if (!serverUrl) {
      body.appendChild(makeActionBtn('⚙ 配置 MangaAlpha 地址', promptConfig, 'muted'));
      body.appendChild(makeMeta('填写后可在本页自动探测本库收录状态。'));
      return;
    }

    const jumpUrl = `${serverUrl}/bgm/${encodeURIComponent(subjectId)}${pageTitle ? `?title=${encodeURIComponent(pageTitle)}` : ''}`;
    const searchUrl = `${serverUrl}/libraries?search=${encodeURIComponent(pageTitle)}`;

    if (!apiKey) {
      body.appendChild(makeLink('在 MangaAlpha 中查看 ↗', jumpUrl, 'muted', '未配置 API Key，点击将使用浏览器登录态跳转'));
      body.appendChild(makeActionBtn('配置 API Key 开启自动探测', promptConfig, 'muted'));
      return;
    }

    body.appendChild(makeMeta('正在探测本库收录状态…'));

    GM_xmlhttpRequest({
      method: 'GET',
      url: `${serverUrl}/api/v1/bangumi/lookup?subject_id=${encodeURIComponent(subjectId)}`,
      headers: { 'X-API-Key': apiKey, 'Accept': 'application/json' },
      timeout: 8000,
      onload(resp) {
        body.textContent = '';
        if (resp.status === 401 || resp.status === 403) {
          body.appendChild(makeActionBtn('API Key 无效（点此更新）', promptConfig, 'warn'));
          body.appendChild(makeLink('直接打开本库 ↗', jumpUrl, 'muted'));
          return;
        }
        if (resp.status < 200 || resp.status >= 300) {
          body.appendChild(makeLink(`在 MangaAlpha 中打开 ↗（HTTP ${resp.status}）`, jumpUrl, 'warn'));
          return;
        }
        let data;
        try {
          data = JSON.parse(resp.responseText || '{}');
        } catch {
          body.appendChild(makeLink('在 MangaAlpha 中打开 ↗', jumpUrl, 'warn'));
          return;
        }

        const matches = Array.isArray(data.matches) ? data.matches : [];
        if (data.found && matches.length === 1) {
          const item = matches[0];
          const countText = item.oneshot ? '单本' : `共 ${item.booksCount || 0} 册`;
          const readText = item.booksReadCount > 0 ? ` · 已读 ${item.booksReadCount}` : '';
          body.appendChild(makeLink(
            '✓ 已收录 · 前往阅读 ↗',
            `${serverUrl}${item.url}`,
            'found',
            `${item.title || ''} (${item.libraryName || ''})`
          ));
          body.appendChild(makeMeta(`${item.libraryName || '书库'} · ${countText}${readText}`));
        } else if (data.found && matches.length > 1) {
          body.appendChild(makeLink(
            `✓ 已收录 ${matches.length} 个版本 · 前往查看 ↗`,
            `${serverUrl}${data.jumpUrl || `/bgm/${subjectId}`}`,
            'found',
            '点击进入优先版本并展开版本切换栏'
          ));
          const libFreq = {};
          matches.forEach((it) => {
            const k = it.libraryName || '';
            libFreq[k] = (libFreq[k] || 0) + 1;
          });
          const list = document.createElement('div');
          list.className = 'ma-versions';
          matches.slice(0, 6).forEach((item) => {
            const a = document.createElement('a');
            a.className = 'ma-ver';
            a.href = `${serverUrl}${item.url}`;
            a.target = '_blank';
            a.rel = 'noopener noreferrer';
            a.title = `${item.title || ''} (${item.libraryName || ''})`;

            const nameSpan = document.createElement('span');
            nameSpan.className = 'ma-ver-name';
            const libName = item.libraryName || '书库';
            if (libFreq[libName] > 1 && item.title) {
              nameSpan.textContent = `${libName} · ${item.title}`;
            } else {
              nameSpan.textContent = libName;
            }

            const cntSpan = document.createElement('span');
            cntSpan.className = 'ma-ver-cnt';
            cntSpan.textContent = item.oneshot ? '单本' : `${item.booksCount || 0}册`;

            a.appendChild(nameSpan);
            a.appendChild(cntSpan);
            list.appendChild(a);
          });
          body.appendChild(list);
        } else {
          body.appendChild(makeLink('本库暂未收录 · 去搜索 ↗', searchUrl, 'muted'));
        }
      },
      onerror() {
        body.textContent = '';
        body.appendChild(makeLink('在 MangaAlpha 中打开 ↗', jumpUrl, 'warn'));
        body.appendChild(makeMeta('无法直连服务器（请检查网络或在油猴跨域提示中允许当前主机）'));
      },
      ontimeout() {
        body.textContent = '';
        body.appendChild(makeLink('在 MangaAlpha 中打开 ↗', jumpUrl, 'warn'));
        body.appendChild(makeMeta('探测超时，点击直接跳转'));
      },
    });
  }

  render();
})();
