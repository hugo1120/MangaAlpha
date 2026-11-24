(() => {
  'use strict';

  const THEME_MODE_KEY = 'fk115_theme_mode';
  const darkMediaQuery = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;

  // Per-user web UI display preferences, stored in Komga client settings under
  // the existing webui.* namespace so they travel across devices.
  const DISPLAY_PREFS = {
    shelfPageSize: { key: 'webui.shelf.pageSize', def: 36, min: 12, max: 120 },
    homeContinueSize: { key: 'webui.home.continueSize', def: 8, min: 4, max: 24 },
    homeUpdatedSize: { key: 'webui.home.updatedSize', def: 18, min: 4, max: 36 },
    homeAddedSize: { key: 'webui.home.addedSize', def: 12, min: 4, max: 36 },
  };

  function clampDisplayPref(spec, raw) {
    const n = Number.parseInt(raw, 10);
    if (!Number.isFinite(n)) return spec.def;
    return Math.min(spec.max, Math.max(spec.min, n));
  }

  function defaultDisplayPrefs() {
    const out = {};
    for (const [name, spec] of Object.entries(DISPLAY_PREFS)) {
      out[name] = spec.def;
    }
    return out;
  }

  function displayPrefStorageKey(userId, name) {
    return `fk115_display.${userId}.${name}`;
  }

  function readLocalDisplayPrefs(userId) {
    const out = defaultDisplayPrefs();
    if (!userId) return out;
    for (const [name, spec] of Object.entries(DISPLAY_PREFS)) {
      try {
        const raw = localStorage.getItem(displayPrefStorageKey(userId, name));
        if (raw != null && raw !== '') out[name] = clampDisplayPref(spec, raw);
      } catch (_) {}
    }
    return out;
  }

  const state = {
    auth: {
      claimed: true,
      authenticated: false,
      userId: '',
      username: '',
      role: '',
      isAdmin: false,
      allowNsfw: false,
      showNsfw: false,
      allLibraries: false,
      version: '',
      codename: '',
      codenameQuote: '',
    },
    themeMode: localStorage.getItem(THEME_MODE_KEY) || 'auto',
    theme: 'dark',
    displayPrefs: defaultDisplayPrefs(),
    displayPrefsUserId: '',
    libraries: [],
    taxonomies: { loaded: false, genres: [], tags: [], publishers: [] },
    pollTimer: null,
    pollFn: null,
    coverVer: Date.now(),
    adminTab: 'overview',
    bgmProxyProbeHTML: '',
    updateStatus: null,
  };

  function isAdminUser() {
    return Boolean(state.auth && state.auth.isAdmin);
  }

  function resetDisplayPrefs() {
    state.displayPrefs = defaultDisplayPrefs();
    state.displayPrefsUserId = '';
  }

  function displayPref(name) {
    const spec = DISPLAY_PREFS[name];
    return Math.min(spec.max, Math.max(spec.min, state.displayPrefs[name] || spec.def));
  }

  async function ensureDisplayPrefs() {
    const uid = (state.auth && state.auth.userId) || '';
    if (!uid) {
      resetDisplayPrefs();
      return;
    }
    if (state.displayPrefsUserId === uid) return;
    state.displayPrefs = readLocalDisplayPrefs(uid);
    try {
      const res = await api('/api/v1/client-settings/user/list', {
        headers: { 'X-FK115-User-Id': uid },
      });
      if (((state.auth && state.auth.userId) || '') !== uid) return;
      const next = defaultDisplayPrefs();
      for (const [name, spec] of Object.entries(DISPLAY_PREFS)) {
        const item = res && res[spec.key];
        const storageKey = displayPrefStorageKey(uid, name);
        if (item && item.value != null && item.value !== '') {
          const value = clampDisplayPref(spec, item.value);
          next[name] = value;
          try { localStorage.setItem(storageKey, String(value)); } catch (_) {}
        } else {
          try { localStorage.removeItem(storageKey); } catch (_) {}
        }
      }
      state.displayPrefs = next;
      state.displayPrefsUserId = uid;
    } catch (_) {
      // A failed read must not block the shelf; per-user cached or default values stay in use.
    }
  }

  async function saveDisplayPref(name, raw) {
    const uid = (state.auth && state.auth.userId) || '';
    const spec = DISPLAY_PREFS[name];
    const value = clampDisplayPref(spec, raw);
    await api('/api/v1/client-settings/user', {
      method: 'PATCH',
      body: { [spec.key]: { value: String(value) } },
    });
    if (((state.auth && state.auth.userId) || '') === uid) {
      state.displayPrefs[name] = value;
      if (uid) {
        try { localStorage.setItem(displayPrefStorageKey(uid, name), String(value)); } catch (_) {}
      }
    }
    return value;
  }

  function displayPrefSelectHTML(name, label, values) {
    const current = displayPref(name);
    const options = values.slice();
    if (!options.includes(current)) options.push(current);
    options.sort((a, b) => a - b);
    const id = `disp-pref-${name}`;
    return `<div class="field" style="margin:0">
      <label for="${esc(id)}">${esc(label)}</label>
      <select id="${esc(id)}" data-display-pref="${esc(name)}">
        ${options.map((value) => `<option value="${value}" ${value === current ? 'selected' : ''}>${value} 项</option>`).join('')}
      </select>
    </div>`;
  }

  function bindDisplayPrefControls(root) {
    root.querySelectorAll('select[data-display-pref]').forEach((select) => {
      select.onchange = async () => {
        const name = select.getAttribute('data-display-pref');
        const previous = displayPref(name);
        select.disabled = true;
        try {
          const saved = await saveDisplayPref(name, select.value);
          toast(`已保存显示偏好：${saved} 项`);
        } catch (err) {
          select.value = String(previous);
          toast(err.message, 'error');
        } finally {
          select.disabled = false;
        }
      };
    });
  }

  function displayPrefCardHTML() {
    return `<div class="card">
      <h3>网页显示偏好（仅当前账号）</h3>
      <p style="font-size:12.5px;color:var(--text-secondary);margin-top:0">
        这些数量会保存到当前账号（Komga client settings 的 <code>webui.*</code> 命名空间），换设备登录同样生效；浏览器只保留一份本地缓存用于首屏加速。
      </p>
      <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:12px">
        ${displayPrefSelectHTML('shelfPageSize', '书架每页', [24, 36, 48, 60, 96])}
        ${displayPrefSelectHTML('homeContinueSize', '首页「继续阅读」', [4, 8, 12, 18, 24])}
        ${displayPrefSelectHTML('homeUpdatedSize', '首页「最近更新」', [8, 12, 18, 24, 36])}
        ${displayPrefSelectHTML('homeAddedSize', '首页「最新入库」', [8, 12, 18, 24, 36])}
      </div>
    </div>`;
  }

  async function copyTextSafeGlobal(txt) {
    if (navigator.clipboard && window.isSecureContext) {
      try { await navigator.clipboard.writeText(txt); return true; } catch (_) {}
    }
    const ta = document.createElement('textarea');
    ta.value = txt;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.left = '-9999px';
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try { ok = document.execCommand('copy'); } catch (_) {}
    document.body.removeChild(ta);
    return ok;
  }

  function resolveEffectiveTheme(mode) {
    if (mode === 'light' || mode === 'dark') return mode;
    return darkMediaQuery && darkMediaQuery.matches ? 'dark' : 'light';
  }

  function themeButtonLabel(mode) {
    if (mode === 'light') return '☀ 日间';
    if (mode === 'dark') return '☾ 夜间';
    return '◐ 自动';
  }

  function themeButtonTitle(mode) {
    if (mode === 'light') return '当前：固定日间模式（点击切换为夜间）';
    if (mode === 'dark') return '当前：固定夜间模式（点击切换为自动跟随系统）';
    return '当前：自动跟随系统日夜模式（点击切换为日间）';
  }

  function nextThemeMode(mode) {
    if (mode === 'auto') return 'light';
    if (mode === 'light') return 'dark';
    return 'auto';
  }

  function brandIconURL(theme) {
    return theme === 'dark' ? '/ui/mangaalpha-icon-dark.png' : '/ui/mangaalpha-icon.png';
  }

  function syncBrandIcons() {
    const src = brandIconURL(state.theme);
    const fav = document.getElementById('app-favicon') || document.querySelector('link[rel="icon"]');
    if (fav && fav.getAttribute('href') !== src) {
      fav.setAttribute('href', src);
    }
    document.querySelectorAll('img.brand-mark').forEach((img) => {
      if (img.getAttribute('src') !== src) img.setAttribute('src', src);
    });
  }

  function syncThemeButtons() {
    document.querySelectorAll('[data-theme-btn]').forEach((btn) => {
      btn.textContent = themeButtonLabel(state.themeMode);
      btn.title = themeButtonTitle(state.themeMode);
    });
  }

  function syncAndroidSystemBars() {
    try {
      if (window.FK115AndroidBridge && typeof window.FK115AndroidBridge.setSystemBarTheme === 'function') {
        const isLight = state.theme === 'light';
        window.FK115AndroidBridge.setSystemBarTheme(isLight ? '#f4f6fa' : '#0f1117', isLight);
      }
    } catch (_) {}
  }

  function setThemeMode(mode) {
    state.themeMode = (mode === 'light' || mode === 'dark') ? mode : 'auto';
    state.theme = resolveEffectiveTheme(state.themeMode);
    document.documentElement.setAttribute('data-theme', state.theme);
    localStorage.setItem(THEME_MODE_KEY, state.themeMode);
    syncThemeButtons();
    syncBrandIcons();
    syncAndroidSystemBars();
  }

  if (darkMediaQuery) {
    const onSysChange = () => {
      if (state.themeMode === 'auto') {
        state.theme = resolveEffectiveTheme('auto');
        document.documentElement.setAttribute('data-theme', state.theme);
        syncBrandIcons();
        syncAndroidSystemBars();
      }
    };
    if (typeof darkMediaQuery.addEventListener === 'function') {
      darkMediaQuery.addEventListener('change', onSysChange);
    } else if (typeof darkMediaQuery.addListener === 'function') {
      darkMediaQuery.addListener(onSysChange);
    }
  }
  window.addEventListener('storage', (ev) => {
    if (!ev.key || ev.key === THEME_MODE_KEY) {
      const m = localStorage.getItem(THEME_MODE_KEY) || 'auto';
      state.themeMode = (m === 'light' || m === 'dark') ? m : 'auto';
      state.theme = resolveEffectiveTheme(state.themeMode);
      document.documentElement.setAttribute('data-theme', state.theme);
      syncThemeButtons();
      syncBrandIcons();
      syncAndroidSystemBars();
    }
  });
  setThemeMode(state.themeMode);

  // Sync visualViewport height into --vvh and track mobile keyboard focus to prevent
  // Android WebView (e.g. Mihon) vertical layout oscillations while typing.
  function isEditableInput(el) {
    if (!el || !el.tagName) return false;
    if (el.tagName === 'TEXTAREA') return !el.readOnly && !el.disabled;
    if (el.tagName === 'INPUT') {
      const t = (el.type || 'text').toLowerCase();
      if (['button', 'checkbox', 'radio', 'submit', 'reset', 'file', 'hidden', 'range', 'color'].includes(t)) return false;
      return !el.readOnly && !el.disabled;
    }
    return !!el.isContentEditable;
  }

  function focusIfDesktop(el) {
    if (!el) return;
    if (window.matchMedia && window.matchMedia('(pointer: fine)').matches) {
      el.focus();
    }
  }

  (() => {
    let lastVVH = 0;
    let vvRaf = 0;
    const syncVVH = () => {
      vvRaf = 0;
      const h = Math.round((window.visualViewport ? window.visualViewport.height : window.innerHeight) || 0);
      if (h > 0 && Math.abs(h - lastVVH) >= 1) {
        lastVVH = h;
        document.documentElement.style.setProperty('--vvh', `${h}px`);
      }
    };
    const scheduleVVH = () => {
      if (!vvRaf) vvRaf = window.requestAnimationFrame(syncVVH);
    };
    syncVVH();
    if (window.visualViewport) {
      window.visualViewport.addEventListener('resize', scheduleVVH, { passive: true });
    }
    window.addEventListener('resize', scheduleVVH, { passive: true });

    let blurTimer = null;
    document.addEventListener('focusin', (e) => {
      if (blurTimer) {
        clearTimeout(blurTimer);
        blurTimer = null;
      }
      if (isEditableInput(e.target)) {
        document.documentElement.classList.add('kb-typing');
        scheduleVVH();
      } else {
        document.documentElement.classList.remove('kb-typing');
      }
    });
    document.addEventListener('focusout', () => {
      if (blurTimer) clearTimeout(blurTimer);
      blurTimer = setTimeout(() => {
        blurTimer = null;
        if (!isEditableInput(document.activeElement)) {
          document.documentElement.classList.remove('kb-typing');
        }
        scheduleVVH();
      }, 120);
    });
  })();

  function esc(val) {
    if (val === null || val === undefined) return '';
    return String(val)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function safeRedirectTarget(raw) {
    const val = String(raw || '').trim();
    if (!val || !val.startsWith('/') || val.startsWith('//') || val.includes('\\') || /[\x00-\x1f\x7f]/.test(val)) {
      return '/';
    }
    if (val.startsWith('/login') || val.startsWith('/setup')) return '/';
    return val;
  }

  function fmtBytes(n) {
    const v = Number(n || 0);
    if (!v || v < 1024) return `${v || 0} B`;
    const units = ['KB', 'MB', 'GB', 'TB'];
    let size = v;
    for (const u of units) {
      size /= 1024;
      if (size < 1024 || u === 'TB') return `${size.toFixed(1)} ${u}`;
    }
    return `${v} B`;
  }

  function fmtMiB(n) {
    const mib = Number(n || 0) / (1024 * 1024);
    if (!mib) return '0.0 MiB';
    if (mib < 1) return `${mib.toFixed(2)} MiB`;
    return `${mib.toFixed(1)} MiB`;
  }

  function bytesToMiBInput(rawBytes, fallbackMiB) {
    const n = Number(rawBytes);
    if (!Number.isFinite(n) || n < 0) return String(fallbackMiB);
    const mib = n / (1024 * 1024);
    const rounded = Math.round(mib * 100) / 100;
    return String(rounded);
  }

  function mibInputToBytes(rawVal) {
    const num = parseFloat(String(rawVal || '').trim());
    if (!Number.isFinite(num) || num < 0) return null;
    return String(Math.round(num * 1024 * 1024));
  }

  function fmtTime(iso) {
    if (!iso) return '—';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return String(iso);
    return d.toLocaleString('zh-CN', { hour12: false });
  }

  function fmtBgmCollectionType(t) {
    switch (Number(t)) {
      case 1: return '想读';
      case 2: return '读过';
      case 3: return '在读';
      case 4: return '搁置';
      case 5: return '抛弃';
      default: return '';
    }
  }

  function renderSeriesTrackerBadge(tr) {
    if (!tr || !tr.status || tr.status === 'idle') return '';
    if (tr.status === 'synced') {
      const parts = [];
      const typeLabel = fmtBgmCollectionType(tr.remoteType);
      if (typeLabel) parts.push(typeLabel);
      if (tr.remoteVolStatus > 0) parts.push(`第 ${tr.remoteVolStatus} 卷`);
      if (tr.remotePrivate) parts.push('私密');
      const summary = parts.length ? parts.join(' · ') : '已同步';
      const tip = tr.lastSyncedAt ? `最近同步：${fmtTime(tr.lastSyncedAt)}` : '已单向同步至 Bangumi';
      return `<span class="badge badge-success" title="${esc(tip)}">BGM 追踪：${esc(summary)}</span>`;
    }
    if (tr.status === 'skipped') {
      return `<span class="badge" title="${esc(tr.lastError || '已按设置跳过')}">BGM 追踪：已跳过 (NSFW)</span>`;
    }
    if (tr.status === 'failed') {
      return `<span class="badge badge-danger" title="${esc(tr.lastError || '同步失败')}">BGM 追踪失败</span><button type="button" class="btn btn-sm" id="btn-retry-bgm-tracker" style="padding:2px 8px;font-size:11.5px;min-height:24px">重试同步</button>`;
    }
    return '';
  }

  function providerLabel(p, webdavAccountName = '') {
    switch ((p || '115').toLowerCase()) {
      case 'local': return '本地 / SMB';
      case 'webdav': return webdavAccountName ? `WebDAV · ${webdavAccountName}` : 'WebDAV';
      case 'union': return '联合库';
      default: return '115 网盘';
    }
  }

  function libraryByID(libraryID) {
    if (!libraryID || !Array.isArray(state.libraries)) return null;
    return state.libraries.find((l) => l.id === libraryID) || null;
  }

  function librarySourceText(libraryID) {
    const lib = libraryByID(libraryID);
    if (!lib) return '';
    return `${lib.name} · ${providerLabel(lib.providerType, lib.webdavAccountName)}`;
  }

  async function ensureLibrariesLoaded() {
    if (Array.isArray(state.libraries) && state.libraries.length > 0) return state.libraries;
    try {
      state.libraries = (await api('/api/v1/libraries')) || [];
    } catch (_) {
      state.libraries = [];
    }
    return state.libraries;
  }

  const WEEKDAY_NAMES = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];

  function formatScheduleSummary(sched) {
    if (!sched || !sched.enabled) return '';
    const when = sched.frequency === 'weekly'
      ? `每${WEEKDAY_NAMES[Number(sched.weekday) || 0]} ${sched.timeOfDay || '03:00'}`
      : `每日 ${sched.timeOfDay || '03:00'}`;
    const parts = [when];
    if (sched.autoBangumi) parts.push('Bangumi');
    if (sched.autoCoverLimit === -1) parts.push('补封面不限');
    else if (Number(sched.autoCoverLimit) > 0) parts.push(`补封面≤${sched.autoCoverLimit}`);
    return parts.join(' · ');
  }

  function pipelineStageLabel(stage) {
    switch (stage) {
      case 'queued': return '排队中';
      case 'scan': return '扫描目录';
      case 'union_refresh': return '合并联合库';
      case 'bangumi': return 'Bangumi 刮削';
      case 'cover': return '补齐封面';
      case 'done': return '已完成';
      default: return stage || '—';
    }
  }

  function toast(msg, kind = 'ok') {
    const root = document.getElementById('toast-root');
    if (!root) return;
    const el = document.createElement('div');
    el.className = `toast ${kind === 'error' ? 'toast-error' : 'toast-ok'}`;
    el.textContent = msg;
    root.appendChild(el);
    setTimeout(() => { el.remove(); }, 3600);
  }

  function stopPolling() {
    if (state.pollTimer) {
      clearTimeout(state.pollTimer);
      state.pollTimer = null;
    }
    state.pollFn = null;
  }

  function schedulePoll(fn, ms = 2600) {
    if (state.pollTimer) {
      clearTimeout(state.pollTimer);
      state.pollTimer = null;
    }
    state.pollFn = fn;
    if (document.hidden) return;
    state.pollTimer = setTimeout(() => {
      state.pollTimer = null;
      if (!document.hidden && state.pollFn === fn) {
        fn();
      }
    }, ms);
  }

  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && state.pollFn && !state.pollTimer) {
      state.pollFn();
    }
  });

  async function api(url, opts = {}) {
    const method = (opts.method || 'GET').toUpperCase();
    const headers = Object.assign({ 'X-FK115-UI': '1' }, opts.headers || {});
    if (
      state.auth && state.auth.authenticated && state.auth.userId &&
      method !== 'GET' && method !== 'HEAD' &&
      !url.startsWith('/admin/api/auth/login') &&
      !url.startsWith('/admin/api/auth/logout') &&
      !url.startsWith('/admin/api/auth/setup')
    ) {
      headers['X-FK115-User-Id'] = state.auth.userId;
    }
    const fetchOpts = {
      method,
      credentials: 'same-origin',
      headers,
    };
    if (opts.body !== undefined) {
      headers['Content-Type'] = 'application/json';
      fetchOpts.body = JSON.stringify(opts.body);
    }
    const resp = await fetch(url, fetchOpts);
    if (resp.status === 409) {
      let errData = {};
      try { errData = await resp.clone().json(); } catch (_) {}
      if (errData && errData.error === 'USER_CONTEXT_MISMATCH') {
        route();
        throw new Error('登录账号已切换，页面已刷新，请重试');
      }
    }
    if (resp.status === 401 && !opts.allow401) {
      let errData = {};
      try { errData = await resp.json(); } catch (_) {}
      if (errData.error === 'SETUP_REQUIRED') {
        navigate('/setup', true);
      } else if (location.pathname !== '/login') {
        const redir = encodeURIComponent(location.pathname + location.search);
        navigate(`/login?redirect=${redir}`, true);
      }
      throw new Error(errData.message || '请先登录');
    }
    if (resp.status === 204) return null;
    const ct = resp.headers.get('content-type') || '';
    const data = ct.includes('application/json') ? await resp.json() : await resp.text();
    if (!resp.ok) {
      const msg = (data && typeof data === 'object' && (data.message || data.error)) || `请求失败 (${resp.status})`;
      const err = new Error(msg);
      err.status = resp.status;
      err.code = data && data.error;
      throw err;
    }
    return data;
  }

  let modalScroll = null;
  let activeConfirmCleanup = null;

  function hasAnyOverlayOpen() {
    const mr = document.getElementById('modal-root');
    const cr = document.getElementById('confirm-root');
    return Boolean((mr && mr.firstElementChild) || (cr && cr.firstElementChild));
  }

  function syncModalBodyLock() {
    if (hasAnyOverlayOpen()) {
      if (!modalScroll) modalScroll = { x: window.scrollX, y: window.scrollY };
      document.body.classList.add('modal-open');
      return;
    }
    document.body.classList.remove('modal-open');
    if (modalScroll) {
      const { x, y } = modalScroll;
      modalScroll = null;
      if (window.scrollX !== x || window.scrollY !== y) window.scrollTo(x, y);
    }
  }

  function closeModal() {
    const root = document.getElementById('modal-root');
    if (root) {
      if (root.contains(document.activeElement)) document.activeElement.blur();
      root.innerHTML = '';
    }
    syncModalBodyLock();
  }

  function openModal({ title, subheadHTML = '', bodyHTML, footHTML = '', maxWidth = '760px', onMount }) {
    const root = document.getElementById('modal-root');
    if (!modalScroll) modalScroll = { x: window.scrollX, y: window.scrollY };
    document.body.classList.add('modal-open');
    root.innerHTML = `
      <div class="modal-backdrop" id="modal-backdrop">
        <div class="modal-dialog" style="max-width:${esc(maxWidth)}">
          <div class="modal-head">
            <h3>${esc(title)}</h3>
            <button type="button" class="btn btn-sm" style="flex-shrink:0" id="modal-close-btn">关闭</button>
          </div>
          ${subheadHTML ? `<div class="modal-subhead" id="modal-subhead">${subheadHTML}</div>` : ''}
          <div class="modal-body" id="modal-body">${bodyHTML}</div>
          ${footHTML ? `<div class="modal-foot">${footHTML}</div>` : ''}
        </div>
      </div>`;
    document.getElementById('modal-close-btn').onclick = closeModal;
    document.getElementById('modal-backdrop').addEventListener('click', (e) => {
      if (e.target.id === 'modal-backdrop') closeModal();
    });
    if (onMount) onMount(document.getElementById('modal-body'));
  }

  function closeConfirmModal(accepted = false) {
    if (activeConfirmCleanup) {
      activeConfirmCleanup(Boolean(accepted));
    }
  }

  function confirmModal(message, opts = {}) {
    const {
      title = '请确认操作',
      confirmText = '确认',
      cancelText = '取消',
      danger = true,
    } = opts;
    if (activeConfirmCleanup) {
      activeConfirmCleanup(false);
    }
    let root = document.getElementById('confirm-root');
    if (!root) {
      root = document.createElement('div');
      root.id = 'confirm-root';
      document.body.appendChild(root);
    }
    const prevFocus = document.activeElement && typeof document.activeElement.focus === 'function'
      ? document.activeElement
      : null;
    const openedRouteSeq = routeSeq;
    const openedUserId = (state.auth && state.auth.userId) || '';

    return new Promise((resolve) => {
      let settled = false;
      const finish = (accepted) => {
        if (settled) return;
        settled = true;
        if (activeConfirmCleanup === finish) activeConfirmCleanup = null;
        document.removeEventListener('keydown', onKeyDown, true);
        if (root.contains(document.activeElement)) document.activeElement.blur();
        root.innerHTML = '';
        syncModalBodyLock();
        if (prevFocus && prevFocus.isConnected) {
          focusIfDesktop(prevFocus);
        }
        const currentUserId = (state.auth && state.auth.userId) || '';
        resolve(Boolean(accepted) && routeSeq === openedRouteSeq && currentUserId === openedUserId);
      };
      const onKeyDown = (e) => {
        if (e.key === 'Escape') {
          e.preventDefault();
          e.stopPropagation();
          finish(false);
          return;
        }
        if (e.key === 'Tab') {
          const focusables = [
            document.getElementById('confirm-close-btn'),
            document.getElementById('confirm-cancel-btn'),
            document.getElementById('confirm-ok-btn'),
          ].filter((el) => el && !el.disabled);
          if (focusables.length === 0) return;
          const idx = focusables.indexOf(document.activeElement);
          e.preventDefault();
          e.stopPropagation();
          if (e.shiftKey) {
            focusables[idx <= 0 ? focusables.length - 1 : idx - 1].focus();
          } else {
            focusables[idx < 0 || idx >= focusables.length - 1 ? 0 : idx + 1].focus();
          }
        }
      };

      activeConfirmCleanup = finish;
      if (!modalScroll) modalScroll = { x: window.scrollX, y: window.scrollY };
      document.body.classList.add('modal-open');
      root.innerHTML = `
        <div class="modal-backdrop confirm-backdrop" id="confirm-backdrop" role="dialog" aria-modal="true" aria-labelledby="confirm-modal-title">
          <div class="modal-dialog" style="max-width:440px">
            <div class="modal-head">
              <h3 id="confirm-modal-title">${esc(title)}</h3>
              <button type="button" class="btn btn-sm" style="flex-shrink:0" id="confirm-close-btn">关闭</button>
            </div>
            <div class="modal-body">
              <p class="confirm-message">${esc(message)}</p>
            </div>
            <div class="modal-foot">
              <button type="button" class="btn" id="confirm-cancel-btn">${esc(cancelText)}</button>
              <button type="button" class="btn ${danger ? 'btn-danger' : 'btn-primary'}" id="confirm-ok-btn">${esc(confirmText)}</button>
            </div>
          </div>
        </div>`;
      document.addEventListener('keydown', onKeyDown, true);
      document.getElementById('confirm-close-btn').onclick = () => finish(false);
      document.getElementById('confirm-cancel-btn').onclick = () => finish(false);
      document.getElementById('confirm-ok-btn').onclick = () => finish(true);
      document.getElementById('confirm-backdrop').addEventListener('click', (e) => {
        if (e.target.id === 'confirm-backdrop') finish(false);
      });
      focusIfDesktop(document.getElementById('confirm-cancel-btn'));
    });
  }

  function navigate(target, replace = false) {
    stopPolling();
    closeConfirmModal(false);
    closeModal();
    if (replace) {
      history.replaceState(null, '', target);
    } else {
      history.pushState(null, '', target);
    }
    route();
  }

  document.addEventListener('click', (e) => {
    const a = e.target.closest('a[data-nav]');
    if (!a) return;
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
    const href = a.getAttribute('href');
    if (!href || href.startsWith('/reader/') || href.startsWith('http')) return;
    e.preventDefault();
    navigate(href);
  });

  window.addEventListener('popstate', () => {
    stopPolling();
    closeConfirmModal(false);
    closeModal();
    route();
  });

  function renderShell(activeNav, innerHTML, searchVal = '') {
    const app = document.getElementById('app');
    const admin = isAdminUser();
    const accountLink = '/account';
    app.innerHTML = `
      <header class="topbar">
        <div class="topbar-inner">
          <a href="/" data-nav class="brand">
            <img class="brand-mark" src="${brandIconURL(state.theme)}" alt="">
            <span>MangaAlpha</span>
          </a>
          <nav class="topnav">
            <a href="/" data-nav class="${activeNav === 'home' ? 'active' : ''}">书架</a>
            <a href="/libraries" data-nav class="${activeNav === 'libraries' ? 'active' : ''}">浏览书库</a>
            <a href="/collections" data-nav class="${activeNav === 'collections' ? 'active' : ''}">收藏夹</a>
            <a href="/readlists" data-nav class="${activeNav === 'readlists' ? 'active' : ''}">阅读列表</a>
            <a href="/history" data-nav class="${activeNav === 'history' ? 'active' : ''}">历史</a>
            ${admin
              ? `<a href="/admin" data-nav class="${activeNav === 'admin' ? 'active' : ''}">管理后台</a>`
              : `<a href="/account" data-nav class="${activeNav === 'account' ? 'active' : ''}">账号设置</a>`}
          </nav>
          <form class="topbar-search" id="global-search-form">
            <span class="search-icon">⌕</span>
            <input type="text" id="global-search-input" placeholder="搜索书名、原目录名、作者…" value="${esc(searchVal)}">
          </form>
          <div class="topbar-actions">
            <button type="button" class="btn btn-sm" id="theme-toggle-btn" data-theme-btn title="${esc(themeButtonTitle(state.themeMode))}">${esc(themeButtonLabel(state.themeMode))}</button>
            ${state.auth.username ? `<a href="${accountLink}" data-nav class="user-pill" title="个人账号设置、显示偏好与 API Key">${esc(state.auth.username)}</a>` : ''}
            ${admin && activeNav === 'admin' && navigator.userAgent.includes('FK115Android/') ? '<a class="btn btn-sm" href="/__android/settings">手机端设置</a><a class="btn btn-sm" href="/__android/stop">停止服务</a>' : ''}
            <button type="button" class="btn btn-sm" id="logout-btn">退出</button>
          </div>
        </div>
      </header>
      <main class="page-shell" id="page-content">${innerHTML}</main>
    `;
    document.getElementById('theme-toggle-btn').onclick = () => {
      setThemeMode(nextThemeMode(state.themeMode));
    };
    document.getElementById('logout-btn').onclick = async () => {
      try { await api('/admin/api/auth/logout', { method: 'POST', allow401: true }); } catch (_) {}
      state.auth = { claimed: true, authenticated: false, userId: '', username: '', role: '', isAdmin: false, allowNsfw: false, showNsfw: false, allLibraries: false, version: '', codename: '', codenameQuote: '' };
      state.libraries = [];
      state.taxonomies = { loaded: false, genres: [], tags: [], publishers: [] };
      state.updateStatus = null;
      resetDisplayPrefs();
      navigate('/login', true);
    };
    document.getElementById('global-search-form').onsubmit = (e) => {
      e.preventDefault();
      const q = document.getElementById('global-search-input').value.trim();
      navigate(q ? `/libraries?q=${encodeURIComponent(q)}` : '/libraries');
    };
  }

  const mergedWorks = () => localStorage.getItem('fk115_merge_bangumi') === '1';

  function seriesVersionURL(seriesID, libraryID = '') {
    const scope = libraryID ? '&library_id=' + encodeURIComponent(libraryID) : '';
    return `/series/${encodeURIComponent(seriesID)}?versions=1${scope}`;
  }

  function seriesCardHTML(s, libraryID = '') {
    const meta = s.metadata || {};
    const title = meta.title || s.name || '未命名';
    const origDiff = s.name && s.name !== title ? s.name : '';
    const unread = Number(s.booksUnreadCount || 0);
    const total = Number(s.booksCount || 0);
    const read = Number(s.booksReadCount || 0);
    const pct = total > 0 ? Math.min(100, Math.round((read / total) * 100)) : 0;
    const score = typeof s.bgmScore === 'number' && s.bgmScore > 0 ? s.bgmScore.toFixed(1) : '';
    const sourceText = !libraryID ? librarySourceText(s.libraryId) : '';
    return `
      <a href="${mergedWorks() && s.bgmSubjectId && !s.oneshot ? seriesVersionURL(s.id, libraryID) : `/series/${encodeURIComponent(s.id)}`}" data-nav class="poster-card">
        <div class="poster-thumb">
          <img loading="lazy" src="/api/v1/series/${encodeURIComponent(s.id)}/thumbnail?v=${state.coverVer}" alt="${esc(title)}">
          ${score ? `<span class="poster-badge-top-left">★ ${esc(score)}</span>` : ''}
          ${unread > 0 ? `<span class="poster-badge-top-right">${unread}</span>` : ''}
          ${read > 0 ? `<div class="poster-progress-bar"><span style="width:${pct}%"></span></div>` : ''}
        </div>
        <div class="poster-body">
          <div class="poster-title" title="${esc(title)}">${esc(title)}</div>
          <div class="poster-sub" title="${esc(origDiff || `${total} 册`)}">${esc(origDiff || (s.oneshot ? '单本' : `共 ${total} 册`))}</div>
          ${sourceText ? `<div class="poster-source" title="${esc(sourceText)}">${esc(sourceText)}</div>` : ''}
        </div>
      </a>
    `;
  }

  // --- Setup & Login Views ---

  function renderSetup() {
    document.title = '初始化管理员 · MangaAlpha';
    const app = document.getElementById('app');
    app.innerHTML = `
      <div class="auth-screen">
        <form class="auth-card" id="setup-form">
          <div style="display:flex;align-items:center;justify-content:space-between;gap:8px">
            <div class="brand"><img class="brand-mark" src="${brandIconURL(state.theme)}" alt=""><span>MangaAlpha</span></div>
            <button type="button" class="btn btn-sm" id="setup-theme-btn" data-theme-btn title="${esc(themeButtonTitle(state.themeMode))}">${esc(themeButtonLabel(state.themeMode))}</button>
          </div>
          <h1>初始化管理员账号</h1>
          <p class="lead">欢迎使用 MangaAlpha。首次启用后请设置管理员用户名与登录密码，设置完成后书架、Komga 与 OPDS 接口均受鉴权保护。</p>
          <div class="field">
            <label for="setup-user">管理员用户名</label>
            <input id="setup-user" type="text" autocomplete="username" placeholder="请自定义用户名（2-64 个字符）" required>
          </div>
          <div class="field">
            <label for="setup-pass">登录密码</label>
            <input id="setup-pass" type="password" autocomplete="new-password" placeholder="至少 8 位字符" required>
          </div>
          <div class="field">
            <label for="setup-pass2">确认登录密码</label>
            <input id="setup-pass2" type="password" autocomplete="new-password" placeholder="再次输入登录密码" required>
          </div>
          <button type="submit" class="btn btn-primary btn-lg" style="width:100%;margin-top:6px" id="setup-submit">完成初始化并进入书架</button>
        </form>
      </div>`;
    document.getElementById('setup-theme-btn').onclick = () => setThemeMode(nextThemeMode(state.themeMode));
    focusIfDesktop(document.getElementById('setup-user'));
    document.getElementById('setup-form').onsubmit = async (e) => {
      e.preventDefault();
      const username = document.getElementById('setup-user').value.trim();
      const password = document.getElementById('setup-pass').value;
      const password2 = document.getElementById('setup-pass2').value;
      if (username.length < 2) { toast('用户名至少需要 2 个字符', 'error'); return; }
      if (password.length < 8) { toast('密码至少需要 8 位字符', 'error'); return; }
      if (password !== password2) { toast('两次输入的密码不一致', 'error'); return; }
      const btn = document.getElementById('setup-submit');
      btn.disabled = true;
      try {
        const res = await api('/admin/api/auth/setup', {
          method: 'POST',
          body: { username, password },
          allow401: true,
        });
        state.auth = {
          claimed: true,
          authenticated: true,
          userId: res.userId || 'admin',
          username: res.username,
          role: res.role || 'admin',
          isAdmin: true,
          allowNsfw: true,
          showNsfw: false,
          allLibraries: true,
        };
        state.libraries = [];
        state.taxonomies = { loaded: false, genres: [], tags: [], publishers: [] };
        resetDisplayPrefs();
        toast('管理员账号设置成功');
        navigate('/', true);
      } catch (err) {
        toast(err.message, 'error');
        btn.disabled = false;
      }
    };
  }

  function renderLogin() {
    document.title = '登录 · MangaAlpha';
    const params = new URLSearchParams(location.search);
    const redirect = params.get('redirect') || '/';
    const app = document.getElementById('app');
    app.innerHTML = `
      <div class="auth-screen">
        <form class="auth-card" id="login-form">
          <div style="display:flex;align-items:center;justify-content:space-between;gap:8px">
            <div class="brand"><img class="brand-mark" src="${brandIconURL(state.theme)}" alt=""><span>MangaAlpha</span></div>
            <button type="button" class="btn btn-sm" id="login-theme-btn" data-theme-btn title="${esc(themeButtonTitle(state.themeMode))}">${esc(themeButtonLabel(state.themeMode))}</button>
          </div>
          <h1>登录书架</h1>
          <p class="lead">请输入用户名与密码访问漫画书库与内置阅读器。</p>
          <div class="field">
            <label for="login-user">用户名</label>
            <input id="login-user" type="text" autocomplete="username" placeholder="请输入用户名" required>
          </div>
          <div class="field">
            <label for="login-pass">密码</label>
            <input id="login-pass" type="password" autocomplete="current-password" placeholder="登录密码" required>
          </div>
          <button type="submit" class="btn btn-primary btn-lg" style="width:100%;margin-top:6px" id="login-submit">登录</button>
        </form>
      </div>`;
    document.getElementById('login-theme-btn').onclick = () => setThemeMode(nextThemeMode(state.themeMode));
    focusIfDesktop(document.getElementById('login-user'));
    document.getElementById('login-form').onsubmit = async (e) => {
      e.preventDefault();
      const username = document.getElementById('login-user').value.trim();
      const password = document.getElementById('login-pass').value;
      const btn = document.getElementById('login-submit');
      btn.disabled = true;
      try {
        const res = await api('/admin/api/auth/login', {
          method: 'POST',
          body: { username, password },
          allow401: true,
        });
        state.auth = {
          claimed: true,
          authenticated: true,
          userId: res.userId || '',
          username: res.username,
          role: res.role || 'user',
          isAdmin: Boolean(res.isAdmin),
          allowNsfw: Boolean(res.allowNsfw),
          showNsfw: Boolean(res.showNsfw),
          allLibraries: Boolean(res.allLibraries),
        };
        state.libraries = [];
        state.taxonomies = { loaded: false, genres: [], tags: [], publishers: [] };
        resetDisplayPrefs();
        navigate(safeRedirectTarget(redirect), true);
      } catch (err) {
        toast(err.message || '用户名或密码错误', 'error');
        btn.disabled = false;
      }
    };
  }

  // --- Home View (/) ---

  function renderHome() {
    document.title = '书架 · MangaAlpha';
    const seq = routeSeq;
    renderShell('home', `<section id="home-libs">正在读取书库…</section>
      <section id="home-continue" aria-live="polite"></section>
      <section id="home-updated" aria-live="polite">正在加载最近更新…</section>
      <section id="home-added" aria-live="polite"></section>`);
    function load(slot, path, render) {
      api(path).then((data) => {
        if (seq !== routeSeq) return;
        const target = document.getElementById(slot);
        if (target) target.innerHTML = render(data);
      }).catch((err) => {
        if (seq !== routeSeq) return;
        const target = document.getElementById(slot);
        if (!target) return;
        target.innerHTML = `<div class="empty-box">${esc(err.message)} <button class="btn btn-sm">重试</button></div>`;
        target.querySelector('button').onclick = () => load(slot, path, render);
      });
    }
    const seriesURL = mergedWorks() ? '/admin/api/bookshelf/series?merge_bangumi=1&' : '/api/v1/series?';
    load('home-libs', '/api/v1/libraries', (libs) => {
      state.libraries = libs || [];
      let html = '';
      if (state.libraries.length === 0) {
        html += isAdminUser() ? `
          <div class="card" style="text-align:center;padding:38px 20px">
            <h2>欢迎来到 MangaAlpha 书架</h2>
            <p style="color:var(--text-secondary);max-width:540px;margin:8px auto 18px">
              当前暂无可展示的书库（尚未创建书库，或所有书库均已设为隐藏）。支持挂载 <strong>115 网盘目录</strong>、<strong>本地 / SMB 挂载目录</strong> 与 <strong>WebDAV 远程目录（CloudDrive2 / OpenList / NAS）</strong>，并支持 Bangumi 中文元数据刮削与内置简体中文双页阅读器。
            </p>
            <a href="/admin?tab=libraries" data-nav class="btn btn-primary btn-lg">前往管理后台管理书库</a>
          </div>` : `
          <div class="card" style="text-align:center;padding:38px 20px">
            <h2>欢迎来到 MangaAlpha 书架</h2>
            <p style="color:var(--text-secondary);max-width:540px;margin:8px auto 18px">
              当前账号暂无可访问的书库。请联系管理员在「用户管理」中为你分配书库访问权限。
            </p>
            <a href="/account" data-nav class="btn btn-primary">查看账号设置</a>
          </div>`;
      } else {
        html += `
          <section class="section-block">
            <div class="section-head">
              <h2>书库</h2>
              <a href="/libraries" data-nav class="more-link">浏览全部 →</a>
            </div>
            <div class="lib-strip">
              <a href="/libraries" data-nav class="lib-pill">全部书库</a>
              ${state.libraries.map((l) => `
                <a href="/libraries/${encodeURIComponent(l.id)}" data-nav class="lib-pill">
                  <span>${esc(l.name)}</span>
                  <span class="badge">${esc(providerLabel(l.providerType, l.webdavAccountName))}</span>
                  ${l.oneshotsDirectory ? `<span class="badge badge-accent">单本</span>` : ''}
                </a>
              `).join('')}
            </div>
          </section>`;
      }

      return html;
    });
    load('home-continue', '/api/v1/books?read_status=IN_PROGRESS&sort=readProgress.readDate,desc&size=' + displayPref('homeContinueSize'), (res) => {
      const contBooks = res?.content || [];
      let html = '';
      if (contBooks.length > 0) {
        html += `
          <section class="section-block">
            <div class="section-head">
              <h2>继续阅读</h2>
            </div>
            <div class="continue-grid">
              ${contBooks.map((b) => {
                const pg = (b.readProgress && b.readProgress.page) || 1;
                const totalPg = (b.media && b.media.pagesCount) || 0;
                const readerURL = `/reader/series/${encodeURIComponent(b.seriesId)}/book/${encodeURIComponent(b.id)}?page=${pg}`;
                return `
                  <div class="continue-card">
                    <a href="/series/${encodeURIComponent(b.seriesId)}" data-nav>
                      <img loading="lazy" src="/api/v1/series/${encodeURIComponent(b.seriesId)}/thumbnail?v=${state.coverVer}" alt="">
                    </a>
                    <div class="continue-info">
                      <a href="/series/${encodeURIComponent(b.seriesId)}" data-nav class="continue-series" title="${esc(b.seriesTitle)}">${esc(b.seriesTitle || b.name)}</a>
                      <div class="continue-book" title="${esc(b.name)}">${esc(b.metadata && b.metadata.title ? b.metadata.title : b.name)}</div>
                      <div><span class="badge badge-accent">第 ${pg}${totalPg > 0 ? ` / ${totalPg}` : ''} 页</span></div>
                    </div>
                    <a href="${readerURL}" class="btn btn-primary btn-sm">继续读</a>
                  </div>`;
              }).join('')}
            </div>
          </section>`;
      }

      return html;
    });
    load('home-updated', seriesURL + 'sort=lastModifiedDate,desc&size=' + displayPref('homeUpdatedSize'), (res) => {
      const updatedList = res?.content || [];
      let html = '';
      if (updatedList.length > 0) {
        html += `
          <section class="section-block">
            <div class="section-head">
              <h2>最近更新</h2>
              <a href="/libraries?sort=lastModifiedDate,desc" data-nav class="more-link">更多 →</a>
            </div>
            <div class="poster-grid">${updatedList.map((x) => seriesCardHTML(x)).join('')}</div>
          </section>`;
      }

      return html;
    });
    load('home-added', seriesURL + 'sort=createdDate,desc&size=' + displayPref('homeAddedSize'), (res) => {
      const addedList = res?.content || [];
      let html = '';
      if (addedList.length > 0) {
        html += `
          <section class="section-block">
            <div class="section-head">
              <h2>最新入库</h2>
              <a href="/libraries?sort=createdDate,desc" data-nav class="more-link">更多 →</a>
            </div>
            <div class="poster-grid">${addedList.map((x) => seriesCardHTML(x)).join('')}</div>
          </section>`;
      }

      return html;
    });
  }

  // --- Libraries Browse View ---

  async function renderLibraries(libraryID) {
    const seq = routeSeq;
    const sp = new URLSearchParams(location.search);
    const q = sp.get('q') || '';
    const readStatus = sp.get('read_status') || '';
    const scrapeStatus = sp.get('scrape_status') || '';
    const oneshot = sp.get('oneshot') || '';
    const seriesStatus = sp.get('status') || '';
    const genre = sp.get('genre') || '';
    const tag = sp.get('tag') || '';
    const publisher = sp.get('publisher') || '';
    const author = sp.get('author') || '';
    const sort = sp.get('sort') || 'lastModifiedDate,desc';
    const page = Math.max(0, parseInt(sp.get('page') || '0', 10) || 0);
    const size = displayPref('shelfPageSize');

    document.title = '浏览书库 · MangaAlpha';
    const oldList = document.getElementById('library-cards');
    const currentCards = oldList?.innerHTML || document.getElementById('library-updating')?.innerHTML || '';
    renderShell('libraries', currentCards
      ? `<p id="library-list-status" role="status">正在更新列表…</p><div id="library-updating" class="poster-grid" style="opacity:.5" inert>${currentCards}</div>`
      : '<p id="library-list-status" role="status">正在加载漫画列表…</p>', q);

    try {
      const apiParams = new URLSearchParams();
      apiParams.set('page', String(page));
      apiParams.set('size', String(size));
      apiParams.set('sort', sort);
      if (libraryID) apiParams.set('library_id', libraryID);
      if (q) apiParams.set('search', q);
      if (readStatus) apiParams.set('read_status', readStatus);
      if (scrapeStatus) apiParams.set('scrape_status', scrapeStatus);
      if (oneshot !== '') apiParams.set('oneshot', oneshot);
      if (seriesStatus) apiParams.set('status', seriesStatus);
      if (genre) apiParams.set('genre', genre);
      if (tag) apiParams.set('tag', tag);
      if (publisher) apiParams.set('publisher', publisher);
      if (author) apiParams.set('author', author);

      const seriesPath = mergedWorks() ? '/admin/api/bookshelf/series' : '/api/v1/series';
      if (mergedWorks()) apiParams.set('merge_bangumi', '1');
      const filters = [
        ['f-genre', '/api/v1/genres', '类型', genre],
        ['f-tag', '/api/v1/tags', '标签', tag],
        ['f-pub', '/api/v1/publishers', '出版社', publisher],
      ].map(async ([id, path, label, selected]) => {
        try {
          const values = await api(path);
          return [id, label, selected, values || [], false];
        } catch (_) {
          return [id, label, selected, [], true];
        }
      });
      const [libs, res] = await Promise.all([api('/api/v1/libraries'), api(`${seriesPath}?${apiParams.toString()}`)]);
      if (seq !== routeSeq) return;
      state.libraries = libs || [];
      const items = (res && res.content) || [];
      const totalElements = Number((res && res.totalElements) || 0);
      const totalPages = Number((res && res.totalPages) || 0);

      const buildURL = (overrides) => {
        const nextLib = overrides.libraryID !== undefined ? overrides.libraryID : libraryID;
        const base = nextLib ? `/libraries/${encodeURIComponent(nextLib)}` : '/libraries';
        const nextSp = new URLSearchParams(location.search);
        Object.entries(overrides).forEach(([k, v]) => {
          if (k === 'libraryID') return;
          if (v === '' || v === null || v === undefined) nextSp.delete(k);
          else nextSp.set(k, String(v));
        });
        if (overrides.page === undefined) nextSp.delete('page');
        const qs = nextSp.toString();
        return qs ? `${base}?${qs}` : base;
      };

      const content = document.getElementById('page-content');
      if (!content) return;
      content.innerHTML = `
        <div class="section-block" style="margin-bottom:16px">
          <div class="lib-strip">
            <a href="${esc(buildURL({ libraryID: '' }))}" data-nav class="lib-pill ${!libraryID ? 'active' : ''}">全部书库</a>
            ${state.libraries.map((l) => `
              <a href="${esc(buildURL({ libraryID: l.id }))}" data-nav class="lib-pill ${libraryID === l.id ? 'active' : ''}">
                <span>${esc(l.name)}</span>
                <span class="badge">${esc(providerLabel(l.providerType, l.webdavAccountName))}</span>
              </a>
            `).join('')}
          </div>
        </div>

        <div class="filter-panel">
          <div class="filter-row">
            <input type="text" id="f-q" class="filter-search-input" placeholder="关键词（中文译名 / 原文件夹名 / 别名 / 作者）" value="${esc(q)}">
            <select id="f-sort">
              <option value="lastModifiedDate,desc" ${sort === 'lastModifiedDate,desc' ? 'selected' : ''}>按最近更新</option>
              <option value="createdDate,desc" ${sort === 'createdDate,desc' ? 'selected' : ''}>按最新入库</option>
              <option value="metadata.titleSort,asc" ${sort === 'metadata.titleSort,asc' ? 'selected' : ''}>按标题 A-Z</option>
              <option value="bgmRank,asc" ${sort === 'bgmRank,asc' ? 'selected' : ''}>按 Bangumi 排名</option>
              <option value="bgmScore,desc" ${sort === 'bgmScore,desc' ? 'selected' : ''}>按 Bangumi 评分</option>
              <option value="random" ${sort === 'random' ? 'selected' : ''}>随机漫游</option>
            </select>
            <select id="f-read">
              <option value="">阅读：全部</option>
              <option value="UNREAD" ${readStatus === 'UNREAD' ? 'selected' : ''}>未读</option>
              <option value="IN_PROGRESS" ${readStatus === 'IN_PROGRESS' ? 'selected' : ''}>阅读中</option>
              <option value="READ" ${readStatus === 'READ' ? 'selected' : ''}>已读完</option>
            </select>
            <select id="f-scrape">
              <option value="">BGM：全部</option>
              <option value="matched" ${scrapeStatus === 'matched' ? 'selected' : ''}>已匹配 Bangumi</option>
              <option value="manual" ${scrapeStatus === 'manual' ? 'selected' : ''}>手动指定</option>
              <option value="unmatched" ${scrapeStatus === 'unmatched' ? 'selected' : ''}>自动未命中</option>
              <option value="none" ${(scrapeStatus === 'none' || scrapeStatus === 'pending') ? 'selected' : ''}>尚未刮削</option>
            </select>
            <select id="f-oneshot">
              <option value="">形态：全部</option>
              <option value="false" ${oneshot === 'false' ? 'selected' : ''}>多卷系列</option>
              <option value="true" ${oneshot === 'true' ? 'selected' : ''}>单本 One-Shot</option>
            </select>
          </div>
          <div class="filter-row">
            <select id="f-status">
              <option value="">连载：全部</option>
              <option value="ONGOING" ${seriesStatus === 'ONGOING' ? 'selected' : ''}>连载中</option>
              <option value="ENDED" ${seriesStatus === 'ENDED' ? 'selected' : ''}>已完结</option>
              <option value="HIATUS" ${seriesStatus === 'HIATUS' ? 'selected' : ''}>休刊</option>
              <option value="ABANDONED" ${seriesStatus === 'ABANDONED' ? 'selected' : ''}>中止</option>
            </select>
            <select id="f-genre" disabled>
              <option value="">类型加载中…</option>
              ${genre ? `<option value="${esc(genre)}" selected>${esc(genre)}</option>` : ''}
            </select>
            <select id="f-tag" disabled>
              <option value="">标签加载中…</option>
              ${tag ? `<option value="${esc(tag)}" selected>${esc(tag)}</option>` : ''}
            </select>
            <select id="f-pub" disabled>
              <option value="">出版社加载中…</option>
              ${publisher ? `<option value="${esc(publisher)}" selected>${esc(publisher)}</option>` : ''}
            </select>
            <input type="text" id="f-author" class="filter-author-input" placeholder="作者筛选" value="${esc(author)}">
            <button type="button" class="btn btn-primary btn-sm" id="f-apply">筛选</button>
            <button type="button" class="btn btn-sm" id="f-reset">重置</button>
            <label class="check-row filter-merge-toggle" title="只在内置网页版合并展示，不改卷列表与阅读进度">
              <input type="checkbox" id="f-merge-bgm" ${mergedWorks() ? 'checked' : ''}>合并相同 Bangumi ID
            </label>
            <span class="filter-count" style="margin-left:auto;color:var(--text-muted);font-size:12.5px">共 ${totalElements.toLocaleString()} 部作品</span>
          </div>
        </div>

        ${items.length > 0
          ? `<div class="poster-grid" id="library-cards">${items.map((item) => seriesCardHTML(item, libraryID)).join('')}</div>`
          : `<div class="empty-box">没有找到符合筛选条件的漫画作品。</div>`}

        ${totalPages > 1 ? `
          <div class="pagination">
            <button type="button" class="btn btn-sm" id="pg-prev" ${page <= 0 ? 'disabled' : ''}>上一页</button>
            <span style="color:var(--text-secondary)">第 ${page + 1} / ${totalPages} 页</span>
            <button type="button" class="btn btn-sm" id="pg-next" ${page + 1 >= totalPages ? 'disabled' : ''}>下一页</button>
          </div>` : ''}
      `;

      const applyFilters = () => {
        navigate(buildURL({
          q: document.getElementById('f-q').value.trim(),
          sort: document.getElementById('f-sort').value,
          read_status: document.getElementById('f-read').value,
          scrape_status: document.getElementById('f-scrape').value,
          oneshot: document.getElementById('f-oneshot').value,
          status: document.getElementById('f-status').value,
          genre: document.getElementById('f-genre').value,
          tag: document.getElementById('f-tag').value,
          publisher: document.getElementById('f-pub').value,
          author: document.getElementById('f-author').value.trim(),
        }));
      };
      document.getElementById('f-apply').onclick = applyFilters;
      document.getElementById('f-q').addEventListener('keydown', (e) => { if (e.key === 'Enter') applyFilters(); });
      document.getElementById('f-author').addEventListener('keydown', (e) => { if (e.key === 'Enter') applyFilters(); });
      ['f-sort', 'f-read', 'f-scrape', 'f-oneshot', 'f-status', 'f-genre', 'f-tag', 'f-pub'].forEach((id) => {
        document.getElementById(id).onchange = applyFilters;
      });
      document.getElementById('f-merge-bgm').onchange = (e) => {
        localStorage.setItem('fk115_merge_bangumi', e.target.checked ? '1' : '0');
        navigate(buildURL({}), true);
      };
      filters.forEach(async (loaded) => {
        const [id, label, selected, values, failed] = await loaded;
        if (seq !== routeSeq) return;
        const select = document.getElementById(id);
        if (!select) return;
        const choices = values.slice(0, id === 'f-genre' ? undefined : 150);
        if (selected && !choices.includes(selected)) choices.unshift(selected);
        select.innerHTML = `<option value="">${label}：${failed ? '加载失败' : '全部'}</option>` +
          choices.map((v) => `<option value="${esc(v)}" ${v === selected ? 'selected' : ''}>${esc(v)}</option>`).join('');
        select.disabled = failed && !selected;
      });
      document.getElementById('f-reset').onclick = () => {
        navigate(libraryID ? `/libraries/${encodeURIComponent(libraryID)}` : '/libraries');
      };
      if (totalPages > 1) {
        document.getElementById('pg-prev').onclick = () => navigate(buildURL({ page: page - 1 }));
        document.getElementById('pg-next').onclick = () => navigate(buildURL({ page: page + 1 }));
      }
    } catch (err) {
      if (seq !== routeSeq) return;
      const status = document.getElementById('library-list-status');
      if (status) {
        status.innerHTML = `列表加载失败：${esc(err.message)} <button type="button" class="btn btn-sm">重试</button>`;
        status.querySelector('button').onclick = () => renderLibraries(libraryID);
      }
    }
  }

  // --- Series Detail View (/series/:id) ---

  function pickStartBook(books) {
    if (!books || books.length === 0) return null;
    const bookLabel = (b) => (b.metadata && b.metadata.title) ? b.metadata.title : b.name;
    const inProg = books.find((b) => b.readProgress && !b.readProgress.completed && b.readProgress.page > 0);
    if (inProg) {
      const mid = bookLabel(inProg);
      const page = inProg.readProgress.page;
      return {
        book: inProg,
        page,
        prefix: '继续阅读 ·\u00a0',
        middle: mid,
        suffix: `（第 ${page} 页）`,
        fullLabel: `继续阅读 · ${mid}（第 ${page} 页）`,
      };
    }
    const firstUnread = books.find((b) => !b.readProgress || !b.readProgress.completed);
    if (firstUnread) {
      const hasAnyRead = books.some((b) => b.readProgress && b.readProgress.completed);
      if (hasAnyRead) {
        const mid = bookLabel(firstUnread);
        return {
          book: firstUnread,
          page: 1,
          prefix: '继续读下一卷 ·\u00a0',
          middle: mid,
          suffix: '',
          fullLabel: `继续读下一卷 · ${mid}`,
        };
      }
      return {
        book: firstUnread,
        page: 1,
        prefix: '开始阅读',
        middle: '',
        suffix: '',
        fullLabel: '开始阅读',
      };
    }
    return {
      book: books[0],
      page: 1,
      prefix: '从第一卷重读',
      middle: '',
      suffix: '',
      fullLabel: '从第一卷重读',
    };
  }

  async function renderSeriesDetail(seriesID) {
    const seq = routeSeq;
    renderShell('libraries', `<div class="boot-splash" style="min-height:45vh"><div class="spinner"></div><p>正在读取作品详情…</p></div>`);
    try {
      const [detail, maintJobs] = await Promise.all([
        api(`/admin/api/series/${encodeURIComponent(seriesID)}/detail`),
        isAdminUser()
          ? api(`/admin/api/maintenance-jobs?targetType=series&targetId=${encodeURIComponent(seriesID)}&limit=6`).catch(() => [])
          : Promise.resolve([]),
      ]);
      if (seq !== routeSeq) return;
      paintSeriesDetail(seriesID, detail, maintJobs || []);
    } catch (err) {
      toast(err.message, 'error');
    }
  }

  function paintSeriesDetail(seriesID, detail, maintJobs) {
    const s = detail.series;
    const meta = s.metadata || {};
    const rawMeta = detail.rawMeta || {};
    const books = detail.books || [];
    const extras = detail.bookExtras || {};
    const lib = detail.library || {};
    const title = meta.title || detail.originalName;
    document.title = `${title} · MangaAlpha`;

    const startTarget = pickStartBook(books);
    const coverSrc = rawMeta.coverSource || 'auto';
    const bgmID = rawMeta.bgmSubjectId || 0;
    const bgmScore = typeof rawMeta.bgmScore === 'number' && rawMeta.bgmScore > 0 ? rawMeta.bgmScore.toFixed(1) : '';
    const authors = (rawMeta.authors || []);
    const genres = (meta.genres || []);
    const tags = (meta.tags || []);
    const altTitles = (meta.alternateTitles || []);

    const content = document.getElementById('page-content');
    if (!content) return;
    const admin = isAdminUser();

    content.innerHTML = `
      <div style="margin-bottom:12px;font-size:13px;color:var(--text-muted);overflow-wrap:anywhere;word-break:break-word">
        <a href="/libraries/${encodeURIComponent(lib.id || '')}" data-nav style="color:var(--accent)">${esc(lib.name || '书库')}</a>
        <span> / </span>
        <span>${esc(title)}</span>
      </div>

      <section class="detail-hero">
        <div class="detail-cover-col">
          <div class="detail-cover-frame">
            <img id="series-cover-img" src="/api/v1/series/${encodeURIComponent(seriesID)}/thumbnail?v=${state.coverVer}" alt="${esc(title)}">
          </div>
          ${admin ? `
            <div class="segmented" title="切换封面显示来源">
              <button type="button" data-cover-src="auto" class="${coverSrc === 'auto' ? 'active' : ''}">自动</button>
              <button type="button" data-cover-src="bangumi" class="${coverSrc === 'bangumi' ? 'active' : ''}">Bangumi</button>
              <button type="button" data-cover-src="book" class="${coverSrc === 'book' ? 'active' : ''}">首卷图</button>
            </div>` : ''}
        </div>

        <div class="detail-main">
          <div class="detail-badges">
            <span class="badge">${esc(lib.name || '书库')}</span>
            <span class="badge">${esc(providerLabel(lib.providerType, lib.webdavAccountName))}</span>
            <span class="badge">${s.oneshot ? '单本 One-Shot' : `共 ${books.length} 册 · ${esc(detail.totalSize)}`}</span>
            <span class="badge badge-accent">${esc(meta.status || 'ONGOING')}</span>
            ${rawMeta.isNsfw ? `<span class="badge badge-danger">NSFW</span>` : ''}
            ${meta.publisher ? `<a href="/libraries?publisher=${encodeURIComponent(meta.publisher)}" data-nav class="badge">${esc(meta.publisher)}</a>` : ''}
            ${bgmID > 0
              ? `<a href="https://bgm.tv/subject/${bgmID}" target="_blank" rel="noopener noreferrer" class="badge badge-bgm" title="若打开提示“数据库中没有查询您所指定的条目”，说明该条目在 Bangumi 网页端受登录保护，请先在浏览器登录 bgm.tv 账号后再访问">Bangumi #${bgmID}${bgmScore ? ` · ★ ${bgmScore}` : ''} ↗</a>`
              : `<span class="badge">${rawMeta.scrapeStatus === 'unmatched' ? 'Bangumi 未命中' : '尚未匹配 Bangumi'}</span>`}
            ${bgmID > 0 ? renderSeriesTrackerBadge(detail.tracker) : ''}
          </div>

          <h1>${esc(title)}</h1>
          <div class="detail-orig">
            ${detail.originalName !== title ? `<div>原始目录名：<strong>${esc(detail.originalName)}</strong></div>` : ''}
            <div>所属书库：<strong>${esc(lib.name || '书库')}</strong>（${esc(providerLabel(lib.providerType, lib.webdavAccountName))}）</div>
            <div>相对路径：${esc(detail.relativePath)}</div>
            ${Array.isArray(detail.unionSeriesSources) && detail.unionSeriesSources.length > 0
              ? `<div>联合库来源层（按优先级）：${detail.unionSeriesSources.map((src, i) => `<strong>#${i + 1} ${esc(src.sourceLibraryName)}</strong> (${esc(providerLabel(src.providerType, src.webdavAccountName))})`).join(' → ')}</div>`
              : ''}
            ${bgmID > 0 ? `<div style="margin-top:2px">提示：部分带受限标签的条目在未登录 <code>bgm.tv</code> 时网页会提示“没有查询您所指定的条目”，登录 Bangumi 账号后即可正常打开。</div>` : ''}
          </div>

          <div id="series-versions"></div>
          <div class="detail-cta">
            ${startTarget ? `<a href="/reader/series/${encodeURIComponent(seriesID)}/book/${encodeURIComponent(startTarget.book.id)}?page=${startTarget.page}" class="btn btn-primary btn-lg btn-cta-read" title="${esc(startTarget.fullLabel)}"><span class="btn-part-fixed">▶\u00a0${esc(startTarget.prefix)}</span>${startTarget.middle ? `<span class="btn-part-ellipsis">${esc(startTarget.middle)}</span>` : ''}${startTarget.suffix ? `<span class="btn-part-fixed">${esc(startTarget.suffix)}</span>` : ''}</a>` : ''}
            ${admin ? `<button type="button" class="btn" id="series-generate-cover" data-maint="series_thumbnail" data-force="false"
              ${!books.length || maintJobs.some((j) => j.operation === 'series_thumbnail' && ['queued','running'].includes(j.status)) ? 'disabled' : ''}>生成首卷封面</button>
            <button type="button" class="btn btn-bgm" id="btn-open-bgm">Bangumi 刮削 / 匹配</button>
            <button type="button" class="btn" id="btn-edit-meta">编辑元数据</button>` : ''}
            <button type="button" class="btn btn-sm" id="btn-series-collections">+ 收藏夹</button>
            <button type="button" class="btn btn-sm" id="btn-mark-series-read">全部标为已读</button>
            <button type="button" class="btn btn-sm" id="btn-reset-series-read">重置未读</button>
          </div>

          ${meta.summary ? `<p class="detail-summary">${esc(meta.summary)}</p>` : ''}

          <div id="series-collections-bar" class="chip-group" style="display:none"></div>

          ${authors.length > 0 ? `
            <div class="chip-group">
              <span style="font-size:12px;color:var(--text-muted)">作者：</span>
              ${authors.map((a) => `<a href="/libraries?author=${encodeURIComponent(a.name)}" data-nav class="chip">${esc(a.name)}${a.role && a.role !== 'writer' ? ` (${esc(a.role)})` : ''}</a>`).join('')}
            </div>` : ''}

          ${genres.length > 0 ? `
            <div class="chip-group">
              <span style="font-size:12px;color:var(--text-muted)">分类：</span>
              ${genres.map((g) => `<a href="/libraries?genre=${encodeURIComponent(g)}" data-nav class="chip">${esc(g)}</a>`).join('')}
            </div>` : ''}

          ${tags.length > 0 ? `
            <div class="chip-group">
              <span style="font-size:12px;color:var(--text-muted)">标签：</span>
              ${tags.map((t) => `<a href="/libraries?tag=${encodeURIComponent(t)}" data-nav class="chip"># ${esc(t)}</a>`).join('')}
            </div>` : ''}

          ${altTitles.length > 0 ? `
            <div style="font-size:12px;color:var(--text-muted);margin-top:8px">
              别名：${altTitles.map((at) => esc(at.title)).join(' / ')}
            </div>` : ''}
        </div>
      </section>

      ${admin ? `
      <details class="card" id="series-maint-panel" ${maintJobs.some((j) => j.status === 'queued' || j.status === 'running') ? 'open' : ''}>
        <summary style="cursor:pointer;font-weight:600;user-select:none">归档索引、推断阅读进度与真实下载统计（Mihon / 维护面板）</summary>
        <div style="margin-top:14px;display:grid;gap:10px;font-size:13px">
          <div><strong>Mihon / Komga 同步进度：</strong>已读 ${s.booksReadCount} / ${s.booksCount} 本${s.booksInProgressCount > 0 ? `（阅读中 ${s.booksInProgressCount} 本）` : ''}</div>
          ${detail.inferred ? `<div><strong>页面流推断进度：</strong>最近 ${esc(detail.inferred.last)} · 最大 ${esc(detail.inferred.max)}</div>` : ''}
          <div><strong>归档索引统计：</strong>${esc(detail.indexSummary)}</div>
          <div><strong>系列封面统计：</strong>${esc(detail.coverSummary)}</div>
          <div><strong>真实 Range 下载统计：</strong>${esc(detail.downloadStat)}</div>
          <div style="display:flex;flex-wrap:wrap;gap:8px;margin-top:6px">
            <button type="button" class="btn btn-sm" data-maint="series_index" data-force="false">生成全部缺失索引</button>
            <button type="button" class="btn btn-sm" data-maint="series_index" data-force="true">强制重建全部索引</button>
            <button type="button" class="btn btn-sm" data-maint="series_thumbnail" data-force="false">生成本地首卷封面</button>
            <button type="button" class="btn btn-sm" data-maint="series_thumbnail" data-force="true">强制重建本地封面</button>
          </div>
          <div id="series-maint-jobs">${renderMaintJobRows(maintJobs)}</div>
        </div>
      </details>` : ''}

      <section class="card">
        <div class="section-head" style="margin-bottom:12px">
          <h2>卷列表（${books.length}）</h2>
        </div>
        <div class="volume-list">
          ${books.map((b) => {
            const ex = extras[b.id] || {};
            const unionSrcs = Array.isArray(ex.unionSources) ? ex.unionSources : [];
            const activeSrc = unionSrcs[0] || null;
            const backupSrcs = unionSrcs.slice(1);
            const rp = b.readProgress;
            const pgCount = (b.media && b.media.pagesCount) || 0;
            const startPg = rp && !rp.completed && rp.page > 0 ? rp.page : 1;
            let stateBadge = `<span class="badge">未读</span>`;
            if (rp && rp.completed) stateBadge = `<span class="badge badge-success">已读</span>`;
            else if (rp && rp.page > 0) stateBadge = `<span class="badge badge-accent">第 ${rp.page}${pgCount > 0 ? ` / ${pgCount}` : ''} 页</span>`;
            return `
              <div class="volume-row">
                <div class="volume-info">
                  <div class="volume-title">
                    <a href="/book/${encodeURIComponent(b.id)}" data-nav>${esc(b.name)}</a>
                  </div>
                  <div class="volume-meta">
                    ${stateBadge}
                    ${activeSrc ? `<span class="badge" title="当前生效读取层">${esc(activeSrc.sourceLibraryName)} · ${esc(providerLabel(activeSrc.providerType, activeSrc.webdavAccountName))}</span>` : ''}
                    ${backupSrcs.length > 0 ? `<span class="badge" title="备用回退副本：${esc(backupSrcs.map((x) => `${x.sourceLibraryName} (${providerLabel(x.providerType, x.webdavAccountName)}, ${fmtBytes(x.sourceSize)})`).join(' / '))}">+${backupSrcs.length} 备用副本</span>` : ''}
                    <span>${esc(b.size)}</span>
                    <span>${pgCount > 0 ? `${pgCount} 页` : '未建页数索引'}</span>
                    ${ex.indexStatus ? `<span>${esc(ex.indexStatus)}</span>` : ''}
                    ${ex.downloadStat ? `<span>${esc(ex.downloadStat)}</span>` : ''}
                    ${ex.inferredProgress ? `<span>${esc(ex.inferredProgress)}</span>` : ''}
                  </div>
                </div>
                <div class="volume-actions">
                  <a href="/reader/series/${encodeURIComponent(seriesID)}/book/${encodeURIComponent(b.id)}?page=${startPg}" class="btn btn-primary btn-sm">${rp && !rp.completed && rp.page > 0 ? '继续读' : '阅读'}</a>
                  <a href="/book/${encodeURIComponent(b.id)}" data-nav class="btn btn-sm">详情</a>
                  <button type="button" class="btn btn-sm" data-book-readlist="${esc(b.id)}" data-book-name="${esc(b.name)}">+ 列表</button>
                  ${rp && rp.completed
                    ? `<button type="button" class="btn btn-sm" data-book-unread="${esc(b.id)}">标未读</button>`
                    : `<button type="button" class="btn btn-sm" data-book-read="${esc(b.id)}">标已读</button>`}
                </div>
              </div>`;
          }).join('')}
        </div>
      </section>
    `;

    content.querySelectorAll('button[data-cover-src]').forEach((btn) => {
      btn.onclick = async () => {
        const src = btn.getAttribute('data-cover-src');
        try {
          await api(`/api/v1/series/${encodeURIComponent(seriesID)}/metadata`, {
            method: 'PATCH',
            body: { coverSource: src },
          });
          state.coverVer = Date.now();
          toast('封面来源已切换');
          renderSeriesDetail(seriesID);
        } catch (err) { toast(err.message, 'error'); }
      };
    });

    const btnOpenBgm = document.getElementById('btn-open-bgm');
    if (btnOpenBgm) btnOpenBgm.onclick = () => openBangumiMatchModal(seriesID, detail);
    const retryTrackerBtn = document.getElementById('btn-retry-bgm-tracker');
    if (retryTrackerBtn) {
      retryTrackerBtn.onclick = async () => {
        retryTrackerBtn.disabled = true;
        retryTrackerBtn.textContent = '同步中…';
        try {
          await api(`/admin/api/series/${encodeURIComponent(seriesID)}/bangumi/tracker/retry`, { method: 'POST' });
          toast('已同步至 Bangumi');
          renderSeriesDetail(seriesID);
        } catch (err) {
          toast(err.message || 'Bangumi 同步失败', 'error');
          renderSeriesDetail(seriesID);
        }
      };
    }
    loadSeriesVersions(seriesID, bgmID);
    loadSeriesCollectionsBar(seriesID);
    const btnEditMeta = document.getElementById('btn-edit-meta');
    if (btnEditMeta) btnEditMeta.onclick = () => openMetadataEditModal(seriesID, detail);
    document.getElementById('btn-series-collections').onclick = () => openAddSeriesToCollectionModal(seriesID, title, () => loadSeriesCollectionsBar(seriesID));

    document.getElementById('btn-mark-series-read').onclick = async () => {
      try {
        await api(`/api/v1/series/${encodeURIComponent(seriesID)}/read-progress`, { method: 'POST' });
        toast('已将整部作品标记为已读');
        renderSeriesDetail(seriesID);
      } catch (err) { toast(err.message, 'error'); }
    };
    const resetSeriesReadBtn = document.getElementById('btn-reset-series-read');
    resetSeriesReadBtn.onclick = async () => {
      if (!(await confirmModal('确认清空整部作品的阅读进度？', { title: '清空阅读进度', confirmText: '确认清空' }))) return;
      if (!resetSeriesReadBtn.isConnected) return;
      try {
        await api(`/api/v1/series/${encodeURIComponent(seriesID)}/read-progress`, { method: 'DELETE' });
        toast('阅读进度已重置');
        renderSeriesDetail(seriesID);
      } catch (err) { toast(err.message, 'error'); }
    };

    content.querySelectorAll('button[data-book-read]').forEach((btn) => {
      btn.onclick = async () => {
        const bid = btn.getAttribute('data-book-read');
        try {
          await api(`/api/v1/books/${encodeURIComponent(bid)}/read-progress`, { method: 'PATCH', body: { completed: true } });
          renderSeriesDetail(seriesID);
        } catch (err) { toast(err.message, 'error'); }
      };
    });
    content.querySelectorAll('button[data-book-unread]').forEach((btn) => {
      btn.onclick = async () => {
        const bid = btn.getAttribute('data-book-unread');
        try {
          await api(`/api/v1/books/${encodeURIComponent(bid)}/read-progress`, { method: 'DELETE' });
          renderSeriesDetail(seriesID);
        } catch (err) { toast(err.message, 'error'); }
      };
    });
    content.querySelectorAll('button[data-book-readlist]').forEach((btn) => {
      btn.onclick = () => {
        openAddBookToReadListModal(
          btn.getAttribute('data-book-readlist'),
          btn.getAttribute('data-book-name') || '单册',
        );
      };
    });

    content.querySelectorAll('button[data-maint]').forEach((btn) => {
      btn.onclick = async () => {
        const operation = btn.getAttribute('data-maint');
        const force = btn.getAttribute('data-force') === 'true';
        btn.disabled = true;
        try {
          await api('/admin/api/maintenance-jobs', {
            method: 'POST',
            body: { operation, seriesId: seriesID, force },
          });
          if (!btn.isConnected) return;
          const panel = document.getElementById('series-maint-panel');
          if (panel) panel.open = true;
          toast(operation === 'series_thumbnail' ? '首卷封面任务已加入队列' : '维护任务已加入队列');
          pollSeriesMaint(seriesID);
        } catch (err) {
          if (btn.isConnected) btn.disabled = false;
          toast(err.message, 'error');
        }
      };
    });

    bindMaintCancelButtons(content, () => pollSeriesMaint(seriesID));
    if (maintJobs.some((j) => j.status === 'queued' || j.status === 'running')) {
      schedulePoll(() => pollSeriesMaint(seriesID), 2000);
    }
  }

  function loadSeriesVersions(seriesID, bgmID) {
    if (!bgmID || Number(bgmID) <= 0) return;
    const qs = new URLSearchParams(location.search);
    const scope = qs.get('library_id') || '';
    const keepVersionsFlag = qs.get('versions') === '1';
    const seq = routeSeq;
    const box = document.getElementById('series-versions');
    const targetURL = (id) => {
      const params = new URLSearchParams();
      if (keepVersionsFlag) params.set('versions', '1');
      if (scope) params.set('library_id', scope);
      const q = params.toString();
      return `/series/${encodeURIComponent(id)}${q ? '?' + q : ''}`;
    };
    api(`/admin/api/bookshelf/versions/${encodeURIComponent(seriesID)}?library_id=${encodeURIComponent(scope)}`)
      .then(({ versions, lastRead }) => {
        if (seq !== routeSeq || !box?.isConnected || (versions || []).length < 2) return;
        const currentVer = versions.find((v) => v.id === seriesID) || versions[0];
        let resume = '';
        if (lastRead && !lastRead.completed) {
          resume = `<a class="btn btn-primary btn-sm" data-stop-toggle href="/reader/series/${encodeURIComponent(lastRead.seriesId)}/book/${encodeURIComponent(lastRead.bookId)}?page=${Number(lastRead.page) || 1}">继续最近阅读</a>`;
        } else if (lastRead && lastRead.seriesId !== seriesID) {
          resume = `<a class="btn btn-sm" data-stop-toggle href="${targetURL(lastRead.seriesId)}" data-nav>回到最近读的版本</a>`;
        }
        box.innerHTML = `
          <details class="series-versions-panel">
            <summary class="series-versions-summary">
              <div class="series-versions-head">
                <span class="series-versions-title">
                  <span class="series-versions-caret">▶</span>
                  <span class="series-versions-label">多版本切换</span>
                  <span class="badge badge-accent series-versions-count">共 ${versions.length} 个版本</span>
                </span>
                ${resume ? `<span class="series-versions-resume">${resume}</span>` : ''}
              </div>
              <div class="series-versions-current">当前：${esc(currentVer.library)} · ${esc(providerLabel(currentVer.provider))}（${Number(currentVer.booksCount)} 册）</div>
            </summary>
            <div class="series-versions-list">
              ${versions.map((v) => {
                const isCur = v.id === seriesID;
                const isLast = lastRead && lastRead.seriesId === v.id;
                return `
                  <a class="series-version-item ${isCur ? 'is-current' : ''}" href="${targetURL(v.id)}" data-nav>
                    <span class="series-version-main">
                      <span class="series-version-name" title="${esc(v.name)}">${esc(v.name)}</span>
                      <span class="series-version-meta">${esc(v.library)} · ${esc(providerLabel(v.provider))} · ${Number(v.booksCount)} 册</span>
                    </span>
                    <span class="series-version-actions">
                      ${isLast ? `<span class="badge">最近阅读</span>` : ''}
                      ${isCur ? `<span class="badge badge-accent">当前</span>` : `<span class="btn btn-sm">切换</span>`}
                    </span>
                  </a>`;
              }).join('')}
            </div>
          </details>`;
        box.querySelectorAll('[data-stop-toggle]').forEach((el) => {
          el.addEventListener('click', (ev) => ev.stopPropagation());
        });
      }).catch(() => {});
  }

  async function pollSeriesMaint(seriesID) {
    if (location.pathname !== `/series/${encodeURIComponent(seriesID)}` && location.pathname !== `/series/${seriesID}`) return;
    const seq = routeSeq;
    try {
      const jobs = await api(`/admin/api/maintenance-jobs?targetType=series&targetId=${encodeURIComponent(seriesID)}&limit=6`);
      if (seq !== routeSeq) return;
      const coverButton = document.getElementById('series-generate-cover');
      if (coverButton) coverButton.disabled = (jobs || []).some((j) => j.operation === 'series_thumbnail' && ['queued', 'running'].includes(j.status));
      const box = document.getElementById('series-maint-jobs');
      if (box) {
        box.innerHTML = renderMaintJobRows(jobs || []);
        bindMaintCancelButtons(box, () => pollSeriesMaint(seriesID));
      }
      if ((jobs || []).some((j) => j.status === 'queued' || j.status === 'running')) {
        schedulePoll(() => pollSeriesMaint(seriesID), 2000);
      } else {
        stopPolling();
        state.coverVer = Date.now();
        const panelOpen = document.getElementById('series-maint-panel')?.open;
        const detail = await api(`/admin/api/series/${encodeURIComponent(seriesID)}/detail`);
        if (seq !== routeSeq) return;
        paintSeriesDetail(seriesID, detail, jobs || []);
        if (panelOpen) document.getElementById('series-maint-panel').open = true;
      }
    } catch (_) { if (seq === routeSeq) stopPolling(); }
  }

  function renderMaintJobRows(jobs) {
    if (!jobs || jobs.length === 0) return '';
    return `<div style="margin-top:8px;display:grid;gap:6px">${jobs.map((j) => `
      <div style="display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:8px 10px;padding:7px 10px;border-radius:6px;background:var(--bg-input);border:1px solid var(--border)">
        <span style="min-width:0;overflow-wrap:anywhere;word-break:break-word">
          <span class="badge ${j.status === 'completed' ? 'badge-success' : j.status === 'failed' ? 'badge-danger' : 'badge-accent'}">${esc(j.status)}</span>
          <strong style="margin-left:6px">${esc(j.operation)}</strong>
          <span style="color:var(--text-muted);margin-left:8px">${j.processedBooks || 0}/${j.totalBooks || 0}</span>
          ${j.error ? `<span style="color:var(--danger);margin-left:8px">${esc(j.error)}</span>` : ''}
        </span>
        ${(j.status === 'queued' || j.status === 'running') ? `<button type="button" class="btn btn-sm btn-danger" data-cancel-maint="${esc(j.id)}">取消</button>` : ''}
      </div>`).join('')}</div>`;
  }

  function bindMaintCancelButtons(root, onDone) {
    root.querySelectorAll('button[data-cancel-maint]').forEach((btn) => {
      btn.onclick = async () => {
        try {
          await api(`/admin/api/maintenance-jobs/${encodeURIComponent(btn.getAttribute('data-cancel-maint'))}/cancel`, { method: 'POST' });
          if (onDone) onDone();
        } catch (err) { toast(err.message, 'error'); }
      };
    });
  }

  // --- Bangumi Match Modal ---

  function bgmSignedDelta(value) {
    const n = Number(value || 0);
    return `${n > 0 ? '+' : ''}${n.toFixed(2)}`;
  }

  function bgmScoreBreakdownHTML(candidate) {
    const b = candidate && candidate.breakdown;
    if (!b) return '';
    const parts = [
      `标题 ${Number(b.titleScore || 0).toFixed(2)}${b.exactTitle ? '（完全一致）' : ''}`,
      `平台 ${bgmSignedDelta(b.platformDelta)}`,
      `系列 ${bgmSignedDelta(b.seriesBonus)}`,
      `版本 ${bgmSignedDelta(b.editionDelta)}`,
      `作者 ${bgmSignedDelta(b.authorDelta)}`,
    ];
    if (Number(b.scanlatorPenalty || 0) !== 0) parts.push(`扫图组 ${bgmSignedDelta(b.scanlatorPenalty)}`);
    if (Number(b.shortTitlePenalty || 0) !== 0) parts.push(`短标题 ${bgmSignedDelta(b.shortTitlePenalty)}`);
    parts.push(`人气 ${bgmSignedDelta(b.popularityBonus)}`);
    parts.push(`合计 ${Number(b.final || 0).toFixed(2)}`);
    return `<details style="margin-top:5px;font-size:12px;color:var(--text-muted)"><summary style="cursor:pointer">评分明细</summary><div style="margin-top:3px">${esc(parts.join(' · '))}</div></details>`;
  }

  function openBangumiMatchModal(seriesID, detail) {
    const rawMeta = detail.rawMeta || {};
    openModal({
      title: `Bangumi 元数据匹配 · ${detail.originalName}`,
      subheadHTML: `
        <div style="display:flex;gap:8px;flex-wrap:wrap;margin-bottom:8px">
          <input type="text" id="bgm-search-q" value="${esc(detail.originalName)}" placeholder="输入漫画标题关键词、Bangumi 条目 ID 或 bgm.tv 链接" style="flex:1 1 200px;min-width:0;height:38px;padding:0 11px;border-radius:8px;border:1px solid var(--border-strong);background:var(--bg-input);color:var(--text)">
          <button type="button" class="btn btn-primary" id="bgm-search-btn">搜索候选</button>
          <button type="button" class="btn btn-bgm" id="bgm-auto-btn">自动清洗匹配</button>
          ${rawMeta.bgmSubjectId ? `<button type="button" class="btn btn-danger" id="bgm-clear-btn">解除绑定 (#${rawMeta.bgmSubjectId})</button>` : ''}
        </div>
        <div id="bgm-parsed-hint" class="bgm-parsed-hint">支持直接粘贴 Subject ID（如 24596）或 https://bgm.tv/subject/... 链接（部分受限条目需登录 bgm.tv 才能在网页端打开）。</div>
      `,
      bodyHTML: `
        <div id="bgm-candidates-box"><div class="boot-splash" style="min-height:140px"><div class="spinner"></div><p>正在搜索 Bangumi…</p></div></div>
      `,
      onMount: () => {
        const searchInput = document.getElementById('bgm-search-q');
        const box = document.getElementById('bgm-candidates-box');
        const hint = document.getElementById('bgm-parsed-hint');
        const defaultHint = hint.textContent;
        const openRouteSeq = routeSeq;
        let searchID = 0;
        let mutating = false;
        const autoBtn = document.getElementById('bgm-auto-btn');
        const clearBtn = document.getElementById('bgm-clear-btn');
        const setMutating = (busy) => {
          mutating = busy;
          if (autoBtn) autoBtn.disabled = busy;
          if (clearBtn) clearBtn.disabled = busy;
          box.querySelectorAll('button[data-bind-bgm]').forEach((b) => { b.disabled = busy; });
        };
        const renderCandidates = (cands, noteHTML = '') => {
          if (cands.length === 0) {
            box.innerHTML = noteHTML + `<div class="empty-box">未找到匹配的 Bangumi 书籍条目，请尝试缩短关键词或直接输入 Bangumi Subject ID。</div>`;
            return;
          }
          box.innerHTML = noteHTML + cands.map((c) => {
            const imgURL = `/admin/api/bangumi/subjects/${encodeURIComponent(c.id)}/cover${c.coverUrl ? `?u=${encodeURIComponent(c.coverUrl)}` : ''}`;
            const authorsTxt = (c.authors || []).map((a) => a.name).join(' / ');
            return `
                <div class="bgm-candidate">
                  <img loading="lazy" src="${esc(imgURL)}" alt="">
                  <div style="flex:1;min-width:0">
                    <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap">
                      <strong style="font-size:15px">${esc(c.nameCn || c.name)}</strong>
                      <span class="badge badge-bgm">#${c.id} · ${esc(c.platformLabel || '漫画')}</span>
                      ${c.nsfw ? `<span class="badge badge-danger">NSFW</span>` : ''}
                      ${c.score ? `<span class="badge">★ ${Number(c.score).toFixed(1)}</span>` : ''}
                      ${c.confidence ? `<span class="badge badge-accent">匹配度 ${Math.round(c.confidence * 100)}%</span>` : ''}
                    </div>
                    ${c.nameCn && c.name && c.name !== c.nameCn ? `<div style="font-size:12px;color:var(--text-muted)">原名：${esc(c.name)}</div>` : ''}
                    <div style="font-size:12px;color:var(--text-secondary);margin-top:3px">
                      ${authorsTxt ? `作者：${esc(authorsTxt)} · ` : ''}
                      ${c.publisher ? `出版社：${esc(c.publisher)} · ` : ''}
                      ${c.releaseDate ? `发售：${esc(c.releaseDate)}` : ''}
                    </div>
                    ${c.summary ? `<div style="font-size:12px;color:var(--text-muted);margin-top:5px;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden">${esc(c.summary)}</div>` : ''}
                    ${bgmScoreBreakdownHTML(c)}
                  </div>
                  <div class="bgm-candidate-actions">
                    <button type="button" class="btn btn-primary btn-sm" data-bind-bgm="${c.id}">选用此条目</button>
                    <a href="https://bgm.tv/subject/${c.id}" target="_blank" rel="noopener noreferrer" class="btn btn-sm">查看 ↗</a>
                  </div>
                </div>`;
          }).join('');

          box.querySelectorAll('button[data-bind-bgm]').forEach((btn) => {
            btn.disabled = mutating;
            btn.onclick = async () => {
              if (mutating) return;
              const subjectId = Number(btn.getAttribute('data-bind-bgm'));
              setMutating(true);
              try {
                await api(`/admin/api/series/${encodeURIComponent(seriesID)}/bangumi/bind`, {
                  method: 'POST',
                  body: { subjectId },
                });
                state.coverVer = Date.now();
                state.taxonomies.loaded = false;
                toast(`已绑定 Bangumi #${subjectId}`);
                if (box.isConnected && openRouteSeq === routeSeq) {
                  closeModal();
                  renderSeriesDetail(seriesID);
                }
              } catch (err) {
                toast(err.message, 'error');
                if (box.isConnected) setMutating(false);
              }
            };
          });
        };
        const doSearch = async (qVal) => {
          const id = ++searchID;
          box.innerHTML = `<div class="boot-splash" style="min-height:140px"><div class="spinner"></div><p>正在搜索 Bangumi…</p></div>`;
          try {
            const res = await api(`/admin/api/series/${encodeURIComponent(seriesID)}/bangumi/candidates?q=${encodeURIComponent(qVal)}`);
            if (id !== searchID || !box.isConnected) return;
            const parsed = res.parsed || {};
            hint.textContent = parsed.candidates && parsed.candidates.length > 0
              ? `清洗候选词：${parsed.candidates.join(' / ')}${parsed.authors && parsed.authors.length ? ` · 识别作者：${parsed.authors.join('、')}` : ''}${parsed.publisher ? ` · 版本/出版社：${parsed.publisher}` : ''}`
              : defaultHint;
            hint.title = hint.textContent;
            renderCandidates(res.candidates || []);
          } catch (err) {
            if (id !== searchID || !box.isConnected) return;
            box.innerHTML = `<div class="empty-box" style="color:var(--danger)">${esc(err.message)}</div>`;
          }
        };

        document.getElementById('bgm-search-btn').onclick = () => doSearch(searchInput.value.trim());
        searchInput.addEventListener('keydown', (e) => {
          if (e.key !== 'Enter' || e.isComposing || e.keyCode === 229) return;
          e.preventDefault();
          doSearch(searchInput.value.trim());
        });
        autoBtn.onclick = async () => {
          if (mutating) return;
          const id = ++searchID;
          setMutating(true);
          try {
            const res = await api(`/admin/api/series/${encodeURIComponent(seriesID)}/bangumi/auto`, { method: 'POST', body: {} });
            if (res.matched) {
              state.coverVer = Date.now();
              state.taxonomies.loaded = false;
              toast('自动匹配成功！');
              if (box.isConnected && openRouteSeq === routeSeq) {
                closeModal();
                renderSeriesDetail(seriesID);
              }
              return;
            }
            if (id !== searchID || !box.isConnected) return;
            const cands = res.candidates || [];
            const threshold = Number(res.threshold || 0.72);
            if (cands.length > 0) {
              const best = Math.round(Number(cands[0].confidence || 0) * 100);
              const note = `<div class="empty-box" style="margin-bottom:10px;color:var(--text-secondary)">自动匹配未通过：最高 ${best}% 低于阈值 ${Math.round(threshold * 100)}%。下面是评分最高的 ${cands.length} 个候选，展开“评分明细”可以看到每一项加减分。</div>`;
              renderCandidates(cands, note);
              toast('自动匹配置信度不足，已列出评分最高的候选', 'error');
            } else {
              box.innerHTML = `<div class="empty-box" style="color:var(--danger)">自动匹配没有找到任何候选：可能是标题清洗后为空，或 Bangumi 本地索引与在线搜索都没有命中。</div>`;
              toast('自动匹配没有找到候选', 'error');
            }
          } catch (err) {
            if (id === searchID) toast(err.message, 'error');
          } finally {
            if (box.isConnected) setMutating(false);
          }
        };
        if (clearBtn) {
          clearBtn.onclick = async () => {
            if (mutating) return;
            const confirmed = await confirmModal('确认解除 Bangumi 绑定并恢复原始目录标题与首卷封面？', {
              title: '解除 Bangumi 绑定',
              confirmText: '确认解除',
            });
            if (!confirmed || mutating || !box.isConnected || openRouteSeq !== routeSeq) return;
            setMutating(true);
            try {
              await api(`/admin/api/series/${encodeURIComponent(seriesID)}/bangumi/bind`, { method: 'DELETE' });
              state.coverVer = Date.now();
              toast('已解除 Bangumi 绑定');
              if (box.isConnected && openRouteSeq === routeSeq) {
                closeModal();
                renderSeriesDetail(seriesID);
              }
            } catch (err) {
              toast(err.message, 'error');
              if (box.isConnected) setMutating(false);
            }
          };
        }
        doSearch('');
      },
    });
  }

  // --- Metadata Edit Modal ---

  function openMetadataEditModal(seriesID, detail) {
    const m = detail.rawMeta || {};
    const genresStr = (m.genres || []).join(', ');
    const tagsStr = (m.tags || []).join(', ');
    openModal({
      title: '编辑系列元数据（手动保存后自动锁定对应字段）',
      bodyHTML: `
        <div class="grid-2">
          <div class="field">
            <div class="field-label-row"><label for="em-title">显示标题</label><label class="check-row"><input type="checkbox" id="em-title-lock" ${m.titleLock ? 'checked' : ''}>锁定</label></div>
            <input type="text" id="em-title" value="${esc(m.title || detail.originalName)}">
          </div>
          <div class="field">
            <div class="field-label-row"><label for="em-sort">排序标题</label><label class="check-row"><input type="checkbox" id="em-sort-lock" ${m.titleSortLock ? 'checked' : ''}>锁定</label></div>
            <input type="text" id="em-sort" value="${esc(m.titleSort || m.title || detail.originalName)}">
          </div>
          <div class="field">
            <div class="field-label-row"><label for="em-status">连载状态</label><label class="check-row"><input type="checkbox" id="em-status-lock" ${m.statusLock ? 'checked' : ''}>锁定</label></div>
            <select id="em-status">
              <option value="ONGOING" ${m.status === 'ONGOING' ? 'selected' : ''}>ONGOING（连载中）</option>
              <option value="ENDED" ${m.status === 'ENDED' ? 'selected' : ''}>ENDED（已完结）</option>
              <option value="HIATUS" ${m.status === 'HIATUS' ? 'selected' : ''}>HIATUS（休刊）</option>
              <option value="ABANDONED" ${m.status === 'ABANDONED' ? 'selected' : ''}>ABANDONED（中止）</option>
            </select>
          </div>
          <div class="field">
            <div class="field-label-row"><label for="em-dir">阅读方向</label><label class="check-row"><input type="checkbox" id="em-dir-lock" ${m.readingDirectionLock ? 'checked' : ''}>锁定</label></div>
            <select id="em-dir">
              <option value="RIGHT_TO_LEFT" ${m.readingDirection === 'RIGHT_TO_LEFT' ? 'selected' : ''}>右翻日漫 (RIGHT_TO_LEFT)</option>
              <option value="LEFT_TO_RIGHT" ${m.readingDirection === 'LEFT_TO_RIGHT' ? 'selected' : ''}>左翻国漫/美漫 (LEFT_TO_RIGHT)</option>
              <option value="VERTICAL" ${m.readingDirection === 'VERTICAL' ? 'selected' : ''}>上下条漫 (VERTICAL)</option>
            </select>
          </div>
          <div class="field">
            <div class="field-label-row"><label for="em-pub">出版社</label><label class="check-row"><input type="checkbox" id="em-pub-lock" ${m.publisherLock ? 'checked' : ''}>锁定</label></div>
            <input type="text" id="em-pub" value="${esc(m.publisher || '')}">
          </div>
          <div class="field">
            <div class="field-label-row"><label for="em-date">发售日期</label><label class="check-row"><input type="checkbox" id="em-date-lock" ${m.releaseDateLock ? 'checked' : ''}>锁定</label></div>
            <input type="text" id="em-date" placeholder="YYYY-MM-DD" value="${esc(m.releaseDate || '')}">
          </div>
          <div class="field">
            <div class="field-label-row"><label for="em-nsfw">NSFW 分级标记</label><label class="check-row"><input type="checkbox" id="em-nsfw-lock" ${m.nsfwLock ? 'checked' : ''}>锁定</label></div>
            <select id="em-nsfw">
              <option value="false" ${!m.isNsfw ? 'selected' : ''}>普通作品（非 NSFW）</option>
              <option value="true" ${m.isNsfw ? 'selected' : ''}>NSFW（受后台显示开关控制）</option>
            </select>
          </div>
        </div>
        <div class="field">
          <div class="field-label-row"><label for="em-genres">分类 Genres（英文逗号分隔）</label><label class="check-row"><input type="checkbox" id="em-genres-lock" ${m.genresLock ? 'checked' : ''}>锁定</label></div>
          <input type="text" id="em-genres" value="${esc(genresStr)}">
        </div>
        <div class="field">
          <div class="field-label-row"><label for="em-tags">标签 Tags（英文逗号分隔）</label><label class="check-row"><input type="checkbox" id="em-tags-lock" ${m.tagsLock ? 'checked' : ''}>锁定</label></div>
          <input type="text" id="em-tags" value="${esc(tagsStr)}">
        </div>
        <div class="field">
          <div class="field-label-row"><label for="em-sum">简介 Summary</label><label class="check-row"><input type="checkbox" id="em-sum-lock" ${m.summaryLock ? 'checked' : ''}>锁定</label></div>
          <textarea id="em-sum">${esc(m.summary || '')}</textarea>
        </div>
      `,
      footHTML: `
        <button type="button" class="btn" id="em-unlock-all">全部解除锁定</button>
        <button type="button" class="btn btn-primary" id="em-save">保存元数据</button>
      `,
      onMount: () => {
        document.getElementById('em-unlock-all').onclick = () => {
          ['em-title-lock', 'em-sort-lock', 'em-status-lock', 'em-dir-lock', 'em-pub-lock', 'em-date-lock', 'em-nsfw-lock', 'em-genres-lock', 'em-tags-lock', 'em-sum-lock']
            .forEach((id) => { document.getElementById(id).checked = false; });
        };
        document.getElementById('em-save').onclick = async () => {
          const splitCSV = (s) => s.split(',').map((x) => x.trim()).filter(Boolean);
          const patch = {
            title: document.getElementById('em-title').value.trim(),
            titleLock: document.getElementById('em-title-lock').checked,
            titleSort: document.getElementById('em-sort').value.trim(),
            titleSortLock: document.getElementById('em-sort-lock').checked,
            status: document.getElementById('em-status').value,
            statusLock: document.getElementById('em-status-lock').checked,
            readingDirection: document.getElementById('em-dir').value,
            readingDirectionLock: document.getElementById('em-dir-lock').checked,
            publisher: document.getElementById('em-pub').value.trim(),
            publisherLock: document.getElementById('em-pub-lock').checked,
            releaseDate: document.getElementById('em-date').value.trim(),
            releaseDateLock: document.getElementById('em-date-lock').checked,
            isNsfw: document.getElementById('em-nsfw').value === 'true',
            nsfwLock: document.getElementById('em-nsfw-lock').checked,
            genres: splitCSV(document.getElementById('em-genres').value),
            genresLock: document.getElementById('em-genres-lock').checked,
            tags: splitCSV(document.getElementById('em-tags').value),
            tagsLock: document.getElementById('em-tags-lock').checked,
            summary: document.getElementById('em-sum').value.trim(),
            summaryLock: document.getElementById('em-sum-lock').checked,
          };
          try {
            await api(`/api/v1/series/${encodeURIComponent(seriesID)}/metadata`, {
              method: 'PATCH',
              body: patch,
            });
            state.taxonomies.loaded = false;
            toast('元数据已保存');
            closeModal();
            renderSeriesDetail(seriesID);
          } catch (err) { toast(err.message, 'error'); }
        };
      },
    });
  }

  // --- Book Detail View (/book/:id & /books/:id) ---

  async function renderBookDetail(bookID) {
    const seq = routeSeq;
    const uid = state.auth.userId || '';
    renderShell('libraries', `<div class="boot-splash" style="min-height:40vh"><div class="spinner"></div><p>正在读取单册详情…</p></div>`);
    try {
      const [detail, jobs] = await Promise.all([
        api(`/admin/api/books/${encodeURIComponent(bookID)}/detail`),
        isAdminUser()
          ? api(`/admin/api/maintenance-jobs?targetType=book&targetId=${encodeURIComponent(bookID)}&limit=6`).catch(() => [])
          : Promise.resolve([]),
      ]);
      if (seq !== routeSeq || (state.auth.userId || '') !== uid) return;
      paintBookDetail(bookID, detail, jobs || []);
    } catch (err) {
      if (seq !== routeSeq) return;
      toast(err.message, 'error');
    }
  }

  function paintBookDetail(bookID, detail, jobs) {
    const b = detail.book || {};
    const rp = b.readProgress;
    const pgCount = (b.media && b.media.pagesCount) || 0;
    const startPg = rp && !rp.completed && rp.page > 0 ? rp.page : 1;
    const admin = isAdminUser();
    document.title = `${b.name || '单册详情'} · MangaAlpha`;

    const content = document.getElementById('page-content');
    if (!content) return;

    content.innerHTML = `
      <div style="margin-bottom:12px;font-size:13px;color:var(--text-muted)">
        <a href="/series/${encodeURIComponent(detail.seriesId)}" data-nav style="color:var(--accent)">← 返回系列：${esc(detail.seriesTitle || detail.originalSeries)}</a>
      </div>

      <section class="card">
        <div class="detail-badges">
          <span class="badge">${esc(detail.libraryName)}</span>
          <span class="badge">${esc(providerLabel(detail.providerType, detail.webdavAccountName))}</span>
          <span class="badge badge-accent">${esc(detail.archiveType)}</span>
          ${rp && rp.completed
            ? `<span class="badge badge-success">已读完</span>`
            : rp && rp.page > 0
              ? `<span class="badge badge-accent">阅读中 · 第 ${rp.page}${pgCount > 0 ? ` / ${pgCount}` : ''} 页</span>`
              : `<span class="badge">未读</span>`}
        </div>
        <h1 style="margin:6px 0 8px;font-size:22px">${esc(b.name)}</h1>
        <div class="detail-orig">
          <div>所属书库：<strong>${esc(detail.libraryName)}</strong>（${esc(providerLabel(detail.providerType, detail.webdavAccountName))}）</div>
          <div>相对路径：${esc(detail.relativePath)}</div>
        </div>
        ${Array.isArray(detail.unionSources) && detail.unionSources.length > 0 ? `
          <div class="union-source-box">
            <div style="font-weight:600">联合库多副本来源（读取失败时按优先级自动回退）</div>
            ${detail.unionSources.map((src, idx) => `
              <div style="display:flex;flex-wrap:wrap;align-items:center;gap:8px">
                <span class="badge ${idx === 0 ? 'badge-success' : ''}">${idx === 0 ? '生效层 #1' : `备用层 #${idx + 1}`}</span>
                <strong>${esc(src.sourceLibraryName)}</strong>
                <span style="color:var(--text-muted)">${esc(providerLabel(src.providerType, src.webdavAccountName))} · ${fmtBytes(src.sourceSize)}</span>
              </div>`).join('')}
          </div>` : ''}

        <div class="detail-cta" style="margin-top:16px">
          <a href="/reader/series/${encodeURIComponent(detail.seriesId)}/book/${encodeURIComponent(bookID)}?page=${startPg}" class="btn btn-primary btn-lg">
            ▶ ${rp && !rp.completed && rp.page > 0 ? `继续阅读（第 ${rp.page} 页）` : '立即在线阅读'}
          </a>
          ${detail.previousBookId ? `<a href="/book/${encodeURIComponent(detail.previousBookId)}" data-nav class="btn">← 上一卷</a>` : ''}
          ${detail.nextBookId ? `<a href="/book/${encodeURIComponent(detail.nextBookId)}" data-nav class="btn">下一卷 →</a>` : ''}
          <button type="button" class="btn btn-sm" id="bk-add-readlist">+ 阅读列表</button>
          <button type="button" class="btn btn-sm" id="bk-mark-read">标记已读</button>
          <button type="button" class="btn btn-sm" id="bk-mark-unread">重置未读</button>
        </div>
        <div id="book-readlists-bar" class="chip-group" style="display:none;margin-top:12px"></div>

        <div class="stat-grid" style="margin-top:18px">
          <div class="stat-box"><div class="k">文件大小</div><div class="v" style="font-size:17px">${esc(b.size)}</div></div>
          <div class="stat-box"><div class="k">页数 / 索引状态</div><div class="v" style="font-size:15px">${esc(detail.indexStatus)}${detail.indexDuration ? ` (${esc(detail.indexDuration)})` : ''}</div></div>
          <div class="stat-box"><div class="k">真实下载速度</div><div class="v" style="font-size:15px">${esc(detail.downloadSpeed)}${detail.downloadBytes ? ` · ${esc(detail.downloadBytes)}` : ''}</div></div>
          ${detail.inferred ? `<div class="stat-box"><div class="k">推断加载进度</div><div class="v" style="font-size:14px">最近 ${esc(detail.inferred.last)}<br>最大 ${esc(detail.inferred.max)}</div></div>` : ''}
        </div>

        ${admin ? `
        <div style="display:flex;gap:8px;flex-wrap:wrap;margin-top:10px">
          <button type="button" class="btn btn-sm" id="bk-idx-build">生成归档索引</button>
          <button type="button" class="btn btn-sm" id="bk-idx-force">强制重建归档索引</button>
        </div>
        <div id="book-maint-jobs">${renderMaintJobRows(jobs)}</div>` : ''}
      </section>
    `;

    document.getElementById('bk-add-readlist').onclick = () => {
      openAddBookToReadListModal(bookID, b.name || '单册', () => loadBookReadListsBar(bookID));
    };
    loadBookReadListsBar(bookID);
    document.getElementById('bk-mark-read').onclick = async () => {
      try {
        await api(`/api/v1/books/${encodeURIComponent(bookID)}/read-progress`, { method: 'PATCH', body: { completed: true } });
        renderBookDetail(bookID);
      } catch (err) { toast(err.message, 'error'); }
    };
    document.getElementById('bk-mark-unread').onclick = async () => {
      try {
        await api(`/api/v1/books/${encodeURIComponent(bookID)}/read-progress`, { method: 'DELETE' });
        renderBookDetail(bookID);
      } catch (err) { toast(err.message, 'error'); }
    };
    if (admin) {
      const startBookIdx = async (force) => {
        try {
          await api('/admin/api/maintenance-jobs', { method: 'POST', body: { operation: 'book_index', bookId: bookID, force } });
          toast('归档索引任务已提交');
          pollBookMaint(bookID);
        } catch (err) { toast(err.message, 'error'); }
      };
      const btnBuild = document.getElementById('bk-idx-build');
      const btnForce = document.getElementById('bk-idx-force');
      if (btnBuild) btnBuild.onclick = () => startBookIdx(false);
      if (btnForce) btnForce.onclick = () => startBookIdx(true);
      bindMaintCancelButtons(content, () => pollBookMaint(bookID));
      if (jobs.some((j) => j.status === 'queued' || j.status === 'running')) {
        schedulePoll(() => pollBookMaint(bookID), 1800);
      }
    }
  }

  async function pollBookMaint(bookID) {
    if (!location.pathname.endsWith(`/${encodeURIComponent(bookID)}`) && !location.pathname.endsWith(`/${bookID}`)) return;
    try {
      const jobs = await api(`/admin/api/maintenance-jobs?targetType=book&targetId=${encodeURIComponent(bookID)}&limit=6`);
      const box = document.getElementById('book-maint-jobs');
      if (box) {
        box.innerHTML = renderMaintJobRows(jobs || []);
        bindMaintCancelButtons(box, () => pollBookMaint(bookID));
      }
      if ((jobs || []).some((j) => j.status === 'queued' || j.status === 'running')) {
        schedulePoll(() => pollBookMaint(bookID), 1800);
      } else {
        stopPolling();
        const detail = await api(`/admin/api/books/${encodeURIComponent(bookID)}/detail`);
        paintBookDetail(bookID, detail, jobs || []);
      }
    } catch (_) { stopPolling(); }
  }

  // --- Admin Console (/admin) ---

  async function renderAdmin() {
    if (!isAdminUser()) {
      navigate('/account', true);
      return;
    }
    const sp = new URLSearchParams(location.search);
    const requestedTab = sp.get('tab') || state.adminTab || 'overview';
    if (requestedTab === 'security') {
      const pk = (sp.get('presetKey') || '').trim();
      navigate(pk ? `/account?presetKey=${encodeURIComponent(pk)}` : '/account', true);
      return;
    }
    const tab = requestedTab;
    state.adminTab = tab;
    document.title = '管理后台 · MangaAlpha';

    const tabs = [
      ['overview', '概览与连接'],
      ['sources', '存储源'],
      ['libraries', '书库管理'],
      ['bangumi', 'Bangumi 刮削中心'],
      ['jobs', '后台任务'],
      ['settings', '缓存与设置'],
      ['users', '用户管理'],
    ];

    renderShell('admin', `
      <div id="admin-update-banner"></div>
      <div class="admin-tabs">
        ${tabs.map(([id, label]) => `<button type="button" data-admin-tab="${id}" class="${tab === id ? 'active' : ''}">${esc(label)}</button>`).join('')}
      </div>
      <div id="admin-tab-body"><div class="boot-splash" style="min-height:35vh"><div class="spinner"></div></div></div>
    `);

    refreshAdminUpdateBanner();

    document.querySelectorAll('button[data-admin-tab]').forEach((btn) => {
      if (btn.classList.contains('active') && btn.scrollIntoView) {
        requestAnimationFrame(() => {
          try { btn.scrollIntoView({ inline: 'center', block: 'nearest' }); } catch (_) {}
        });
      }
      btn.onclick = () => {
        const next = btn.getAttribute('data-admin-tab');
        state.adminTab = next;
        navigate(`/admin?tab=${encodeURIComponent(next)}`, true);
      };
    });

    const body = document.getElementById('admin-tab-body');
    if (!body) return;

    switch (tab) {
      case 'sources': await renderAdminSources(body); break;
      case 'libraries': await renderAdminLibraries(body); break;
      case 'bangumi': await renderAdminBangumi(body); break;
      case 'jobs': await renderAdminJobs(body); break;
      case 'settings': await renderAdminSettings(body); break;
      case 'users': await renderAdminUsers(body); break;
      default: await renderAdminOverview(body); break;
    }
  }

  function paintAdminUpdateBanner(st) {
    const slot = document.getElementById('admin-update-banner');
    if (!slot) return;
    if (!st || !st.hasUpdate || st.dismissed || !st.latestVersion) {
      slot.innerHTML = '';
      return;
    }
    const isAndroid = navigator.userAgent.includes('FK115Android/');
    slot.innerHTML = `
      <div class="update-banner">
        <div class="update-banner-main">
          <span class="badge badge-accent">新版本</span>
          <span>发现 MangaAlpha <strong>${esc(st.latestVersion)}</strong>（当前 ${esc(st.currentVersion || '-')}）</span>
          ${st.inDocker && st.dockerCommand ? `<code>${esc(st.dockerCommand)}</code>` : ''}
        </div>
        <div class="update-banner-actions">
          ${isAndroid
            ? `<a class="btn btn-sm btn-primary" href="/__android/settings">手机端下载并安装</a>`
            : `<button type="button" class="btn btn-sm btn-primary" id="upd-banner-detail">查看更新与下载</button>`}
          <button type="button" class="btn btn-sm" id="upd-banner-dismiss">忽略此版本</button>
        </div>
      </div>
    `;
    const detailBtn = document.getElementById('upd-banner-detail');
    if (detailBtn) {
      detailBtn.onclick = () => {
        state.adminTab = 'settings';
        navigate('/admin?tab=settings', true);
      };
    }
    const dismissBtn = document.getElementById('upd-banner-dismiss');
    if (dismissBtn) {
      dismissBtn.onclick = async () => {
        try {
          state.updateStatus = await api('/admin/api/update/config', {
            method: 'PUT',
            body: { dismissVersion: st.latestVersion },
          });
          paintAdminUpdateBanner(state.updateStatus);
          toast(`已忽略 ${st.latestVersion} 的更新提示`);
        } catch (err) { toast(err.message, 'error'); }
      };
    }
  }

  async function refreshAdminUpdateBanner() {
    try {
      const st = await api('/admin/api/update');
      state.updateStatus = st;
      paintAdminUpdateBanner(st);
    } catch (_) {}
  }

  async function renderAdminOverview(body) {
    const st = await api('/admin/api/status');
    const perf = st.performance || {};
    const addrs = st.mihonAddresses || [];
    body.innerHTML = `
      <div class="stat-grid">
        <div class="stat-box"><div class="k">有效书库数</div><div class="v">${st.libraries}${st.hiddenLibraries > 0 ? `<span class="stat-note">已隐藏 ${st.hiddenLibraries}</span>` : ''}</div></div>
        <div class="stat-box"><div class="k">漫画系列总数</div><div class="v">${Number(st.series || 0).toLocaleString()}${(!st.showNsfw && st.nsfwSeries > 0) ? `<span class="stat-note">已隐藏 NSFW ${Number(st.nsfwSeries).toLocaleString()} 部</span>` : ''}</div></div>
        <div class="stat-box"><div class="k">漫画文件总数</div><div class="v">${Number(st.books || 0).toLocaleString()}</div></div>
        <div class="stat-box"><div class="k">漫画总容量</div><div class="v">${fmtBytes(st.comicBytes)}</div></div>
        <div class="stat-box"><div class="k">Bangumi 已匹配</div><div class="v" style="color:var(--bgm)">${Number(st.bgmMatched || 0).toLocaleString()}</div></div>
        <div class="stat-box"><div class="k">115 / WebDAV 状态</div><div class="v" style="font-size:15px">${st.configured115 ? '115 已连接' : '115 未配'} · WebDAV ${st.webdavAccounts || 0} 个</div></div>
      </div>

      <div class="grid-2">
        <div class="card">
          <h3>客户端接入地址（Koharia / Mihon / KOReader）</h3>
          <p style="color:var(--text-secondary);font-size:13px">使用账号密码（HTTP Basic）或在右上角进入 <a href="/account" data-nav style="color:var(--accent)">「账号设置」</a> 生成专属 API Key 登录（KOReader 仅需填用户名密码）：</p>
          <div style="display:grid;gap:8px;margin-top:10px">
            <div style="font-size:12.5px;color:var(--text-secondary)">
              <strong>Komga 客户端</strong>（推荐 <a href="https://github.com/Mister-album/Koharia" target="_blank" rel="noopener noreferrer" style="color:var(--accent)">Koharia ↗</a> Android 阅读器，或 <a href="https://mihon.app/" target="_blank" rel="noopener noreferrer" style="color:var(--accent)">Mihon ↗</a> Komga 扩展）
            </div>
            ${(addrs.length ? addrs : [st.adminUrl || location.origin]).map((u) => `<div class="code-box">${esc(u)}</div>`).join('')}
            <div style="font-size:12.5px;color:var(--text-secondary);margin-top:4px">
              <strong>KOReader 墨水屏</strong>（推荐插件 <a href="https://github.com/hugo1120/opdsforcomic.koplugin/releases" target="_blank" rel="noopener noreferrer" style="color:var(--accent)"><code>opdsforcomic.koplugin</code> ↗</a> · 点击直读 / 章节导航 / 零提前建索引 / 双向同步进度）
            </div>
            ${(addrs.length ? addrs : [st.adminUrl || location.origin]).map((u) => `<div class="code-box">${esc(u.replace(/\/+$/, ''))}/api/opds/v1.2</div>`).join('')}
            <div style="font-size:12.5px;color:var(--text-secondary);margin-top:4px">
              <strong>标准 Komga OPDS v1.2</strong>（兼容其他标准 OPDS 阅读客户端）
            </div>
            ${(addrs.length ? addrs : [st.adminUrl || location.origin]).map((u) => `<div class="code-box">${esc(u.replace(/\/+$/, ''))}/opds/v1.2/catalog</div>`).join('')}
          </div>
          <div style="margin-top:12px;font-size:12.5px;color:var(--text-muted)">
            版本：<code>${esc(st.version)}</code> · 数据目录：<code>${esc(st.dataDir)}</code>
          </div>
        </div>

        <div class="card">
          <h3>全局索引与流式读取统计</h3>
          <div style="display:grid;gap:8px;font-size:13px;color:var(--text-secondary)">
            <div>已建立归档索引单册：<strong>${Number(perf.indexCount ?? perf.indexedBooksCount ?? 0).toLocaleString()}</strong></div>
            <div>累计真实 Range 读取流量：<strong>${fmtBytes(perf.downloadBytes || 0)}</strong>（${Number(perf.downloadSamples || 0).toLocaleString()} 次请求）</div>
          </div>
        </div>
      </div>
    `;
  }

  let sourcesRenderSeq = 0;

  async function renderAdminSources(body) {
    if (!body || !body.isConnected) return;
    const reqSeq = ++sourcesRenderSeq;
    const renderRouteSeq = routeSeq;
    const [acc115, webdavs] = await Promise.all([
      api('/admin/api/account'),
      api('/admin/api/webdav-accounts'),
    ]);
    if (reqSeq !== sourcesRenderSeq || renderRouteSeq !== routeSeq || !body.isConnected) return;
    body.innerHTML = `
      <div class="grid-2">
        <div class="card">
          <h3>115 开放平台令牌</h3>
          <p style="font-size:12.5px;color:var(--text-muted)">状态：RefreshToken ${acc115.hasRefreshToken ? '✅ 已保存' : '❌ 未配置'} · AccessToken ${acc115.hasAccessToken ? '✅ 已保存' : '未缓存'} · 更新于 ${fmtTime(acc115.updatedAt)}</p>
          <details class="field-hint"${acc115.hasRefreshToken ? '' : ' open'}>
            <summary>如何获取 115 Token？</summary>
            <ol>
              <li>打开 <a href="https://api.oplist.org/" target="_blank" rel="noopener noreferrer">OpenList 授权工具 ↗</a>，选择 115 网盘的 OAuth2 授权登录。</li>
              <li>勾选「使用 OpenList 提供的参数」，点击获取 Token，再在弹出的窗口登录 115 并确认授权。</li>
              <li>将 <strong>Refresh Token</strong> 和 <strong>Access Token</strong>（可选）粘贴到下方同名字段，点击「保存并验证」。请勿分享或公开截图中的 Token。</li>
            </ol>
          </details>
          <div class="field">
            <label>Refresh Token（留空则保留现有）</label>
            <input type="password" id="s115-refresh" placeholder="输入新的 115 Refresh Token">
          </div>
          <div class="field">
            <label>Access Token（可选，留空自动刷新）</label>
            <input type="password" id="s115-access" placeholder="可选 Access Token">
          </div>
          <div style="display:flex;gap:8px;flex-wrap:wrap">
            <button type="button" class="btn btn-primary" id="s115-save">保存并验证</button>
            <button type="button" class="btn" id="s115-test">测试当前连接</button>
            <button type="button" class="btn" id="s115-force-refresh">强制刷新令牌</button>
          </div>
        </div>

        <div class="card">
          <h3>本地磁盘与 SMB 挂载目录说明</h3>
          <p style="color:var(--text-secondary);font-size:13px">
            本地目录存储源无需预配账号，支持服务器本地磁盘、Linux 挂载的 SMB/CIFS 共享（如 <code>/share/manga</code>、<code>/mnt/smb</code>）以及 Windows 映射网络驱动器（如 <code>Z:\</code> 或 <code>\\nas\manga</code>）。
          </p>
          <ul style="color:var(--text-muted);font-size:12.5px;padding-left:18px;line-height:1.7">
            <li>通过内核 <code>io.ReaderAt</code> 原生按偏移量随机读取，自动跳过分块磁盘缓存，零双份空间浪费，并针对 SMB 目录遍历去除了多余元数据往返。</li>
            <li>内置<strong>空挂载点防误删保护</strong>：当已有漫画数据的本地/SMB 根目录因断连变为空目录时，自动中止扫描并保留全部历史系列、元数据与阅读进度。</li>
            <li>Docker 部署时可将宿主机目录或 SMB 挂载点通过 <code>-v /share/manga:/manga:ro</code> 只读映射进容器。</li>
          </ul>
        </div>
      </div>

      <div class="card">
        <div class="section-head">
          <h3>WebDAV 账号列表（支持 OpenList / CloudDrive2 / 群晖 / Nextcloud 等）</h3>
          <button type="button" class="btn btn-primary btn-sm" id="wd-add-btn">+ 新增 WebDAV 账号</button>
        </div>
        ${(webdavs || []).length === 0
          ? `<div class="empty-box">暂未添加 WebDAV 账号。</div>`
          : `<div class="table-wrap"><table class="data-table">
              <thead><tr><th>名称</th><th>服务器地址</th><th>用户名</th><th>限速 (req/s)</th><th>TLS</th><th>操作</th></tr></thead>
              <tbody>
                ${webdavs.map((w) => `
                  <tr>
                    <td><strong>${esc(w.name)}</strong></td>
                    <td class="cell-wrap"><code>${esc(w.baseUrl)}</code></td>
                    <td>${esc(w.username || '—')}</td>
                    <td>${w.ratePerSecond || 8}</td>
                    <td>${w.insecureTls ? '<span class="badge badge-warn">跳过证书校验</span>' : '标准验证'}</td>
                    <td><div class="table-actions">
                      <button type="button" class="btn btn-sm" data-wd-test="${esc(w.id)}">测试</button>
                      <button type="button" class="btn btn-sm" data-wd-edit='${esc(JSON.stringify(w))}'>编辑</button>
                      <button type="button" class="btn btn-sm btn-danger" data-wd-del="${esc(w.id)}">删除</button>
                    </div></td>
                  </tr>`).join('')}
              </tbody>
            </table></div>`}
      </div>
    `;

    document.getElementById('s115-save').onclick = async () => {
      try {
        await api('/admin/api/account', {
          method: 'PUT',
          body: {
            refreshToken: document.getElementById('s115-refresh').value.trim(),
            accessToken: document.getElementById('s115-access').value.trim(),
          },
        });
        toast('115 账号保存并验证成功');
        renderAdminSources(body);
      } catch (err) { toast(err.message, 'error'); }
    };
    document.getElementById('s115-test').onclick = async () => {
      try { await api('/admin/api/account/test', { method: 'POST' }); toast('115 连接正常'); }
      catch (err) { toast(err.message, 'error'); }
    };
    document.getElementById('s115-force-refresh').onclick = async () => {
      try { await api('/admin/api/account/refresh', { method: 'POST' }); toast('115 令牌刷新成功'); renderAdminSources(body); }
      catch (err) { toast(err.message, 'error'); }
    };

    document.getElementById('wd-add-btn').onclick = () => openWebDAVModal(null, () => renderAdminSources(body));
    body.querySelectorAll('button[data-wd-test]').forEach((btn) => {
      btn.onclick = async () => {
        try {
          await api('/admin/api/webdav-accounts/test', { method: 'POST', body: { id: btn.getAttribute('data-wd-test') } });
          toast('WebDAV 连接测试通过');
        } catch (err) { toast(err.message, 'error'); }
      };
    });
    body.querySelectorAll('button[data-wd-edit]').forEach((btn) => {
      btn.onclick = () => openWebDAVModal(JSON.parse(btn.getAttribute('data-wd-edit')), () => renderAdminSources(body));
    });
    body.querySelectorAll('button[data-wd-del]').forEach((btn) => {
      btn.onclick = async () => {
        if (btn.disabled) return;
        const confirmed = await confirmModal('确认删除该 WebDAV 账号？（若仍有书库使用该账号将拒绝删除）', {
          title: '删除 WebDAV 账号',
          confirmText: '确认删除',
        });
        if (!confirmed || !btn.isConnected || btn.disabled) return;
        const origText = btn.textContent;
        btn.disabled = true;
        btn.textContent = '删除中…';
        try {
          await api(`/admin/api/webdav-accounts/${encodeURIComponent(btn.getAttribute('data-wd-del'))}`, { method: 'DELETE' });
          toast('已删除 WebDAV 账号');
        } catch (err) {
          btn.disabled = false;
          btn.textContent = origText;
          toast(err.message, 'error');
          return;
        }
        if (!body.isConnected) return;
        try {
          await renderAdminSources(body);
        } catch (err) {
          toast(`账号已删除，但刷新列表失败：${err.message}`, 'error');
        }
      };
    });
  }

  function openWebDAVModal(existing, onSaved) {
    const isEdit = Boolean(existing && existing.id);
    openModal({
      title: isEdit ? `编辑 WebDAV 账号 · ${existing.name}` : '新增 WebDAV 账号',
      maxWidth: '540px',
      bodyHTML: `
        <div class="field">
          <label>账号备注名称</label>
          <input type="text" id="wd-name" placeholder="例如：CloudDrive2 / OpenList / 群晖 NAS" value="${esc(existing ? existing.name : '')}">
        </div>
        <div class="field">
          <label>WebDAV 根地址 (Base URL)</label>
          <input type="url" id="wd-url" placeholder="http://127.0.0.1:19798/dav 或 https://nas.example.com:5006/dav" value="${esc(existing ? existing.baseUrl : '')}">
          <div class="field-hint">CloudDrive2 桌面/NAS 默认 <code>http://&lt;IP&gt;:19798/dav</code>；若 CloudDrive2 App 与 MangaAlpha 在同一台手机上，可用 <code>http://127.0.0.1:29798/dav</code>。OpenList 默认通常为 <code>http://&lt;IP&gt;:5244/dav</code>。</div>
        </div>
        <div class="grid-2">
          <div class="field">
            <label>用户名</label>
            <input type="text" id="wd-user" value="${esc(existing ? existing.username : '')}">
          </div>
          <div class="field">
            <label>密码${isEdit ? '（留空保持不变）' : ''}</label>
            <input type="password" id="wd-pass" placeholder="${isEdit ? '留空不修改' : '登录密码'}">
          </div>
        </div>
        <div class="field">
          <label>请求限速（每秒最大请求数，同时限制目录扫描与 Range 读取，默认 8）</label>
          <input type="number" step="0.5" min="0.5" max="100" id="wd-rate" value="${existing && existing.ratePerSecond ? existing.ratePerSecond : 8}">
          <div class="field-hint">若后端通过 CloudDrive2 / OpenList 挂载 115 等网盘，建议设为 <code>2 ~ 4</code> 以避免触发上游风控。</div>
        </div>
        <div class="field">
          <label class="check-row">
            <input type="checkbox" id="wd-insecure" ${existing && existing.insecureTls ? 'checked' : ''}>
            <span>允许局域网自签名 HTTPS 证书（跳过 TLS 校验）</span>
          </label>
        </div>
      `,
      footHTML: `
        <button type="button" class="btn" id="wd-modal-test">测试连接</button>
        <button type="button" class="btn btn-primary" id="wd-modal-save">保存</button>
      `,
      onMount: () => {
        const readConnection = () => ({
          baseUrl: document.getElementById('wd-url').value.trim(),
          username: document.getElementById('wd-user').value.trim(),
          password: document.getElementById('wd-pass').value,
          ratePerSecond: parseFloat(document.getElementById('wd-rate').value) || 8,
          insecureTls: document.getElementById('wd-insecure').checked,
        });
        document.getElementById('wd-modal-test').onclick = async () => {
          try {
            const payload = readConnection();
            if (isEdit) payload.id = existing.id;
            await api('/admin/api/webdav-accounts/test', { method: 'POST', body: payload });
            toast('WebDAV 连接验证成功！');
          } catch (err) { toast(err.message, 'error'); }
        };
        document.getElementById('wd-modal-save').onclick = async () => {
          const payload = { ...readConnection(), name: document.getElementById('wd-name').value.trim() };
          try {
            if (isEdit) {
              await api(`/admin/api/webdav-accounts/${encodeURIComponent(existing.id)}`, { method: 'PUT', body: payload });
            } else {
              await api('/admin/api/webdav-accounts', { method: 'POST', body: payload });
            }
            toast('WebDAV 账号已保存');
            closeModal();
            if (onSaved) onSaved();
          } catch (err) { toast(err.message, 'error'); }
        };
      },
    });
  }

  // --- Admin Libraries Tab ---

  async function renderAdminLibraries(body) {
    const [libs, webdavs, schedRes] = await Promise.all([
      api('/admin/api/libraries'),
      api('/admin/api/webdav-accounts'),
      api('/admin/api/schedules').catch(() => ({ timezone: '', schedules: {} })),
    ]);
    const allLibs = libs || [];
    const schedulesMap = (schedRes && schedRes.schedules) || {};
    const serverTZ = (schedRes && schedRes.timezone) || '';
    const hiddenLibCount = allLibs.filter((l) => !l.enabled && !l.unionParentId).length;
    const subLibCount = allLibs.filter((l) => Boolean(l.unionParentId)).length;
    const scannableLibCount = allLibs.filter((l) => l.enabled && l.providerType !== 'union').length;
    const eligibleForNewUnion = allLibs.filter((l) => l.providerType !== 'union' && !l.unionParentId);

    const libByID = new Map(allLibs.map((l) => [l.id, l]));
    const unionIDs = new Set(allLibs.filter((l) => l.providerType === 'union').map((l) => l.id));
    const subsByUnion = new Map();
    allLibs.forEach((l) => {
      if (!l.unionParentId || !unionIDs.has(l.unionParentId)) return;
      if (!subsByUnion.has(l.unionParentId)) subsByUnion.set(l.unionParentId, []);
      subsByUnion.get(l.unionParentId).push(l);
    });
    subsByUnion.forEach((list, parentID) => {
      const parent = libByID.get(parentID);
      const orderMap = new Map((parent && parent.unionLayers || []).map((ly, idx) => [ly.memberLibraryId, idx]));
      list.sort((a, b) => {
        const pa = orderMap.has(a.id) ? orderMap.get(a.id) : Number(a.unionPriority || 0);
        const pb = orderMap.has(b.id) ? orderMap.get(b.id) : Number(b.unionPriority || 0);
        return pa - pb || a.name.localeCompare(b.name);
      });
    });
    const topLibs = allLibs.filter((l) => !l.unionParentId || !unionIDs.has(l.unionParentId));

    const renderLibCardHTML = (l, nested = false, layerRank = 0) => {
      const isUnion = l.providerType === 'union';
      const isSub = Boolean(l.unionParentId);
      const sched = schedulesMap[l.id];
      const schedText = formatScheduleSummary(sched);
      const subChildren = isUnion ? (subsByUnion.get(l.id) || []) : [];
      const cardClass = nested ? 'lib-card lib-sub-card' : `lib-card${!l.enabled ? ' lib-card-hidden' : ''}`;
      return `
      <div class="${cardClass}" style="${!l.enabled && nested ? 'opacity:0.72;' : ''}">
        <div class="lib-card-row">
          <div class="lib-card-info">
            <div class="lib-card-title-row">
              ${nested ? `<span class="badge badge-accent">#${layerRank + 1}</span>` : ''}
              <strong style="font-size:${nested ? '14.5px' : '15.5px'};overflow-wrap:anywhere">${esc(l.name)}</strong>
              <span class="badge ${isUnion ? 'badge-accent' : ''}">${esc(providerLabel(l.providerType, l.webdavAccountName))}</span>
              ${l.oneShot ? `<span class="badge badge-accent">单本 One-Shots</span>` : ''}
              ${isSub && !nested ? `<span class="badge">子库 · 归属：${esc(l.unionParentName)} (#${Number(l.unionPriority || 0) + 1})</span>` : ''}
              ${!l.enabled ? `<span class="badge badge-warn">已隐藏</span>` : ''}
              ${schedText ? `<span class="badge badge-success" title="服务器本机时区：${esc(serverTZ || 'Local')}">⏰ ${esc(schedText)}</span>` : ''}
            </div>
            <div style="font-size:12px;color:var(--text-muted);margin-top:6px;overflow-wrap:anywhere;word-break:break-word">
              ${isUnion
                ? `叠加层级：${(l.unionLayers || []).map((ly, idx) => `#${idx + 1} ${esc(ly.memberLibraryName)} (${esc(providerLabel(ly.providerType, ly.webdavAccountName))})`).join(' → ') || '无'}`
                : `定位标识：<code>${esc(l.providerType === '115' ? `CID:${l.rootCid}` : l.rootPath || l.rootCid)}</code>`}
              · ${Number(l.seriesCount || 0).toLocaleString()} 系列 · ${Number(l.bookCount ?? l.booksCount ?? 0).toLocaleString()} 册 · ${fmtBytes(l.comicBytes)}
            </div>
            <div style="font-size:12px;color:var(--text-muted);margin-top:2px">
              ${isSub ? `<span>联合子库：不对前台书架与 Komga 单独暴露，扫库完成后自动刷新所属联合库 · </span>` : ''}
              ${!l.enabled ? `<span>已隐藏：保留全部数据与缓存，不参与前台书架、客户端与定时任务 · </span>` : ''}
              ${isUnion ? '最近合并' : '最近扫描'}：${esc(l.lastScanStatus || '从未')} ${l.lastScanCompletedAt ? `(${fmtTime(l.lastScanCompletedAt)})` : ''}
              ${l.lastScanError ? `<span style="color:var(--danger)"> · ${esc(l.lastScanError)}</span>` : ''}
            </div>
          </div>
          <div style="display:flex;flex-wrap:wrap;gap:6px">
            ${isUnion ? `
              ${l.enabled ? `
                <button type="button" class="btn btn-primary btn-sm" data-lib-union-refresh="${esc(l.id)}">刷新合并</button>
                <button type="button" class="btn btn-sm" data-lib-pipeline="${esc(l.id)}">跑流水线</button>
                <button type="button" class="btn btn-sm" data-lib-schedule="${esc(l.id)}">定时…</button>
                <button type="button" class="btn btn-bgm btn-sm" data-lib-bgm="${esc(l.id)}">刮削 Bangumi</button>
                <button type="button" class="btn btn-sm" data-lib-cover="${esc(l.id)}" data-provider="union">补封面…</button>
                <button type="button" class="btn btn-sm" data-lib-toggle-hide="${esc(l.id)}" data-lib-name="${esc(l.name)}" data-lib-next-enabled="false">隐藏</button>
              ` : `
                <button type="button" class="btn btn-primary btn-sm" data-lib-toggle-hide="${esc(l.id)}" data-lib-name="${esc(l.name)}" data-lib-next-enabled="true">取消隐藏</button>
              `}
              <button type="button" class="btn btn-sm" data-lib-edit-union="${esc(l.id)}">编辑层级</button>
              <button type="button" class="btn btn-sm btn-danger" data-lib-del="${esc(l.id)}">删除</button>
            ` : isSub ? `
              ${l.enabled ? `
                <button type="button" class="btn btn-primary btn-sm" data-lib-scan="${esc(l.id)}">独立扫描</button>
                <button type="button" class="btn btn-sm" data-lib-schedule="${esc(l.id)}">定时…</button>
              ` : ''}
              <button type="button" class="btn btn-sm" data-lib-edit='${esc(JSON.stringify(l))}'>编辑</button>
              <button type="button" class="btn btn-sm btn-danger" disabled title="请先从所属联合库中移出后再删除">删除</button>
            ` : `
              ${l.enabled ? `
                <button type="button" class="btn btn-primary btn-sm" data-lib-scan="${esc(l.id)}">扫描</button>
                <button type="button" class="btn btn-sm" data-lib-pipeline="${esc(l.id)}">跑流水线</button>
                <button type="button" class="btn btn-sm" data-lib-schedule="${esc(l.id)}">定时…</button>
                <button type="button" class="btn btn-bgm btn-sm" data-lib-bgm="${esc(l.id)}">刮削 Bangumi</button>
                <button type="button" class="btn btn-sm" data-lib-cover="${esc(l.id)}" data-provider="${esc(l.providerType)}">补齐首卷封面…</button>
                <button type="button" class="btn btn-sm" data-lib-toggle-hide="${esc(l.id)}" data-lib-name="${esc(l.name)}" data-lib-next-enabled="false">隐藏</button>
              ` : `
                <button type="button" class="btn btn-primary btn-sm" data-lib-toggle-hide="${esc(l.id)}" data-lib-name="${esc(l.name)}" data-lib-next-enabled="true">取消隐藏</button>
              `}
              <button type="button" class="btn btn-sm" data-lib-edit='${esc(JSON.stringify(l))}'>编辑</button>
              <button type="button" class="btn btn-sm btn-danger" data-lib-del="${esc(l.id)}">删除</button>
            `}
          </div>
        </div>
        ${isUnion && subChildren.length > 0 ? `
          <div class="lib-union-toggle-bar">
            <button type="button" class="lib-union-toggle-btn" data-union-toggle="${esc(l.id)}" data-sub-count="${subChildren.length}" aria-expanded="false">
              ▸ 展开物理子库（${subChildren.length}）
            </button>
          </div>` : ''}
      </div>`;
    };

    body.innerHTML = `
      <div class="card">
        <h3>新增书库</h3>
        <div class="grid-2">
          <div class="field">
            <label>存储源类型</label>
            <select id="nl-provider">
              <option value="115">115 网盘目录</option>
              <option value="local">服务器本地 / SMB 挂载目录</option>
              <option value="webdav">WebDAV 远程目录（CloudDrive2 / OpenList / NAS）</option>
              <option value="union">联合库（多物理库按优先级分层叠加）</option>
            </select>
          </div>
          <div class="field">
            <label>书库显示名称</label>
            <input type="text" id="nl-name" placeholder="例如：日漫连载 / 本地珍藏 / 漫画联合库">
          </div>
        </div>

        <div id="nl-115-box">
          <div class="field">
            <label>115 目录 CID <button type="button" class="btn btn-sm" id="nl-browse-115" style="margin-left:8px">浏览 115 文件夹</button></label>
            <input type="text" id="nl-cid" placeholder="输入 115 文件夹 CID 或点击上方浏览选择">
          </div>
        </div>

        <div id="nl-local-box" style="display:none">
          <div class="field">
            <label>服务器本地或 SMB 挂载绝对路径 <button type="button" class="btn btn-sm" id="nl-browse-local" style="margin-left:8px">浏览目录</button></label>
            <input type="text" id="nl-local-path" placeholder="例如：/share/manga、/mnt/smb/comics 或 Z:\\Manga">
          </div>
        </div>

        <div id="nl-webdav-box" style="display:none">
          <div class="grid-2">
            <div class="field">
              <label>选择 WebDAV 账号</label>
              <select id="nl-wd-account">
                ${(webdavs || []).map((w) => `<option value="${esc(w.id)}">${esc(w.name)} (${esc(w.baseUrl)})</option>`).join('') || '<option value="">请先在「存储源」中添加 WebDAV 账号</option>'}
              </select>
            </div>
            <div class="field">
              <label>WebDAV 子目录路径 <button type="button" class="btn btn-sm" id="nl-browse-wd" style="margin-left:8px">浏览远程目录</button></label>
              <input type="text" id="nl-wd-path" value="/" placeholder="/comics">
            </div>
          </div>
        </div>

        <div id="nl-union-box" style="display:none;margin-bottom:12px">
          <div class="field" style="margin-bottom:6px">
            <label>选择要纳入的物理书库并调整层优先级（#1 优先级最高，同名文件上层覆盖下层，读取失败自动回退下层）</label>
            <p style="margin:4px 0 8px;font-size:12px;color:var(--text-muted)">
              物理库纳入联合库后自动成为子库（对前台书架与 Komga 隐藏，保留后台独立扫描）；所选子库的 One-Shots 模式必须一致。
            </p>
            ${eligibleForNewUnion.length === 0
              ? `<div class="empty-box" style="padding:12px">暂无可纳入联合库的空闲物理书库，请先添加物理书库。</div>`
              : `<div id="nl-union-picker" class="union-layer-list"></div>`}
          </div>
        </div>

        <div id="nl-browser-drawer" style="display:none;margin-bottom:14px"></div>

        <div style="display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:12px">
          <label class="check-row" id="nl-oneshot-wrap">
            <input type="checkbox" id="nl-oneshot">
            <span>单本漫画库（One-Shots 模式：每个压缩包独立成为一部作品）</span>
          </label>
          <button type="button" class="btn btn-primary" id="nl-create-btn">创建书库</button>
        </div>
      </div>

      <div class="card">
        <div class="section-head">
          <h3>已配置书库（${topLibs.length}${subLibCount > 0 ? ` · 含 ${subLibCount} 个联合子库` : ''}${hiddenLibCount > 0 ? ` · 已隐藏 ${hiddenLibCount}` : ''}）</h3>
          <button type="button" class="btn btn-sm" id="lib-scan-all" ${scannableLibCount === 0 ? 'disabled' : ''}>扫描全部物理书库</button>
        </div>
        ${topLibs.length === 0
          ? `<div class="empty-box">暂无书库。</div>`
          : `<div style="display:grid;gap:12px">
              ${topLibs.map((l) => {
                const isUnion = l.providerType === 'union';
                const subChildren = isUnion ? (subsByUnion.get(l.id) || []) : [];
                if (!isUnion || subChildren.length === 0) {
                  return renderLibCardHTML(l, false, 0);
                }
                return `
                <div class="lib-union-group">
                  ${renderLibCardHTML(l, false, 0)}
                  <div class="lib-sub-tree" data-union-children="${esc(l.id)}" hidden>
                    ${subChildren.map((sub, idx) => renderLibCardHTML(sub, true, idx)).join('')}
                  </div>
                </div>`;
              }).join('')}
            </div>`}
      </div>
    `;

    // Interactive union layer picker for new union library
    let newUnionSelected = [];
    const renderUnionPicker = (containerEl, candidates, selectedIDs, onChange) => {
      if (!containerEl) return;
      const byID = new Map(candidates.map((c) => [c.id, c]));
      const ordered = [
        ...selectedIDs.filter((id) => byID.has(id)).map((id) => ({ lib: byID.get(id), checked: true })),
        ...candidates.filter((c) => !selectedIDs.includes(c.id)).map((c) => ({ lib: c, checked: false })),
      ];
      const activeIdx = (id) => selectedIDs.indexOf(id);
      containerEl.innerHTML = ordered.map(({ lib, checked }) => {
        const idx = activeIdx(lib.id);
        return `
          <div class="union-layer-item">
            <label class="check-row" style="margin:0;flex:1;min-width:0">
              <input type="checkbox" data-upick-id="${esc(lib.id)}" ${checked ? 'checked' : ''}>
              <span>${checked ? `<strong>#${idx + 1}</strong> ` : ''}${esc(lib.name)}</span>
              <span class="badge">${esc(providerLabel(lib.providerType, lib.webdavAccountName))}</span>
              ${lib.oneShot ? `<span class="badge badge-accent">单本</span>` : ''}
            </label>
            ${checked ? `
              <div style="display:flex;gap:4px">
                <button type="button" class="btn btn-sm" data-upick-move="${esc(lib.id)}" data-dir="-1" ${idx === 0 ? 'disabled' : ''}>↑</button>
                <button type="button" class="btn btn-sm" data-upick-move="${esc(lib.id)}" data-dir="1" ${idx === selectedIDs.length - 1 ? 'disabled' : ''}>↓</button>
              </div>` : ''}
          </div>`;
      }).join('');
      containerEl.querySelectorAll('input[data-upick-id]').forEach((cb) => {
        cb.onchange = () => {
          const id = cb.getAttribute('data-upick-id');
          let next = selectedIDs.slice();
          if (cb.checked) {
            if (!next.includes(id)) next.push(id);
          } else {
            next = next.filter((x) => x !== id);
          }
          onChange(next);
        };
      });
      containerEl.querySelectorAll('button[data-upick-move]').forEach((btn) => {
        btn.onclick = () => {
          const id = btn.getAttribute('data-upick-move');
          const dir = Number(btn.getAttribute('data-dir'));
          const idx = selectedIDs.indexOf(id);
          const swap = idx + dir;
          if (idx < 0 || swap < 0 || swap >= selectedIDs.length) return;
          const next = selectedIDs.slice();
          [next[idx], next[swap]] = [next[swap], next[idx]];
          onChange(next);
        };
      });
    };

    const refreshNewUnionPicker = () => {
      renderUnionPicker(document.getElementById('nl-union-picker'), eligibleForNewUnion, newUnionSelected, (next) => {
        newUnionSelected = next;
        refreshNewUnionPicker();
      });
    };
    refreshNewUnionPicker();

    const provSel = document.getElementById('nl-provider');
    provSel.onchange = () => {
      const v = provSel.value;
      document.getElementById('nl-115-box').style.display = v === '115' ? '' : 'none';
      document.getElementById('nl-local-box').style.display = v === 'local' ? '' : 'none';
      document.getElementById('nl-webdav-box').style.display = v === 'webdav' ? '' : 'none';
      document.getElementById('nl-union-box').style.display = v === 'union' ? '' : 'none';
      document.getElementById('nl-oneshot-wrap').style.display = v === 'union' ? 'none' : '';
      const drawer = document.getElementById('nl-browser-drawer');
      if (drawer) {
        drawer._browseSeq = (Number(drawer._browseSeq) || 0) + 1;
        drawer.style.display = 'none';
        drawer.innerHTML = '';
      }
    };

    document.getElementById('nl-browse-115').onclick = () => browse115Drawer('0', [{ cid: '0', name: '115 根目录' }]);
    const localPath = document.getElementById('nl-local-path');
    const browseLocal = document.getElementById('nl-browse-local');
    if (navigator.userAgent.includes('FK115Android/')) {
      localPath.readOnly = true;
      localPath.placeholder = '使用 Android 系统选择器授予目录读取权限';
      const picker = document.createElement('a');
      picker.className = browseLocal.className;
      picker.style.cssText = browseLocal.style.cssText;
      picker.href = '/__android/pick-folder';
      picker.textContent = '选择手机目录';
      browseLocal.replaceWith(picker);
    } else {
      browseLocal.onclick = () => browseLocalDrawer(localPath.value.trim() || '/');
    }
    document.getElementById('nl-browse-wd').onclick = () => {
      const acc = document.getElementById('nl-wd-account').value;
      if (!acc) { toast('请先选择 WebDAV 账号', 'error'); return; }
      browseWebDAVDrawer(acc, document.getElementById('nl-wd-path').value.trim() || '/');
    };

    document.getElementById('nl-create-btn').onclick = async (ev) => {
      const btn = ev.currentTarget;
      const providerType = provSel.value;
      const name = document.getElementById('nl-name').value.trim();
      const oneShot = document.getElementById('nl-oneshot').checked;
      if (!name) { toast('请输入书库名称', 'error'); return; }
      const payload = { name, providerType, oneShot, enabled: true };
      if (providerType === '115') payload.rootCid = document.getElementById('nl-cid').value.trim();
      else if (providerType === 'local') payload.rootPath = document.getElementById('nl-local-path').value.trim();
      else if (providerType === 'webdav') {
        payload.webdavAccountId = document.getElementById('nl-wd-account').value;
        payload.rootPath = document.getElementById('nl-wd-path').value.trim() || '/';
      } else if (providerType === 'union') {
        if (newUnionSelected.length === 0) { toast('请至少选择一个物理书库加入联合库', 'error'); return; }
        payload.memberLibraryIds = newUnionSelected;
      }
      const origText = btn.textContent;
      btn.disabled = true;
      btn.textContent = providerType === 'union' ? '创建并合并中…' : '创建中…';
      try {
        const res = await api('/admin/api/libraries', { method: 'POST', body: payload });
        state.libraries = [];
        if (providerType === 'union' && res && res.stats) {
          toast(`联合库已创建并完成合并（${res.stats.seriesCount} 部 / ${res.stats.booksCount} 册）`);
        } else {
          toast('书库已创建');
        }
        renderAdminLibraries(body);
      } catch (err) {
        btn.disabled = false;
        btn.textContent = origText;
        toast(err.message, 'error');
      }
    };

    document.getElementById('lib-scan-all').onclick = async () => {
      try {
        await api('/admin/api/scans', { method: 'POST', body: {} });
        toast('已启动全部物理书库扫描');
        navigate('/admin?tab=jobs');
      } catch (err) { toast(err.message, 'error'); }
    };

    body.querySelectorAll('button[data-union-toggle]').forEach((btn) => {
      btn.onclick = () => {
        const unionID = btn.getAttribute('data-union-toggle');
        const subCount = btn.getAttribute('data-sub-count') || '0';
        const tree = body.querySelector(`[data-union-children="${CSS.escape(unionID)}"]`);
        if (!tree) return;
        const expanded = tree.hasAttribute('hidden');
        if (expanded) {
          tree.removeAttribute('hidden');
        } else {
          tree.setAttribute('hidden', '');
        }
        btn.setAttribute('aria-expanded', expanded ? 'true' : 'false');
        btn.textContent = expanded ? `▾ 收起物理子库（${subCount}）` : `▸ 展开物理子库（${subCount}）`;
      };
    });

    body.querySelectorAll('button[data-lib-union-refresh]').forEach((btn) => {
      btn.onclick = async () => {
        const libID = btn.getAttribute('data-lib-union-refresh');
        const origText = btn.textContent;
        btn.disabled = true;
        btn.textContent = '合并中…';
        try {
          const res = await api(`/admin/api/libraries/${encodeURIComponent(libID)}/union-refresh`, { method: 'POST' });
          state.libraries = [];
          const st = (res && res.stats) || {};
          toast(`联合库合并完成：${st.seriesCount || 0} 部 / ${st.booksCount || 0} 册`);
          renderAdminLibraries(body);
        } catch (err) {
          btn.disabled = false;
          btn.textContent = origText;
          toast(err.message, 'error');
        }
      };
    });

    body.querySelectorAll('button[data-lib-pipeline]').forEach((btn) => {
      btn.onclick = async () => {
        try {
          await api('/admin/api/pipelines', { method: 'POST', body: { libraryId: btn.getAttribute('data-lib-pipeline') } });
          toast('自动化流水线已启动');
          navigate('/admin?tab=jobs');
        } catch (err) { toast(err.message, 'error'); }
      };
    });

    body.querySelectorAll('button[data-lib-schedule]').forEach((btn) => {
      btn.onclick = () => {
        const libID = btn.getAttribute('data-lib-schedule');
        const lib = allLibs.find((x) => x.id === libID);
        if (!lib) return;
        const existing = schedulesMap[libID] || {
          libraryId: libID,
          enabled: false,
          frequency: 'daily',
          weekday: 0,
          timeOfDay: '03:00',
          scanMemberLibraryIds: lib.providerType === 'union'
            ? (lib.unionLayers || []).filter((ly) => ly.providerType === 'local').map((ly) => ly.memberLibraryId)
            : [],
          autoBangumi: true,
          autoCoverLimit: 0,
        };
        const isUnion = lib.providerType === 'union';
        const isSub = Boolean(lib.unionParentId);
        const allowUnlimitedCover = lib.providerType === 'local' || isUnion;
        openModal({
          title: `定时自动化流水线 · ${lib.name}`,
          maxWidth: '520px',
          bodyHTML: `
            <p style="margin:0 0 12px;font-size:12.5px;color:var(--text-muted)">
              按服务器本机时区（${esc(serverTZ || 'Local')}）触发。${isSub ? '该库为联合库子库：定时扫库成功后自动刷新所属联合库。' : '若未准备 Bangumi 离线库，将自动跳过 Bangumi 刮削与补封面；任一扫库失败则中止后续步骤。'}
            </p>
            <div class="field">
              <label class="check-row"><input type="checkbox" id="sch-enabled" ${existing.enabled ? 'checked' : ''}><span>启用定时自动化流水线</span></label>
            </div>
            <div class="grid-2">
              <div class="field">
                <label>执行频率</label>
                <select id="sch-freq">
                  <option value="daily" ${existing.frequency !== 'weekly' ? 'selected' : ''}>每日固定时间</option>
                  <option value="weekly" ${existing.frequency === 'weekly' ? 'selected' : ''}>每周固定时间</option>
                </select>
              </div>
              <div class="field">
                <label>触发时间（24 小时制）</label>
                <input type="time" id="sch-time" value="${esc(existing.timeOfDay || '03:00')}" required>
              </div>
            </div>
            <div class="field" id="sch-weekday-wrap" style="${existing.frequency === 'weekly' ? '' : 'display:none'}">
              <label>每周触发星期</label>
              <select id="sch-weekday">
                ${WEEKDAY_NAMES.map((name, idx) => `<option value="${idx}" ${Number(existing.weekday) === idx ? 'selected' : ''}>${name}</option>`).join('')}
              </select>
            </div>
            ${isUnion ? `
              <div class="field">
                <label>定时触发前需要扫描的子库（可只勾选最上层本地/SMB 库）</label>
                <div class="union-layer-list">
                  ${(lib.unionLayers || []).map((ly, idx) => `
                    <label class="union-layer-item" style="cursor:pointer;justify-content:flex-start">
                      <input type="checkbox" class="sch-member-cb" value="${esc(ly.memberLibraryId)}" ${(existing.scanMemberLibraryIds || []).includes(ly.memberLibraryId) ? 'checked' : ''}>
                      <span><strong>#${idx + 1}</strong> ${esc(ly.memberLibraryName)}</span>
                      <span class="badge">${esc(providerLabel(ly.providerType, ly.webdavAccountName))}</span>
                    </label>`).join('')}
                </div>
              </div>` : ''}
            ${!isSub ? `
              <div class="field">
                <label class="check-row"><input type="checkbox" id="sch-bgm" ${existing.autoBangumi !== false ? 'checked' : ''}><span>扫库/合并完成后执行 Bangumi 增量刮削</span></label>
              </div>
              <div class="field">
                <label>未匹配 Bangumi 且缺封面作品的自动补封面上限（默认 0 = 不自动补封面）</label>
                <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
                  <input type="number" id="sch-cover-limit" min="0" max="10000" value="${existing.autoCoverLimit < 0 ? 0 : Number(existing.autoCoverLimit || 0)}" style="max-width:140px" ${existing.autoCoverLimit === -1 ? 'disabled' : ''}>
                  ${allowUnlimitedCover ? `<label class="check-row" style="margin:0"><input type="checkbox" id="sch-cover-unlimited" ${existing.autoCoverLimit === -1 ? 'checked' : ''}><span>不限数量</span></label>` : ''}
                </div>
              </div>` : ''}
          `,
          footHTML: `<button type="button" class="btn btn-primary" id="sch-save">保存定时配置</button>`,
          onMount: () => {
            const freqSel = document.getElementById('sch-freq');
            const wdWrap = document.getElementById('sch-weekday-wrap');
            freqSel.onchange = () => { wdWrap.style.display = freqSel.value === 'weekly' ? '' : 'none'; };
            const unlim = document.getElementById('sch-cover-unlimited');
            const limInput = document.getElementById('sch-cover-limit');
            if (unlim && limInput) {
              unlim.onchange = () => { limInput.disabled = unlim.checked; };
            }
            document.getElementById('sch-save').onclick = async () => {
              let coverLimit = 0;
              if (!isSub) {
                if (unlim && unlim.checked) coverLimit = -1;
                else coverLimit = Math.max(0, Number(limInput ? limInput.value : 0) || 0);
              }
              const memberIDs = isUnion
                ? Array.from(document.querySelectorAll('.sch-member-cb:checked')).map((el) => el.value)
                : [];
              try {
                await api(`/admin/api/libraries/${encodeURIComponent(libID)}/schedule`, {
                  method: 'PUT',
                  body: {
                    enabled: document.getElementById('sch-enabled').checked,
                    frequency: freqSel.value,
                    weekday: Number(document.getElementById('sch-weekday').value || 0),
                    timeOfDay: document.getElementById('sch-time').value || '03:00',
                    scanMemberLibraryIds: memberIDs,
                    autoBangumi: isSub ? false : document.getElementById('sch-bgm').checked,
                    autoCoverLimit: coverLimit,
                  },
                });
                toast('定时流水线配置已保存');
                closeModal();
                renderAdminLibraries(body);
              } catch (err) { toast(err.message, 'error'); }
            };
          },
        });
      };
    });

    body.querySelectorAll('button[data-lib-edit-union]').forEach((btn) => {
      btn.onclick = () => {
        const libID = btn.getAttribute('data-lib-edit-union');
        const lib = allLibs.find((x) => x.id === libID);
        if (!lib) return;
        const candidates = allLibs.filter((x) => x.providerType !== 'union' && (!x.unionParentId || x.unionParentId === libID));
        let selected = (lib.unionLayers || []).map((ly) => ly.memberLibraryId);
        openModal({
          title: `编辑联合库 · ${lib.name}`,
          maxWidth: '520px',
          bodyHTML: `
            <div class="field"><label>联合库名称</label><input type="text" id="eu-name" value="${esc(lib.name)}"></div>
            <div class="field"><label class="check-row"><input type="checkbox" id="eu-hidden" ${!lib.enabled ? 'checked' : ''}><span>隐藏此联合库</span></label></div>
            <div class="field">
              <label>成员物理库与层优先级（#1 最高；移出后原物理库自动恢复独立显示）</label>
              <div id="eu-picker" class="union-layer-list"></div>
            </div>
          `,
          footHTML: `<button type="button" class="btn btn-primary" id="eu-save">保存并重新合并</button>`,
          onMount: () => {
            const paint = () => renderUnionPicker(document.getElementById('eu-picker'), candidates, selected, (next) => {
              selected = next;
              paint();
            });
            paint();
            document.getElementById('eu-save').onclick = async (ev) => {
              if (selected.length === 0) { toast('联合库至少需要保留一个物理子库', 'error'); return; }
              const saveBtn = ev.currentTarget;
              const origText = saveBtn.textContent;
              saveBtn.disabled = true;
              saveBtn.textContent = '保存并合并中…';
              try {
                await api(`/admin/api/libraries/${encodeURIComponent(libID)}`, {
                  method: 'PUT',
                  body: {
                    name: document.getElementById('eu-name').value.trim(),
                    enabled: !document.getElementById('eu-hidden').checked,
                    memberLibraryIds: selected,
                  },
                });
                state.libraries = [];
                toast('联合库已更新并完成重新合并');
                closeModal();
                renderAdminLibraries(body);
              } catch (err) {
                saveBtn.disabled = false;
                saveBtn.textContent = origText;
                toast(err.message, 'error');
              }
            };
          },
        });
      };
    });

    body.querySelectorAll('button[data-lib-toggle-hide]').forEach((btn) => {
      btn.onclick = async () => {
        const libID = btn.getAttribute('data-lib-toggle-hide');
        const libName = btn.getAttribute('data-lib-name') || '';
        const nextEnabled = btn.getAttribute('data-lib-next-enabled') === 'true';
        btn.disabled = true;
        try {
          await api(`/admin/api/libraries/${encodeURIComponent(libID)}`, {
            method: 'PUT',
            body: { enabled: nextEnabled },
          });
          state.libraries = [];
          toast(nextEnabled
            ? `已恢复显示书库「${libName}」`
            : `已隐藏书库「${libName}」（数据完整保留，相关运行中任务已自动中止）`);
          renderAdminLibraries(body);
        } catch (err) {
          btn.disabled = false;
          toast(err.message, 'error');
        }
      };
    });

    body.querySelectorAll('button[data-lib-scan]').forEach((btn) => {
      btn.onclick = async () => {
        try {
          await api('/admin/api/scans', { method: 'POST', body: { libraryId: btn.getAttribute('data-lib-scan') } });
          toast('扫描任务已启动');
          navigate('/admin?tab=jobs');
        } catch (err) { toast(err.message, 'error'); }
      };
    });
    body.querySelectorAll('button[data-lib-bgm]').forEach((btn) => {
      btn.onclick = async () => {
        try {
          await api('/admin/api/bangumi/runs', { method: 'POST', body: { libraryId: btn.getAttribute('data-lib-bgm'), force: false } });
          toast('已启动本库 Bangumi 批量刮削');
          navigate('/admin?tab=bangumi');
        } catch (err) { toast(err.message, 'error'); }
      };
    });
    body.querySelectorAll('button[data-lib-cover]').forEach((btn) => {
      btn.onclick = () => {
        const local = btn.dataset.provider === 'local';
        openModal({
          title: '批量补齐首卷封面',
          maxWidth: '520px',
          bodyHTML: `<p>只读首卷第一页生成缩略图，不改变 Bangumi 绑定或封面显示偏好。</p>
            <label class="check-row"><input id="cover-unbound" type="checkbox" checked>仅补未绑定 Bangumi 且缺有效封面的系列</label>
            <label class="check-row"><input id="cover-all" type="checkbox" ${local ? 'checked' : ''}>全库处理</label>
            <div class="field"><label for="cover-limit">最多处理最近更新的系列数</label>
              <input id="cover-limit" type="number" min="1" max="10000" value="${local ? 50 : 5}" ${local ? 'disabled' : ''}></div>`,
          footHTML: '<button type="button" class="btn btn-primary" id="cover-start">开始补齐</button>',
          onMount: () => {
            const all = document.getElementById('cover-all');
            const limit = document.getElementById('cover-limit');
            const start = document.getElementById('cover-start');
            all.onchange = () => { limit.disabled = all.checked; start.classList.toggle('btn-danger', all.checked); };
            all.onchange();
            start.onclick = async () => {
              if (start.disabled) return;
              if (!all.checked && (!limit.value || !limit.checkValidity())) {
                limit.reportValidity();
                return;
              }
              if (all.checked) {
                const confirmed = await confirmModal('确认处理全库？远程库可能产生网络流量，任务可在后台取消。', {
                  title: '确认全库补齐封面',
                  confirmText: '开始全库补齐',
                });
                if (!confirmed || !start.isConnected || start.disabled) return;
              }
              start.disabled = true;
              try {
                await api('/admin/api/cover-jobs', { method: 'POST', body: {
                  libraryId: btn.dataset.libCover,
                  missingUnbound: document.getElementById('cover-unbound').checked,
                  all: all.checked,
                  limit: all.checked ? 0 : Number(limit.value),
                } });
                toast('封面补齐任务已启动');
                navigate('/admin?tab=jobs');
              } catch (err) { start.disabled = false; toast(err.message, 'error'); }
            };
          },
        });
      };
    });
    body.querySelectorAll('button[data-lib-edit]').forEach((btn) => {
      btn.onclick = () => {
        const l = JSON.parse(btn.getAttribute('data-lib-edit'));
        openModal({
          title: `编辑书库 · ${l.name}`,
          maxWidth: '460px',
          bodyHTML: `
            <div class="field"><label>书库名称</label><input type="text" id="el-name" value="${esc(l.name)}"></div>
            <div class="field"><label class="check-row"><input type="checkbox" id="el-hidden" ${!l.enabled ? 'checked' : ''}><span>隐藏此书库（保留全部数据，不参与展示、客户端、扫描与刮削等其余操作）</span></label></div>
            <div class="field"><label class="check-row"><input type="checkbox" id="el-oneshot" ${l.oneShot ? 'checked' : ''} ${l.unionParentId ? 'disabled' : ''}><span>One-Shots 单本漫画库模式（修改后需重扫${l.unionParentId ? '；联合子库需先移出联合库再改' : ''}）</span></label></div>
          `,
          footHTML: `<button type="button" class="btn btn-primary" id="el-save">保存</button>`,
          onMount: () => {
            document.getElementById('el-save').onclick = async () => {
              try {
                await api(`/admin/api/libraries/${encodeURIComponent(l.id)}`, {
                  method: 'PUT',
                  body: {
                    name: document.getElementById('el-name').value.trim(),
                    enabled: !document.getElementById('el-hidden').checked,
                    oneShot: document.getElementById('el-oneshot').checked,
                  },
                });
                state.libraries = [];
                toast('书库配置已更新');
                closeModal();
                renderAdminLibraries(body);
              } catch (err) { toast(err.message, 'error'); }
            };
          },
        });
      };
    });
    body.querySelectorAll('button[data-lib-del]').forEach((btn) => {
      btn.onclick = async () => {
        if (btn.disabled) return;
        const confirmed = await confirmModal('确认删除该书库及其索引记录？（不会删除网盘或磁盘上的原始文件；若删除联合库，其物理子库会自动恢复独立显示）', {
          title: '删除书库',
          confirmText: '确认删除',
        });
        if (!confirmed || !btn.isConnected || btn.disabled) return;
        const origText = btn.textContent;
        btn.disabled = true;
        btn.textContent = '删除中…';
        try {
          await api(`/admin/api/libraries/${encodeURIComponent(btn.getAttribute('data-lib-del'))}`, { method: 'DELETE' });
          state.libraries = [];
          toast('书库已删除');
          renderAdminLibraries(body);
        } catch (err) {
          btn.disabled = false;
          btn.textContent = origText;
          toast(err.message, 'error');
        }
      };
    });
  }

  async function browse115Drawer(cid, stack) {
    const drawer = document.getElementById('nl-browser-drawer');
    if (!drawer) return;
    const reqSeq = (drawer._browseSeq = (Number(drawer._browseSeq) || 0) + 1);
    const targetCID = String(cid || '').trim() || '0';
    const trail = Array.isArray(stack) && stack.length > 0
      ? stack.map((item) => (typeof item === 'string' ? { cid: item || '0', name: item === '0' ? '115 根目录' : item } : item))
      : [{ cid: targetCID, name: targetCID === '0' ? '115 根目录' : targetCID }];
    drawer.style.display = '';
    drawer.innerHTML = `<div class="folder-browser">正在读取 115 目录…</div>`;
    try {
      const res = await api(`/admin/api/115/folders?cid=${encodeURIComponent(targetCID)}`);
      if (reqSeq !== drawer._browseSeq || drawer.isConnected === false) return;
      const currentCID = String((res && res.cid) || targetCID).trim() || '0';
      const currentNode = trail[trail.length - 1] || { cid: currentCID, name: currentCID === '0' ? '115 根目录' : currentCID };
      const breadcrumb = trail.map((n) => n.name || n.cid).join(' / ');
      const folders = (res && Array.isArray(res.folders)) ? res.folders : [];
      drawer.innerHTML = `
        <div class="folder-browser">
          <div style="display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:6px 8px;padding:4px 8px;border-bottom:1px solid var(--border)">
            <span style="font-size:12px;color:var(--text-muted);word-break:break-all;min-width:0">${esc(breadcrumb)} <code style="font-size:11.5px">(CID: ${esc(currentCID)})</code></span>
            <div style="display:flex;gap:6px;flex-wrap:wrap">
              ${trail.length > 1 ? `<button type="button" class="btn btn-sm" id="b115-up">返回上级</button>` : ''}
              <button type="button" class="btn btn-sm btn-primary" id="b115-pick-cur">选用当前目录</button>
            </div>
          </div>
          ${folders.map((f) => {
            const fid = String((f && (f.id || f.fileId)) || '').trim();
            if (!fid) return '';
            return `
              <div class="folder-item">
                <span data-b115-open="${esc(fid)}" data-b115-name="${esc(f.name)}" style="flex:1;cursor:pointer">📁 ${esc(f.name)} <small style="color:var(--text-muted)">(${esc(fid)})</small></span>
                <button type="button" class="btn btn-sm" data-b115-pick="${esc(fid)}" data-b115-name="${esc(f.name)}">选用</button>
              </div>`;
          }).join('') || `<div style="padding:12px;color:var(--text-muted)">此目录下没有子文件夹</div>`}
        </div>`;
      const up = document.getElementById('b115-up');
      if (up) {
        up.onclick = () => {
          const prevTrail = trail.slice(0, -1);
          const prev = prevTrail[prevTrail.length - 1] || { cid: '0', name: '115 根目录' };
          return browse115Drawer(prev.cid, prevTrail);
        };
      }
      const pickCur = document.getElementById('b115-pick-cur');
      if (pickCur) {
        pickCur.onclick = () => {
          document.getElementById('nl-cid').value = currentCID;
          const nameInput = document.getElementById('nl-name');
          if (nameInput && !nameInput.value.trim() && currentCID !== '0' && currentNode.name) {
            nameInput.value = currentNode.name;
          }
          drawer.style.display = 'none';
        };
      }
      drawer.querySelectorAll('[data-b115-open]').forEach((el) => {
        el.onclick = () => {
          const next = String(el.getAttribute('data-b115-open') || '').trim();
          if (!next) return;
          const nextName = el.getAttribute('data-b115-name') || next;
          return browse115Drawer(next, trail.concat({ cid: next, name: nextName }));
        };
      });
      drawer.querySelectorAll('[data-b115-pick]').forEach((btn) => {
        btn.onclick = () => {
          const picked = String(btn.getAttribute('data-b115-pick') || '').trim();
          if (!picked) return;
          document.getElementById('nl-cid').value = picked;
          const nameInput = document.getElementById('nl-name');
          if (nameInput && !nameInput.value.trim()) nameInput.value = btn.getAttribute('data-b115-name') || '';
          drawer.style.display = 'none';
        };
      });
    } catch (err) {
      if (reqSeq !== drawer._browseSeq || drawer.isConnected === false) return;
      drawer.innerHTML = `<div class="folder-browser" style="padding:10px;display:flex;justify-content:space-between;align-items:center;gap:8px;flex-wrap:wrap">
        <span style="color:var(--danger)">${esc(err.message)}</span>
        ${trail.length > 1 ? `<button type="button" class="btn btn-sm" id="b115-err-up">返回上级</button>` : ''}
      </div>`;
      const errUp = document.getElementById('b115-err-up');
      if (errUp) {
        errUp.onclick = () => {
          const prevTrail = trail.slice(0, -1);
          const prev = prevTrail[prevTrail.length - 1] || { cid: '0', name: '115 根目录' };
          return browse115Drawer(prev.cid, prevTrail);
        };
      }
    }
  }

  async function browseLocalDrawer(dirPath) {
    const drawer = document.getElementById('nl-browser-drawer');
    if (!drawer) return;
    const reqSeq = (drawer._browseSeq = (Number(drawer._browseSeq) || 0) + 1);
    drawer.style.display = '';
    drawer.innerHTML = `<div class="folder-browser">正在读取本地目录…</div>`;
    try {
      const res = await api(`/admin/api/local/folders?path=${encodeURIComponent(dirPath)}`);
      if (reqSeq !== drawer._browseSeq || drawer.isConnected === false) return;
      const folders = res.folders || [];
      drawer.innerHTML = `
        <div class="folder-browser">
          <div style="display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;padding:4px 8px;border-bottom:1px solid var(--border);gap:6px 8px">
            <code style="font-size:12px;word-break:break-all;min-width:0">${esc(res.current)}</code>
            <div style="display:flex;gap:6px;flex-wrap:wrap">
              ${res.parent && res.parent !== res.current ? `<button type="button" class="btn btn-sm" id="bloc-up">上级目录</button>` : ''}
              <button type="button" class="btn btn-sm btn-primary" id="bloc-pick-cur">选用当前目录</button>
            </div>
          </div>
          ${folders.map((f) => `
            <div class="folder-item">
              <span data-bloc-open="${esc(f.path)}" style="flex:1">📁 ${esc(f.name)}</span>
              <button type="button" class="btn btn-sm" data-bloc-pick="${esc(f.path)}" data-bloc-name="${esc(f.name)}">选用</button>
            </div>`).join('') || `<div style="padding:12px;color:var(--text-muted)">无子目录</div>`}
        </div>`;
      const up = document.getElementById('bloc-up');
      if (up) up.onclick = () => browseLocalDrawer(res.parent);
      document.getElementById('bloc-pick-cur').onclick = () => {
        document.getElementById('nl-local-path').value = res.current;
        drawer.style.display = 'none';
      };
      drawer.querySelectorAll('[data-bloc-open]').forEach((el) => {
        el.onclick = () => browseLocalDrawer(el.getAttribute('data-bloc-open'));
      });
      drawer.querySelectorAll('[data-bloc-pick]').forEach((btn) => {
        btn.onclick = () => {
          document.getElementById('nl-local-path').value = btn.getAttribute('data-bloc-pick');
          const nameInput = document.getElementById('nl-name');
          if (!nameInput.value.trim()) nameInput.value = btn.getAttribute('data-bloc-name');
          drawer.style.display = 'none';
        };
      });
    } catch (err) {
      if (reqSeq !== drawer._browseSeq || drawer.isConnected === false) return;
      drawer.innerHTML = `<div class="folder-browser" style="color:var(--danger);padding:10px">${esc(err.message)}</div>`;
    }
  }

  async function browseWebDAVDrawer(accountID, remotePath) {
    const drawer = document.getElementById('nl-browser-drawer');
    if (!drawer) return;
    const reqSeq = (drawer._browseSeq = (Number(drawer._browseSeq) || 0) + 1);
    drawer.style.display = '';
    drawer.innerHTML = `<div class="folder-browser">正在通过 PROPFIND 读取远程目录…</div>`;
    try {
      const res = await api(`/admin/api/webdav-accounts/${encodeURIComponent(accountID)}/folders?path=${encodeURIComponent(remotePath)}`);
      if (reqSeq !== drawer._browseSeq || drawer.isConnected === false) return;
      // The WebDAV API returns paths relative to the DAV base, under relPath.
      // Never turn a missing child path into a selection of the root.
      const folders = (res.folders || []).filter((folder) =>
        typeof folder?.relPath === 'string' && folder.relPath.startsWith('/') && folder.relPath !== '/');
      drawer.innerHTML = `
        <div class="folder-browser">
          <div style="display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;padding:4px 8px;border-bottom:1px solid var(--border);gap:6px 8px">
            <code style="font-size:12px;word-break:break-all;min-width:0">${esc(res.current)}</code>
            <div style="display:flex;gap:6px;flex-wrap:wrap">
              ${res.parent && res.parent !== res.current ? `<button type="button" class="btn btn-sm" id="bwd-up">上级目录</button>` : ''}
              <button type="button" class="btn btn-sm btn-primary" id="bwd-pick-cur">选用当前路径</button>
            </div>
          </div>
          ${folders.map((f) => `
            <div class="folder-item">
              <span data-bwd-open="${esc(f.relPath)}" style="flex:1">📁 ${esc(f.name)}</span>
              <button type="button" class="btn btn-sm" data-bwd-pick="${esc(f.relPath)}" data-bwd-name="${esc(f.name)}">选用</button>
            </div>`).join('') || `<div style="padding:12px;color:var(--text-muted)">无子目录</div>`}
        </div>`;
      const up = document.getElementById('bwd-up');
      if (up) up.onclick = () => browseWebDAVDrawer(accountID, res.parent);
      document.getElementById('bwd-pick-cur').onclick = () => {
        document.getElementById('nl-wd-path').value = res.current;
        drawer.style.display = 'none';
      };
      drawer.querySelectorAll('[data-bwd-open]').forEach((el) => {
        el.onclick = () => browseWebDAVDrawer(accountID, el.getAttribute('data-bwd-open'));
      });
      drawer.querySelectorAll('[data-bwd-pick]').forEach((btn) => {
        btn.onclick = () => {
          document.getElementById('nl-wd-path').value = btn.getAttribute('data-bwd-pick');
          const nameInput = document.getElementById('nl-name');
          if (!nameInput.value.trim()) nameInput.value = btn.getAttribute('data-bwd-name');
          drawer.style.display = 'none';
        };
      });
    } catch (err) {
      if (reqSeq !== drawer._browseSeq || drawer.isConnected === false) return;
      drawer.innerHTML = `<div class="folder-browser" style="color:var(--danger);padding:10px">${esc(err.message)}</div>`;
    }
  }

  // --- Admin Bangumi Scraper Center ---

  async function renderAdminBangumi(body) {
    const [st, runs, libs, tracker] = await Promise.all([
      api('/admin/api/bangumi/status'),
      api('/admin/api/bangumi/runs?limit=20'),
      api('/admin/api/libraries'),
      api('/admin/api/bangumi/tracker?limit=0').catch(() => ({})),
    ]);
    paintAdminBangumi(body, st, runs || [], libs || [], tracker || {});
  }

  function paintAdminBangumi(body, st, runs, libs, tracker) {
    const arch = st.archive || {};
    const archSrc = st.archiveSource || { source: 'zh_cn', customUrl: '', metaUrl: '' };
    const defaultDowForSrc = archSrc.source === 'official' ? 3 : 4;
    const sched = st.archiveSchedule || { enabled: false, frequency: 'weekly', dayOfWeek: defaultDowForSrc, dayOfMonth: 1, timeOfDay: '06:00' };
    const tr = tracker || {};
    const dlPct = arch.totalBytes > 0 ? Math.min(100, Math.round((arch.downloadedBytes / arch.totalBytes) * 100)) : 0;
    const prevInput = document.getElementById('bgm-proxy-url');
    const editingProxy = prevInput && document.activeElement === prevInput;
    const proxyInputVal = editingProxy ? prevInput.value : (st.proxyUrl || '');
    const selStart = editingProxy ? prevInput.selectionStart : null;
    const selEnd = editingProxy ? prevInput.selectionEnd : null;
    const oauthAppBox = document.getElementById('bgm-oauth-app-box');
    const editingOAuthApp = oauthAppBox && oauthAppBox.contains(document.activeElement);
    const trackerCustomIDDraft = editingOAuthApp ? (document.getElementById('bgm-tracker-oauth-cid')?.value ?? (tr.customOAuthClientId || '')) : (tr.customOAuthClientId || '');
    const trackerCustomSecDraft = editingOAuthApp ? (document.getElementById('bgm-tracker-oauth-csec')?.value || '') : '';
    const agentIncludeNsfw = !!state.bgmAgentIncludeNsfw;
    const buildAgentPrompt = (includeNsfw) => {
      const base = location.origin;
      const guideUrl = `${base}/api/agent/v1/guide`;
      const nsfwNote = includeNsfw
        ? '本次请同时包含 NSFW 作品（使用 nsfw=all）。'
        : '本次仅处理非 NSFW 作品（默认 nsfw=exclude）。';
      return `请帮我把 MangaAlpha（${base}）中规则未命中的漫画精确匹配并绑定 Bangumi 条目。\n请先读取公开内置指引：${guideUrl}\n然后严格按指引完成两步：\n1. 如尚未授权，先发起一键授权请求并把「授权确认链接」和「核对码」发给我，待我点击批准后自动领取 API Key；\n2. 领取授权后按内置防幻觉 SOP 逐条核实候选并批量绑定。${nsfwNote}`;
    };
    const agentPromptText = buildAgentPrompt(agentIncludeNsfw);
    const srcForm = document.getElementById('bgm-arch-source-form');
    const editingSrc = srcForm && srcForm.contains(document.activeElement);
    const srcState = editingSrc ? {
      source: document.getElementById('bgm-arch-source')?.value || archSrc.source || 'zh_cn',
      customUrl: document.getElementById('bgm-arch-custom-url')?.value ?? (archSrc.customUrl || ''),
    } : {
      source: archSrc.source || 'zh_cn',
      customUrl: archSrc.customUrl || '',
    };
    const schedForm = document.getElementById('bgm-arch-sched-form');
    const editingSched = schedForm && schedForm.contains(document.activeElement);
    const schedState = editingSched ? {
      enabled: document.getElementById('bgm-arch-sched-enabled')?.checked ?? !!sched.enabled,
      frequency: document.getElementById('bgm-arch-sched-freq')?.value || sched.frequency || 'weekly',
      dayOfWeek: Number(document.getElementById('bgm-arch-sched-dow')?.value ?? sched.dayOfWeek ?? defaultDowForSrc),
      dayOfMonth: Number(document.getElementById('bgm-arch-sched-dom')?.value ?? sched.dayOfMonth ?? 1),
      timeOfDay: document.getElementById('bgm-arch-sched-time')?.value || sched.timeOfDay || '06:00',
    } : {
      enabled: !!sched.enabled,
      frequency: sched.frequency || 'weekly',
      dayOfWeek: Number(sched.dayOfWeek ?? defaultDowForSrc),
      dayOfMonth: Number(sched.dayOfMonth ?? 1),
      timeOfDay: sched.timeOfDay || '06:00',
    };
    const weekdays = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];
    const checkResultLabel = (r) => ({
      up_to_date: '已是最新（已跳过下载）',
      updated: '已下载并更新离线库',
      failed: '检查或更新失败',
      canceled: '已取消',
    }[r] || r || '');

    body.innerHTML = `
      <div class="stat-grid">
        <div class="stat-box"><div class="k">总系列数</div><div class="v">${Number(st.totalSeries || 0).toLocaleString()}${(!st.showNsfw && st.nsfwCount > 0) ? `<span class="stat-note">已隐藏 NSFW ${Number(st.nsfwCount).toLocaleString()} 部</span>` : ''}</div></div>
        <div class="stat-box"><div class="k">已匹配 Bangumi</div><div class="v" style="color:var(--success)">${Number(st.matchedCount || 0).toLocaleString()}</div></div>
        <div class="stat-box"><div class="k">待刮削</div><div class="v">${Number(st.pendingCount || 0).toLocaleString()}</div></div>
        <div class="stat-box"><div class="k">自动未命中</div><div class="v" style="color:var(--warn)">${Number(st.unmatchedCount || 0).toLocaleString()}</div></div>
      </div>

      <div class="card">
        <div style="display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:12px">
          <div>
            <h3 style="margin:0 0 4px">NSFW 作品前台显示开关</h3>
            <div style="font-size:12.5px;color:var(--text-secondary)">
              系统正常匹配 Bangumi <code>nsfw: true</code> 条目及含成人目录特征的作品（非 <code>nsfw: true</code> 的普通受限标签作品不受影响）。默认关闭以在前台书架与 Komga 客户端中自动隐藏 NSFW 作品（当前未隐藏书库中共识别 <strong>${Number(st.nsfwCount || 0).toLocaleString()}</strong> 部 NSFW 作品）。
            </div>
          </div>
          <label class="check-row" style="font-weight:600;color:var(--text)">
            <input type="checkbox" id="bgm-show-nsfw" ${st.showNsfw ? 'checked' : ''}>
            <span>显示 NSFW 作品</span>
          </label>
        </div>
      </div>

      <div class="card">
        <div class="section-head" style="margin-bottom:8px">
          <h3>Bangumi 专用网络代理设置</h3>
          <span class="badge ${st.proxyUrl ? 'badge-success' : ''}">${st.proxyUrl ? '已启用代理' : '当前为直连'}</span>
        </div>
        <p style="font-size:12.5px;color:var(--text-secondary);margin:0 0 12px">
          统一加速 GitHub 离线快照下载（中文简介增强版 / <code>bangumi/Archive</code>）、软件版本更新检测与下载、<code>api.bgm.tv</code> 检索与 <code>lain.bgm.tv</code> 封面抓取。<strong>仅作用于 Bangumi 与 GitHub 相关请求，绝不影响 115 网盘与 WebDAV 流量。</strong>
        </p>
        <div class="inline-action-bar">
          <input type="text" id="bgm-proxy-url" value="${esc(proxyInputVal)}" placeholder="留空直连；支持 http://127.0.0.1:7890 或 socks5://127.0.0.1:1080" style="height:36px;padding:0 11px;border-radius:8px;border:1px solid var(--border-strong);background:var(--bg-input);color:var(--text)">
          <button type="button" class="btn btn-primary" id="bgm-proxy-save">保存代理</button>
          <button type="button" class="btn" id="bgm-proxy-test">测试连通性与延迟</button>
          ${st.proxyUrl ? `<button type="button" class="btn btn-danger" id="bgm-proxy-clear">清空改回直连</button>` : ''}
        </div>
        <div id="bgm-proxy-probe-result" style="margin-top:10px;font-size:12.5px">${state.bgmProxyProbeHTML || ''}</div>
      </div>

      <div class="grid-2">
        <div class="card">
          <h3>引擎一：Bangumi 离线索引库（推荐大规模书库使用）</h3>
          <p style="font-size:12.5px;color:var(--text-secondary)">
            支持默认<strong>中文简介增强镜像</strong>（全站非 NSFW 外文简介 AI 翻译为简体中文，与官方 100% 同构）或切换回 <code>bangumi/Archive</code> 官方原版 / 自定义源。拉取快照并校验 SHA256 后流式导入全量书籍主条目，几万部漫画可在本地 SQLite 毫秒级完成匹配，零 API 限流风险。
          </p>
          <form id="bgm-arch-source-form" style="margin:10px 0 12px;padding:10px 12px;border-radius:8px;background:var(--bg-input);border:1px solid var(--border)">
            <div style="font-size:12.5px;font-weight:600;margin-bottom:6px">离线快照数据源</div>
            <div class="inline-action-bar">
              <select id="bgm-arch-source" style="height:34px;padding:0 8px;border-radius:7px;border:1px solid var(--border-strong);background:var(--bg-card);color:var(--text)">
                <option value="zh_cn" ${srcState.source === 'zh_cn' ? 'selected' : ''}>中文简介增强镜像（默认 · xJogger/bangumi-archive-zh）</option>
                <option value="official" ${srcState.source === 'official' ? 'selected' : ''}>Bangumi 官方原版（bangumi/Archive）</option>
                <option value="custom" ${srcState.source === 'custom' ? 'selected' : ''}>自定义兼容源（aux/latest.json）</option>
              </select>
              <button type="submit" class="btn btn-sm btn-primary" ${arch.updating ? 'disabled' : ''}>保存源</button>
            </div>
            <div id="bgm-arch-custom-wrap" style="margin-top:8px;display:${srcState.source === 'custom' ? '' : 'none'}">
              <input type="url" id="bgm-arch-custom-url" value="${esc(srcState.customUrl)}" placeholder="https://raw.githubusercontent.com/.../aux/latest.json" style="width:100%;height:34px;padding:0 10px;border-radius:7px;border:1px solid var(--border-strong);background:var(--bg-card);color:var(--text)">
            </div>
            ${archSrc.metaUrl ? `<div style="margin-top:6px;font-size:11.5px;color:var(--text-muted);word-break:break-all">当前元数据地址：<code>${esc(archSrc.metaUrl)}</code></div>` : ''}
          </form>
          <div style="font-size:13px;margin:12px 0;display:grid;gap:4px">
            <div>状态：${arch.ready ? `<span class="badge badge-success">已就绪 · ${Number(arch.subjectCount || 0).toLocaleString()} 条书籍条目</span>` : `<span class="badge badge-warn">尚未建立离线索引</span>`}</div>
            ${arch.dumpName ? `<div style="color:var(--text-muted);font-size:12px">快照版本：${esc(arch.dumpName)} · 导入于 ${fmtTime(arch.importedAt)}</div>` : ''}
            ${(arch.lastCheckAt || sched.lastCheckedAt) ? `<div style="color:var(--text-muted);font-size:12px">最近检查：${fmtTime(arch.lastCheckAt || sched.lastCheckedAt)}${checkResultLabel(arch.lastCheckResult || sched.lastResult) ? ` · ${esc(checkResultLabel(arch.lastCheckResult || sched.lastResult))}` : ''}</div>` : ''}
            ${arch.lastSyncAt ? `<div style="color:var(--text-muted);font-size:12px">最近元数据自动同步：${fmtTime(arch.lastSyncAt)} · 增量更新 ${Number(arch.lastSyncUpdatedSeries || 0).toLocaleString()} 部作品</div>` : ''}
            ${arch.updating ? `
              <div style="margin-top:6px">
                <div style="font-size:12px;color:var(--accent);margin-bottom:4px">正在更新：${esc(arch.phase || '下载/解析中')} ${arch.totalBytes > 0 ? `(${fmtBytes(arch.downloadedBytes)} / ${fmtBytes(arch.totalBytes)} · ${dlPct}%)` : ''}</div>
                <div class="progress-track"><span style="width:${dlPct || 35}%"></span></div>
              </div>` : ''}
            ${arch.lastError ? `<div style="color:var(--danger);font-size:12px">最近错误：${esc(arch.lastError)}</div>` : ''}
            ${arch.lastSyncError ? `<div style="color:var(--danger);font-size:12px">最近同步错误：${esc(arch.lastSyncError)}</div>` : ''}
          </div>
          <div style="display:flex;gap:8px;flex-wrap:wrap">
            <button type="button" class="btn btn-primary" id="bgm-arch-update" ${arch.updating ? 'disabled' : ''}>${arch.ready ? '检查并更新离线索引库' : '一键下载并构建离线索引库'}</button>
            ${arch.updating ? `<button type="button" class="btn btn-danger" id="bgm-arch-cancel">取消下载</button>` : ''}
          </div>

          <form id="bgm-arch-sched-form" style="margin-top:14px;padding-top:12px;border-top:1px solid var(--border)">
            <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;flex-wrap:wrap;margin-bottom:8px">
              <strong style="font-size:13px">定时检查更新（服务器本机时区）</strong>
              <label class="check-row" style="font-size:12.5px">
                <input type="checkbox" id="bgm-arch-sched-enabled" ${schedState.enabled ? 'checked' : ''}>
                <span>启用定时更新</span>
              </label>
            </div>
            <div style="display:flex;gap:8px;flex-wrap:wrap;align-items:center">
              <select id="bgm-arch-sched-freq" style="height:34px;padding:0 8px;border-radius:7px;border:1px solid var(--border-strong);background:var(--bg-input);color:var(--text)">
                <option value="weekly" ${schedState.frequency === 'weekly' ? 'selected' : ''}>每周</option>
                <option value="monthly" ${schedState.frequency === 'monthly' ? 'selected' : ''}>每月</option>
              </select>
              <select id="bgm-arch-sched-dow" style="height:34px;padding:0 8px;border-radius:7px;border:1px solid var(--border-strong);background:var(--bg-input);color:var(--text);display:${schedState.frequency === 'weekly' ? '' : 'none'}">
                ${weekdays.map((lbl, idx) => `<option value="${idx}" ${schedState.dayOfWeek === idx ? 'selected' : ''}>${lbl}</option>`).join('')}
              </select>
              <select id="bgm-arch-sched-dom" style="height:34px;padding:0 8px;border-radius:7px;border:1px solid var(--border-strong);background:var(--bg-input);color:var(--text);display:${schedState.frequency === 'monthly' ? '' : 'none'}">
                ${Array.from({ length: 28 }, (_, i) => i + 1).map((d) => `<option value="${d}" ${schedState.dayOfMonth === d ? 'selected' : ''}>每月 ${d} 日</option>`).join('')}
              </select>
              <input type="time" id="bgm-arch-sched-time" value="${esc(schedState.timeOfDay)}" style="height:34px;padding:0 8px;border-radius:7px;border:1px solid var(--border-strong);background:var(--bg-input);color:var(--text)">
              <button type="submit" class="btn btn-sm btn-primary">保存定时</button>
            </div>
            <div style="margin-top:6px;font-size:11.5px;color:var(--text-muted);line-height:1.5">
              ${sched.enabled && sched.nextRunAt ? `下次计划检查：<strong>${fmtTime(sched.nextRunAt)}</strong> · ` : ''}${archSrc.source === 'official'
                ? '官方原版每周三约 05:04 发布新快照（默认推荐周三 06:00）；'
                : '上游每周三约 05:04 发布，中文增强镜像于周三上午完成增量翻译发布（默认推荐周四 06:00，切换数据源时若未自定义时间会自动同步对应默认日）；'}快照未更新自动跳过下载，导入新版后自动本地增量同步已匹配作品的未锁定元数据（不重下封面、不覆盖手动锁定字段）。
            </div>
          </form>
        </div>

        <div class="card">
          <h3>启动批量自动刮削</h3>
          <p style="font-size:12.5px;color:var(--text-secondary)">
            支持 <code>[标题][作者]</code> 与 <code>[作者][标题]</code> 双序识别、通用子目录（如 <code>台版</code>/<code>特别篇</code>）自动回溯父目录并追加后缀、繁简异体字归一与作者交叉打分，不会覆盖已手动绑定或锁定的字段。
          </p>
          <div class="field">
            <label>选择目标书库</label>
            <select id="bgm-batch-lib">
              <option value="">全部未隐藏书库</option>
              ${(libs || []).filter((l) => l.enabled !== false && !l.unionParentId).map((l) => `<option value="${esc(l.id)}">${esc(l.name)}</option>`).join('')}
            </select>
          </div>
          <div class="field">
            <label>刮削范围模式</label>
            <select id="bgm-batch-mode">
              <option value="incremental">增量刮削（仅处理尚未刮削的新作品）</option>
              <option value="retry_unmatched">重试未命中（处理“自动未命中”与新作品，规则升级后推荐）</option>
              <option value="force">全量强制重刮（重新匹配全部作品，保留手动绑定与锁定字段）</option>
            </select>
          </div>
          <button type="button" class="btn btn-bgm" id="bgm-batch-start">开始批量刮削</button>
        </div>
      </div>

      <div class="card">
        <div class="section-head" style="margin-bottom:8px">
          <h3>使用 AI Agent 协助匹配未命中作品</h3>
          <span class="badge ${Number(st.unmatchedCount || 0) > 0 ? 'badge-accent' : 'badge-success'}">未命中 ${Number(st.unmatchedCount || 0).toLocaleString()} 部 · 待刮削 ${Number(st.pendingCount || 0).toLocaleString()} 部</span>
        </div>
        <p style="margin:0 0 10px;font-size:12.5px;color:var(--text-secondary);line-height:1.65">
          无需在服务器配置大模型 Key：直接将下方提示词复制给你手头的 <strong>Claude Code / Codex / Cursor / Antigravity / OpenClaw</strong> 等 AI Agent。
          Agent 会自动读取公开内置指引（不含任何私密书库数据），发给你一个带<strong>核对码</strong>的一键授权链接；你点击批准后，Agent 自动按防幻觉 SOP 核实候选并批量绑定。
        </p>
        <div style="display:flex;flex-wrap:wrap;gap:10px;align-items:center;margin-bottom:10px">
          <button type="button" class="btn btn-primary btn-sm" id="bgm-agent-copy-btn">复制给 AI Agent</button>
          <a class="btn btn-sm" href="/api/agent/v1/guide" target="_blank" rel="noopener noreferrer">查看内置匹配指引 ↗</a>
          <label class="check-row" style="font-size:12.5px;color:var(--text-secondary)">
            <input type="checkbox" id="bgm-agent-include-nsfw" ${agentIncludeNsfw ? 'checked' : ''}>
            <span>本次包含 NSFW 作品（默认排除）</span>
          </label>
        </div>
        <pre id="bgm-agent-prompt-box" class="code-box" style="margin:0;white-space:pre-wrap;word-break:break-word;font-size:12px;line-height:1.6;user-select:all">${esc(agentPromptText)}</pre>
      </div>

      <div class="card" id="bgm-oauth-app-box">
        <div class="section-head" style="margin-bottom:8px;flex-wrap:wrap;gap:8px">
          <div>
            <h3 style="margin:0 0 4px">Bangumi 进度追踪器（全局 OAuth 应用配置）</h3>
            <div style="font-size:12.5px;color:var(--text-secondary)">
              每个账号独立绑定自己的 Bangumi 进度同步；管理员和普通用户均可在个人「账号设置」中管理自己的授权、开关与同步记录。
            </div>
          </div>
          <a href="/account" data-nav class="btn btn-sm btn-primary">前往「账号设置」管理我的同步 →</a>
        </div>
        <div style="margin-top:10px;padding:10px 12px;border-radius:8px;background:var(--bg-input);border:1px solid var(--border)">
          <div style="font-size:12.5px;font-weight:600;color:var(--text-secondary);margin-bottom:6px">
            全局自定义 Bangumi OAuth App（可选，默认使用官方内置应用${tr.usingCustomOAuthApp ? ' · 当前已启用自定义 App' : ''}）
          </div>
          <div style="font-size:12px;color:var(--text-muted);margin-bottom:8px">
            如需覆盖全服 OAuth 应用，可在 <a href="https://bgm.tv/dev/app" target="_blank" rel="noopener noreferrer" style="color:var(--accent)">bgm.tv/dev/app ↗</a> 自建应用（回调地址留空即可）并填入下方，对全站所有用户生效。
          </div>
          <div class="inline-action-bar">
            <input type="text" id="bgm-tracker-oauth-cid" value="${esc(trackerCustomIDDraft)}" placeholder="自定义 App ID（例如 bgm...）"
              style="height:34px;padding:0 10px;border-radius:8px;border:1px solid var(--border-strong);background:var(--bg-card);color:var(--text)">
            <input type="password" id="bgm-tracker-oauth-csec" autocomplete="off" value="${esc(trackerCustomSecDraft)}"
              placeholder="${tr.hasCustomOAuthSecret ? '已保存自定义 App Secret（留空保持不变）' : '自定义 App Secret'}"
              style="height:34px;padding:0 10px;border-radius:8px;border:1px solid var(--border-strong);background:var(--bg-card);color:var(--text)">
            <button type="button" class="btn btn-sm" id="bgm-tracker-oauth-app-save">保存自定义 App</button>
            ${tr.usingCustomOAuthApp ? `<button type="button" class="btn btn-sm" id="bgm-tracker-oauth-app-reset">恢复默认内置 App</button>` : ''}
          </div>
        </div>
      </div>

      <div class="card">
        <h3>油猴脚本（Bangumi 条目页快捷跳转与简介简中翻译）</h3>
        <p style="margin:0 0 12px;font-size:13px;color:var(--text-secondary);line-height:1.65">
          在 <code>bgm.tv</code> / <code>bangumi.tv</code> / <code>chii.in</code> 条目页<strong>右侧收藏盒下方</strong>以原生侧栏卡片展示本库收录状态（自动适配浅色与深色模式）、一键直达作品或多版本切换栏，并在<strong>简介正上方</strong>自动提供「显示简中翻译 / 查看原文」切换按钮（复用本地中文增强离线库）。
          支持 Tampermonkey / Violentmonkey 直接弹窗安装与覆盖升级。
        </p>
        <div style="display:flex;flex-wrap:wrap;gap:10px;align-items:center">
          <a class="btn btn-primary btn-sm" id="bgm-us-install-script" href="/bgm/userscript/latest.user.js" target="_blank" rel="noopener noreferrer">① 安装 / 更新油猴脚本</a>
          <button type="button" class="btn btn-sm" id="bgm-us-pair-key">② 一键生成并同步专属 Key 到油猴</button>
        </div>
        <div style="margin-top:10px;font-size:12px;color:var(--text-muted);line-height:1.65;padding:8px 10px;border-radius:8px;background:var(--bg-input);border:1px solid var(--border)">
          <div><strong>① 源码与密钥彻底分离：</strong>脚本本体永久不含任何密钥（并内置 <code>@updateURL</code> / <code>@downloadURL</code>，支持在油猴扩展中直接点击原生「检查更新」），后续升级脚本的 Diff 永远纯净、绝不清空已保存的 Key。</div>
          <div style="margin-top:4px"><strong>② 首次或轮换 Key 同步：</strong>装完脚本后点一次「② 一键生成并同步专属 Key 到油猴」，会自动吊销旧的同名「Bangumi 油猴脚本」Key，并通过本机配对页把地址与新 Key 直接写入油猴本地存储（全程免手输、密钥不进服务端日志）。</div>
          <div style="margin-top:4px"><strong>③ 跨域权限提示说明：</strong>脚本运行在 <code>bgm.tv</code> 上、需要向你的 MangaAlpha 服务器（<code>${esc(location.hostname)}</code>）发起收录探测与简中简介请求，已在 <code>@connect</code> 中自动预授权当前主机；若油猴弹出「跨域访问确认」，点击<strong>「始终允许该域名」</strong>即可。</div>
          <div style="margin-top:4px">脚本更新地址：<code>${esc(location.origin)}/bgm/userscript/latest.user.js</code> · 直达跳转格式：<code>${esc(location.origin)}/bgm/&lt;subjectId&gt;</code></div>
        </div>
        <div id="bgm-us-status" style="margin-top:8px;font-size:12px;color:var(--text-muted)"></div>
      </div>

      <div class="card">
        <div class="section-head">
          <h3>刮削任务记录</h3>
          <button type="button" class="btn btn-sm" id="bgm-refresh-btn">刷新</button>
        </div>
        ${runs.length === 0
          ? `<div class="empty-box">暂无批量刮削任务记录。</div>`
          : `<div class="table-wrap"><table class="data-table">
              <thead><tr><th>目标</th><th>模式</th><th>状态</th><th>进度</th><th>命中 / 未命中 / 跳过</th><th>当前处理 / 开始时间</th><th>操作</th></tr></thead>
              <tbody>
                ${runs.map((r) => {
                  const pct = r.totalSeries > 0 ? Math.min(100, Math.round((r.processedSeries / r.totalSeries) * 100)) : 0;
                  return `
                    <tr>
                      <td><strong>${esc(r.libraryName || '全部未隐藏书库')}</strong></td>
                      <td><span class="badge">${esc(r.mode)}</span></td>
                      <td><span class="badge ${(r.status === 'completed' || r.status === 'success') ? 'badge-success' : r.status === 'failed' ? 'badge-danger' : 'badge-accent'}">${esc(r.status)}</span></td>
                      <td style="min-width:140px">
                        <div style="font-size:12px">${r.processedSeries} / ${r.totalSeries} (${pct}%)</div>
                        <div class="progress-track" style="margin-top:3px"><span style="width:${pct}%"></span></div>
                      </td>
                      <td><span style="color:var(--success)">${r.matchedCount}</span> / <span style="color:var(--warn)">${r.unmatchedCount}</span> / ${r.skippedCount}</td>
                      <td class="cell-wrap" style="font-size:12px;color:var(--text-muted)">${esc(r.currentSeries || fmtTime(r.startedAt || r.createdAt))}</td>
                      <td>${(r.status === 'queued' || r.status === 'running') ? `<button type="button" class="btn btn-sm btn-danger" data-cancel-bgm-run="${esc(r.id)}">取消</button>` : ''}</td>
                    </tr>`;
                }).join('')}
              </tbody>
            </table></div>`}
      </div>
    `;

    if (editingProxy) {
      const restored = document.getElementById('bgm-proxy-url');
      if (restored) {
        restored.focus();
        if (selStart !== null && selEnd !== null) {
          try { restored.setSelectionRange(selStart, selEnd); } catch (_) {}
        }
      }
    }

    document.getElementById('bgm-show-nsfw').onchange = async (e) => {
      const showNsfw = e.target.checked;
      try {
        await api('/admin/api/bangumi/config', { method: 'PUT', body: { showNsfw } });
        state.taxonomies.loaded = false;
        toast(showNsfw ? '已开启显示 NSFW 作品' : '已隐藏 NSFW 作品');
        renderAdminBangumi(body);
      } catch (err) {
        e.target.checked = !showNsfw;
        toast(err.message, 'error');
      }
    };

    document.getElementById('bgm-proxy-save').onclick = async () => {
      const proxyUrl = document.getElementById('bgm-proxy-url').value.trim();
      try {
        await api('/admin/api/bangumi/config', { method: 'PUT', body: { proxyUrl } });
        toast(proxyUrl ? 'Bangumi 代理已保存并立即生效' : '已切换为直连模式');
        renderAdminBangumi(body);
      } catch (err) { toast(err.message, 'error'); }
    };
    const clearProxyBtn = document.getElementById('bgm-proxy-clear');
    if (clearProxyBtn) {
      clearProxyBtn.onclick = async () => {
        try {
          state.bgmProxyProbeHTML = '';
          await api('/admin/api/bangumi/config', { method: 'PUT', body: { proxyUrl: '' } });
          toast('已清空代理并恢复直连');
          renderAdminBangumi(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    }
    document.getElementById('bgm-proxy-test').onclick = async () => {
      const btn = document.getElementById('bgm-proxy-test');
      const probeBox = document.getElementById('bgm-proxy-probe-result');
      const proxyUrl = document.getElementById('bgm-proxy-url').value.trim();
      btn.disabled = true;
      if (probeBox) probeBox.innerHTML = `<span style="color:var(--text-secondary)">正在测试 GitHub 与 api.bgm.tv 连通性…</span>`;
      try {
        const rep = await api('/admin/api/bangumi/proxy/test', { method: 'POST', body: { proxyUrl } });
        const fmtItem = (label, p) => p && p.ok
          ? `<span style="color:var(--success)">✅ ${label}：连通 (${p.latencyMs} ms${p.detail ? ` · ${esc(p.detail)}` : ''})</span>`
          : `<span style="color:var(--danger)">❌ ${label}：失败 (${esc((p && p.error) || '超时')})</span>`;
        state.bgmProxyProbeHTML = `<div style="display:flex;flex-wrap:wrap;gap:16px;padding:8px 11px;border-radius:8px;background:var(--bg-input);border:1px solid var(--border)">
          <span>模式：<strong>${rep.direct ? '直连' : esc(rep.proxyUrl)}</strong></span>
          ${fmtItem('api.bgm.tv', rep.api)}
          ${fmtItem('GitHub 离线库源', rep.archive)}
        </div>`;
        if (probeBox) probeBox.innerHTML = state.bgmProxyProbeHTML;
      } catch (err) {
        toast(err.message, 'error');
        if (probeBox) probeBox.innerHTML = `<span style="color:var(--danger)">${esc(err.message)}</span>`;
      } finally {
        btn.disabled = false;
      }
    };

    const archSrcSel = document.getElementById('bgm-arch-source');
    if (archSrcSel) {
      archSrcSel.onchange = () => {
        const wrap = document.getElementById('bgm-arch-custom-wrap');
        if (wrap) wrap.style.display = archSrcSel.value === 'custom' ? '' : 'none';
      };
    }
    const archSrcFormEl = document.getElementById('bgm-arch-source-form');
    if (archSrcFormEl) {
      archSrcFormEl.onsubmit = async (e) => {
        e.preventDefault();
        const source = document.getElementById('bgm-arch-source')?.value || 'zh_cn';
        const customUrl = (document.getElementById('bgm-arch-custom-url')?.value || '').trim();
        try {
          await api('/admin/api/bangumi/archive/source', {
            method: 'PUT',
            body: { source, customUrl },
          });
          toast('离线索引数据源已保存；点击「检查并更新离线索引库」即可拉取对应源');
          renderAdminBangumi(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    }

    document.getElementById('bgm-arch-update').onclick = async () => {
      try {
        await api('/admin/api/bangumi/archive/update', { method: 'POST', body: { force: false } });
        toast('已开始检查并更新离线索引库');
        renderAdminBangumi(body);
      } catch (err) { toast(err.message, 'error'); }
    };
    const archSchedFreq = document.getElementById('bgm-arch-sched-freq');
    if (archSchedFreq) {
      archSchedFreq.onchange = () => {
        const isWeekly = archSchedFreq.value === 'weekly';
        const dow = document.getElementById('bgm-arch-sched-dow');
        const dom = document.getElementById('bgm-arch-sched-dom');
        if (dow) dow.style.display = isWeekly ? '' : 'none';
        if (dom) dom.style.display = isWeekly ? 'none' : '';
      };
    }
    const archSchedFormEl = document.getElementById('bgm-arch-sched-form');
    if (archSchedFormEl) {
      archSchedFormEl.onsubmit = async (e) => {
        e.preventDefault();
        try {
          await api('/admin/api/bangumi/archive/schedule', {
            method: 'PUT',
            body: {
              enabled: !!document.getElementById('bgm-arch-sched-enabled')?.checked,
              frequency: document.getElementById('bgm-arch-sched-freq')?.value || 'weekly',
              dayOfWeek: Number(document.getElementById('bgm-arch-sched-dow')?.value ?? 3),
              dayOfMonth: Number(document.getElementById('bgm-arch-sched-dom')?.value ?? 1),
              timeOfDay: document.getElementById('bgm-arch-sched-time')?.value || '06:00',
            },
          });
          toast('Bangumi 离线库定时更新配置已保存');
          renderAdminBangumi(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    }
    const cancelArch = document.getElementById('bgm-arch-cancel');
    if (cancelArch) {
      cancelArch.onclick = async () => {
        try { await api('/admin/api/bangumi/archive/cancel', { method: 'POST' }); renderAdminBangumi(body); }
        catch (err) { toast(err.message, 'error'); }
      };
    }
    document.getElementById('bgm-batch-start').onclick = async () => {
      try {
        const mode = document.getElementById('bgm-batch-mode').value;
        await api('/admin/api/bangumi/runs', {
          method: 'POST',
          body: {
            libraryId: document.getElementById('bgm-batch-lib').value,
            mode,
            force: mode === 'force',
          },
        });
        toast('批量刮削任务已启动');
        renderAdminBangumi(body);
      } catch (err) { toast(err.message, 'error'); }
    };
    const copyTextSafe = async (txt) => {
      if (navigator.clipboard && window.isSecureContext) {
        try { await navigator.clipboard.writeText(txt); return true; } catch (_) {}
      }
      const ta = document.createElement('textarea');
      ta.value = txt;
      ta.setAttribute('readonly', '');
      ta.style.position = 'fixed';
      ta.style.left = '-9999px';
      document.body.appendChild(ta);
      ta.select();
      let ok = false;
      try { ok = document.execCommand('copy'); } catch (_) {}
      document.body.removeChild(ta);
      return ok;
    };

    const agentNsfwCheck = document.getElementById('bgm-agent-include-nsfw');
    if (agentNsfwCheck) {
      agentNsfwCheck.onchange = () => {
        state.bgmAgentIncludeNsfw = !!agentNsfwCheck.checked;
        const box = document.getElementById('bgm-agent-prompt-box');
        if (box) box.textContent = buildAgentPrompt(state.bgmAgentIncludeNsfw);
      };
    }
    const agentCopyBtn = document.getElementById('bgm-agent-copy-btn');
    if (agentCopyBtn) {
      agentCopyBtn.onclick = async () => {
        const txt = buildAgentPrompt(!!state.bgmAgentIncludeNsfw);
        const ok = await copyTextSafe(txt);
        toast(ok ? '已复制提示词，直接粘贴给你的 AI Agent 即可' : '浏览器阻止自动复制，请手动全选下方文本框复制', ok ? 'info' : 'error');
      };
    }

    const oauthAppSaveBtn = document.getElementById('bgm-tracker-oauth-app-save');
    if (oauthAppSaveBtn) {
      oauthAppSaveBtn.onclick = async () => {
        const cid = (document.getElementById('bgm-tracker-oauth-cid')?.value || '').trim();
        const csec = (document.getElementById('bgm-tracker-oauth-csec')?.value || '').trim();
        oauthAppSaveBtn.disabled = true;
        try {
          const payload = { customOAuthClientId: cid };
          if (csec) payload.customOAuthClientSecret = csec;
          await api('/admin/api/bangumi/tracker/config', { method: 'PUT', body: payload });
          toast('全局自定义 Bangumi OAuth App 配置已保存');
          renderAdminBangumi(body);
        } catch (err) {
          toast(err.message || '保存自定义 OAuth App 失败', 'error');
        } finally {
          oauthAppSaveBtn.disabled = false;
        }
      };
    }
    const oauthAppResetBtn = document.getElementById('bgm-tracker-oauth-app-reset');
    if (oauthAppResetBtn) {
      oauthAppResetBtn.onclick = async () => {
        if (!(await confirmModal('确认清除自定义 App 配置并恢复官方内置 MangaAlpha 应用？', { title: '恢复内置 OAuth 应用', confirmText: '确认恢复' }))) return;
        if (!oauthAppResetBtn.isConnected) return;
        try {
          await api('/admin/api/bangumi/tracker/config', { method: 'PUT', body: { clearCustomOAuthApp: true } });
          toast('已恢复默认内置 Bangumi OAuth 应用');
          renderAdminBangumi(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    }

    const usPairKeyBtn = document.getElementById('bgm-us-pair-key');
    if (usPairKeyBtn) {
      usPairKeyBtn.onclick = async () => {
        usPairKeyBtn.disabled = true;
        const popup = window.open('about:blank', '_blank');
        try {
          const res = await api('/admin/api/bangumi/userscript/pair-key', { method: 'POST' });
          const hash = `server=${encodeURIComponent(location.origin)}&key=${encodeURIComponent(res.key)}`;
          const pairUrl = `${location.origin}${res.pairPath || '/bgm/userscript/pair'}#${hash}`;
          if (popup && !popup.closed) {
            popup.location.href = pairUrl;
          } else {
            window.location.href = pairUrl;
          }
          const stEl = document.getElementById('bgm-us-status');
          if (stEl) {
            const rotNote = res.rotatedOldKeys > 0 ? `（已自动吊销 ${res.rotatedOldKeys} 个旧油猴 Key）` : '';
            stEl.innerHTML = `<span style="color:var(--success)">✅ 已生成新专属 Key（前缀 <code>${esc(res.keyPrefix || '')}</code>）${rotNote}并打开配对页自动写入油猴存储。若未自动完成，请先点左侧「① 安装 / 更新油猴脚本」。</span>`;
          }
          toast('已打开配对页同步专属 Key 到油猴');
        } catch (err) {
          if (popup && !popup.closed) popup.close();
          toast(err.message || '生成油猴专属 Key 失败', 'error');
        } finally {
          usPairKeyBtn.disabled = false;
        }
      };
    }

    document.getElementById('bgm-refresh-btn').onclick = () => renderAdminBangumi(body);
    body.querySelectorAll('button[data-cancel-bgm-run]').forEach((btn) => {
      btn.onclick = async () => {
        try {
          await api(`/admin/api/bangumi/runs/${encodeURIComponent(btn.getAttribute('data-cancel-bgm-run'))}/cancel`, { method: 'POST' });
          renderAdminBangumi(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    });

    const hasActive = Boolean(arch.updating) || runs.some((r) => r.status === 'queued' || r.status === 'running');
    if (hasActive) {
      schedulePoll(async () => {
        if (location.pathname !== '/admin' || state.adminTab !== 'bangumi') return;
        await renderAdminBangumi(body);
      }, 2200);
    } else {
      stopPolling();
    }
  }

  // --- Admin Jobs Tab (Adaptive Polling ONLY when active!) ---

  async function renderAdminJobs(body) {
    const [pipelines, scans, covers, maints] = await Promise.all([
      api('/admin/api/pipelines?limit=20').catch(() => []),
      api('/admin/api/scans?limit=25'),
      api('/admin/api/cover-jobs?limit=20'),
      api('/admin/api/maintenance-jobs?limit=20'),
    ]);
    paintAdminJobs(body, pipelines || [], scans || [], covers || [], maints || []);
  }

  function paintAdminJobs(body, pipelines, scans, covers, maints) {
    const isActive = (r) => r.status === 'queued' || r.status === 'running';
    const anyActive = pipelines.some(isActive) || scans.some(isActive) || covers.some(isActive) || maints.some(isActive);

    body.innerHTML = `
      <div style="display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:8px;margin-bottom:14px">
        <span style="font-size:13px;color:var(--text-secondary)">
          ${anyActive ? '⚡ 检测到运行中任务，正在自动同步进度…' : '当前无运行中任务（空闲时不会后台轮询刷屏）'}
        </span>
        <button type="button" class="btn btn-sm" id="jobs-refresh-btn">手动刷新</button>
      </div>

      <div class="card">
        <h3>自动化流水线任务（定时 / 手动）</h3>
        ${pipelines.length === 0 ? `<div class="empty-box">暂无流水线运行记录。</div>` : `
          <div class="table-wrap"><table class="data-table">
            <thead><tr><th>书库</th><th>触发</th><th>阶段</th><th>状态</th><th>摘要 / 错误</th><th>开始时间</th><th>操作</th></tr></thead>
            <tbody>
              ${pipelines.map((p) => `
                <tr>
                  <td><strong>${esc(p.libraryName || p.libraryId)}</strong></td>
                  <td><span class="badge">${esc(p.triggerType === 'scheduled' ? '定时' : '手动')}</span></td>
                  <td><span class="badge">${esc(pipelineStageLabel(p.stage))}</span></td>
                  <td><span class="badge ${p.status === 'success' ? 'badge-success' : p.status === 'failed' ? 'badge-danger' : 'badge-accent'}">${esc(p.status)}</span></td>
                  <td class="cell-wrap" style="font-size:12px">
                    ${p.summary ? `<div>${esc(p.summary)}</div>` : ''}
                    ${p.error ? `<div style="color:var(--danger)">${esc(p.error)}</div>` : ''}
                  </td>
                  <td>${fmtTime(p.startedAt || p.createdAt)}</td>
                  <td>${isActive(p) ? `<button type="button" class="btn btn-sm btn-danger" data-cancel-pipeline="${esc(p.id)}">取消</button>` : ''}</td>
                </tr>`).join('')}
            </tbody>
          </table></div>`}
      </div>

      <div class="card">
        <h3>书库扫描任务</h3>
        ${scans.length === 0 ? `<div class="empty-box">暂无扫描记录。</div>` : `
          <div class="table-wrap"><table class="data-table">
            <thead><tr><th>书库</th><th>状态</th><th>统计</th><th>开始时间</th><th>错误信息</th><th>操作</th></tr></thead>
            <tbody>
              ${scans.map((s) => `
                <tr>
                  <td><strong>${esc(s.libraryName || s.libraryId)}</strong></td>
                  <td><span class="badge ${(s.status === 'completed' || s.status === 'success') ? 'badge-success' : s.status === 'failed' ? 'badge-danger' : 'badge-accent'}">${esc(s.status)}</span></td>
                  <td>目录 ${s.directoriesSeen ?? s.directoriesScanned ?? 0} · 系列 ${s.seriesSeen ?? s.seriesFound ?? 0} · 单册 ${s.booksSeen ?? s.booksFound ?? 0}</td>
                  <td>${fmtTime(s.startedAt)}</td>
                  <td class="cell-wrap" style="color:var(--danger);font-size:12px">${esc(s.error || '')}</td>
                  <td>${isActive(s) ? `<button type="button" class="btn btn-sm btn-danger" data-cancel-scan="${esc(s.id)}">取消</button>` : ''}</td>
                </tr>`).join('')}
            </tbody>
          </table></div>`}
      </div>

      <div class="card">
        <h3>批量封面生成任务</h3>
        ${covers.length === 0 ? `<div class="empty-box">暂无批量封面任务记录。</div>` : `
          <div class="table-wrap"><table class="data-table">
            <thead><tr><th>书库</th><th>状态</th><th>进度</th><th>生成 / 跳过 / 失败</th><th>当前处理</th><th>操作</th></tr></thead>
            <tbody>
              ${covers.map((c) => `
                <tr>
                  <td><strong>${esc(c.libraryName || c.libraryId)}</strong></td>
                  <td><span class="badge ${(c.status === 'completed' || c.status === 'success') ? 'badge-success' : c.status === 'failed' ? 'badge-danger' : 'badge-accent'}">${esc(c.status)}</span></td>
                  <td>${c.processedSeries || 0} / ${c.totalSeries || 0}</td>
                  <td>${c.generatedCount || 0} / ${c.skippedCount || 0} / ${c.failedCount || 0}</td>
                  <td class="cell-wrap" style="font-size:12px;color:var(--text-muted)">${esc(c.currentSeries || fmtTime(c.startedAt))}</td>
                  <td>${isActive(c) ? `<button type="button" class="btn btn-sm btn-danger" data-cancel-cover="${esc(c.id)}">取消</button>` : ''}</td>
                </tr>`).join('')}
            </tbody>
          </table></div>`}
      </div>

      <div class="card">
        <h3>归档索引与单部维护任务</h3>
        ${maints.length === 0 ? `<div class="empty-box">暂无维护任务记录。</div>` : renderMaintJobRows(maints)}
      </div>
    `;

    document.getElementById('jobs-refresh-btn').onclick = () => renderAdminJobs(body);
    body.querySelectorAll('button[data-cancel-pipeline]').forEach((btn) => {
      btn.onclick = async () => {
        try { await api(`/admin/api/pipelines/${encodeURIComponent(btn.getAttribute('data-cancel-pipeline'))}/cancel`, { method: 'POST' }); renderAdminJobs(body); }
        catch (err) { toast(err.message, 'error'); }
      };
    });
    body.querySelectorAll('button[data-cancel-scan]').forEach((btn) => {
      btn.onclick = async () => {
        try { await api(`/admin/api/scans/${encodeURIComponent(btn.getAttribute('data-cancel-scan'))}/cancel`, { method: 'POST' }); renderAdminJobs(body); }
        catch (err) { toast(err.message, 'error'); }
      };
    });
    body.querySelectorAll('button[data-cancel-cover]').forEach((btn) => {
      btn.onclick = async () => {
        try { await api(`/admin/api/cover-jobs/${encodeURIComponent(btn.getAttribute('data-cancel-cover'))}/cancel`, { method: 'POST' }); renderAdminJobs(body); }
        catch (err) { toast(err.message, 'error'); }
      };
    });
    bindMaintCancelButtons(body, () => renderAdminJobs(body));

    if (anyActive) {
      schedulePoll(async () => {
        if (location.pathname !== '/admin' || state.adminTab !== 'jobs') return;
        await renderAdminJobs(body);
      }, 2400);
    } else {
      stopPolling();
    }
  }

  // --- Admin Cache & Settings Tab ---

  async function renderAdminSettings(body) {
    const [cache, settings, diagData, updStatus, apwData] = await Promise.all([
      api('/admin/api/cache'),
      api('/admin/api/settings'),
      api('/admin/api/diagnostics?limit=80').catch(() => ({ status: {}, events: [] })),
      api('/admin/api/update').catch(() => null),
      api('/admin/api/archive-passwords').catch(() => ({ groups: [], totalPasswords: 0, totalBindings: 0 })),
    ]);
    if (updStatus) {
      state.updateStatus = updStatus;
      paintAdminUpdateBanner(updStatus);
    }
    const upd = updStatus || {};
    const apwGroups = (apwData && Array.isArray(apwData.groups)) ? apwData.groups : [];
    const apwTotalBindings = Number(apwData && apwData.totalBindings) || 0;
    const isAndroidUA = navigator.userAgent.includes('FK115Android/');
    const entries = cache.entries || {};
    const rangeStat = entries.range || {};
    const pageStat = entries.page || {};
    const thumbStat = cache.seriesThumbnails || {};
    const bgmThumb = cache.bangumiCovers || {};
    const diagStatus = (diagData && diagData.status) || {};
    const diagEvents = (diagData && diagData.events) || [];
    const formatDiagLines = (events) => {
      if (!events || events.length === 0) return '（暂无诊断事件记录）';
      return events.slice().reverse().map((ev) => {
        const ts = (ev.ts || '').replace('T', ' ').replace(/\.\d+Z$/, 'Z');
        const parts = [`[${ts}]`, (ev.level || 'info').toUpperCase(), `${ev.component || 'core'}/${ev.stage || '-'}`];
        if (ev.action) parts.push(ev.action);
        if (ev.code) parts.push(`code=${ev.code}`);
        if (ev.status) parts.push(`http=${ev.status}`);
        if (ev.durationMs) parts.push(`${ev.durationMs}ms`);
        if (ev.detail) parts.push(ev.detail);
        if (ev.meta && Object.keys(ev.meta).length) parts.push(JSON.stringify(ev.meta));
        return parts.join(' ');
      }).join('\n');
    };

    body.innerHTML = `
      <div class="card">
        <div class="section-head" style="flex-wrap:wrap;gap:10px">
          <div>
            <h3 style="margin:0 0 4px">软件版本与更新检测</h3>
            <div style="font-size:12.5px;color:var(--text-secondary)">
              默认每 24 小时后台静默检查一次 GitHub Releases（自动复用「Bangumi 刮削中心」配置的网络代理；未配置则直连）。服务端/桌面端提供更新提示、变更日志与对应平台下载/Docker 更新命令，不强行原地替换程序；Android 客户端可在「手机端设置」中校验 SHA256 后直接安装。
            </div>
          </div>
          <label class="check-row" style="font-weight:600;color:var(--text)">
            <input type="checkbox" id="upd-auto-check" ${upd.autoCheckEnabled !== false ? 'checked' : ''}>
            <span>每 24 小时自动检查更新</span>
          </label>
        </div>
        <div style="display:flex;flex-wrap:wrap;gap:10px;align-items:center;margin:10px 0;font-size:13px">
          <span>当前版本：<code>${esc(upd.currentVersion || '-')}</code></span>
          <span class="badge">${esc(upd.platform || '-')}</span>
          ${upd.inDocker ? `<span class="badge badge-accent">Docker 容器</span>` : ''}
          ${isAndroidUA ? `<span class="badge badge-accent">Android 客户端</span>` : ''}
          ${upd.hasUpdate
            ? `<span class="badge badge-accent">发现新版本：${esc(upd.latestVersion)}</span>`
            : (upd.latestVersion ? `<span class="badge badge-success">已是最新版本 (${esc(upd.latestVersion)})</span>` : `<span class="badge">尚未检查</span>`)}
          ${upd.checkedAt ? `<span style="font-size:12px;color:var(--text-muted)">最近检查：${fmtTime(upd.checkedAt)}</span>` : ''}
        </div>
        ${upd.lastError ? `<div style="font-size:12.5px;color:var(--danger);margin-bottom:10px">最近检查失败：${esc(upd.lastError)}（若无法直连 GitHub，可在「Bangumi 刮削中心」配置网络代理）</div>` : ''}
        ${upd.inDocker && upd.dockerCommand ? `
          <div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin-bottom:10px">
            <div class="code-box" style="flex:1 1 200px;min-width:0;max-width:100%">${esc(upd.dockerCommand)}</div>
            <button type="button" class="btn btn-sm btn-primary" id="upd-copy-docker">复制拉取命令</button>
          </div>` : ''}
        <div style="display:flex;flex-wrap:wrap;gap:8px;align-items:center">
          <button type="button" class="btn btn-sm btn-primary" id="upd-check-now" ${upd.checking ? 'disabled' : ''}>${upd.checking ? '检查中…' : '立即检查更新'}</button>
          ${isAndroidUA && upd.hasUpdate ? `<a class="btn btn-sm btn-primary" href="/__android/settings">打开手机端下载并校验安装</a>` : ''}
          ${!isAndroidUA && !upd.inDocker && upd.hasUpdate && upd.assetUrl ? `<a class="btn btn-sm btn-primary" href="${esc(upd.assetUrl)}" target="_blank" rel="noopener noreferrer">下载 ${esc(upd.assetName)}${upd.assetSize ? ` (${fmtBytes(upd.assetSize)})` : ''}</a>` : ''}
          ${upd.releaseUrl ? `<a class="btn btn-sm" href="${esc(upd.releaseUrl)}" target="_blank" rel="noopener noreferrer">查看 GitHub Release 页面</a>` : ''}
          ${upd.hasUpdate && !upd.dismissed ? `<button type="button" class="btn btn-sm" id="upd-dismiss-ver">忽略 ${esc(upd.latestVersion)}</button>` : ''}
          ${upd.hasUpdate && upd.dismissed ? `<button type="button" class="btn btn-sm" id="upd-undismiss-ver">恢复顶部提示</button>` : ''}
        </div>
        ${upd.releaseNotes ? `<details style="margin-top:10px" ${upd.hasUpdate ? 'open' : ''}><summary style="cursor:pointer;font-size:12.5px;color:var(--text-secondary)">查看 ${esc(upd.latestVersion || '最新')} 更新日志</summary><pre class="diag-log-view" style="max-height:220px">${esc(upd.releaseNotes)}</pre></details>` : ''}
      </div>

      <div class="card">
        <div class="section-head">
          <h3>磁盘与数据库缓存统计</h3>
          <button type="button" class="btn btn-sm btn-danger" data-clear-cache="all">清空全部缓存</button>
        </div>
        <div class="stat-grid">
          <div class="stat-box">
            <div class="k">Range 分块缓存（115 / WebDAV）</div>
            <div class="v" style="font-size:17px">${fmtMiB(rangeStat.bytes)} · ${rangeStat.files ?? rangeStat.count ?? 0} 块</div>
            <button type="button" class="btn btn-sm" style="margin-top:8px" data-clear-cache="range">清理 Range</button>
          </div>
          <div class="stat-box">
            <div class="k">解压页面缓存</div>
            <div class="v" style="font-size:17px">${fmtMiB(pageStat.bytes)} · ${pageStat.files ?? pageStat.count ?? 0} 页</div>
            <button type="button" class="btn btn-sm" style="margin-top:8px" data-clear-cache="page">清理页面缓存</button>
          </div>
          <div class="stat-box">
            <div class="k">归档目录索引 (ZIP/RAR)</div>
            <div class="v" style="font-size:17px">${Number(cache.archiveIndexes || 0).toLocaleString()} 本</div>
            <button type="button" class="btn btn-sm" style="margin-top:8px" data-clear-cache="archive">清理索引</button>
          </div>
          <div class="stat-box">
            <div class="k">首卷缩略图缓存</div>
            <div class="v" style="font-size:17px">${fmtMiB(thumbStat.bytes)} · ${thumbStat.files ?? thumbStat.count ?? 0} 张</div>
            <button type="button" class="btn btn-sm" style="margin-top:8px" data-clear-cache="thumbnail">清理首卷封面</button>
          </div>
          <div class="stat-box">
            <div class="k">Bangumi 懒加载封面缓存</div>
            <div class="v" style="font-size:17px">${fmtMiB(bgmThumb.bytes)} · ${bgmThumb.files ?? bgmThumb.count ?? 0} 张</div>
            <button type="button" class="btn btn-sm" style="margin-top:8px" data-clear-cache="bangumi">清理 Bgm 封面</button>
          </div>
        </div>
      </div>

      <form class="card" id="sys-settings-form">
        <h3>运行参数与缓存策略</h3>
        <div class="grid-2">
          <div class="field"><label>115 API 限速 (req/s)</label><input type="text" name="api_rate_per_second" value="${esc(settings.api_rate_per_second || '3')}"></div>
          <div class="field"><label>下载直链缓存秒数 (downurl_ttl_seconds)</label><input type="number" name="downurl_ttl_seconds" value="${esc(settings.downurl_ttl_seconds || '1800')}"></div>
          <div class="field"><label>Range 分块大小 (MiB)</label><input type="number" step="0.25" min="0.25" name="range_block_size_mib" value="${esc(bytesToMiBInput(settings.range_block_size, 2))}"></div>
          <div class="field"><label>Range 缓存上限 (MiB)</label><input type="number" step="1" min="0" name="range_cache_max_mib" value="${esc(bytesToMiBInput(settings.range_cache_max_bytes, 2048))}"></div>
          <div class="field"><label>页面缓存上限 (MiB)</label><input type="number" step="1" min="0" name="page_cache_max_mib" value="${esc(bytesToMiBInput(settings.page_cache_max_bytes, 1024))}"></div>
          <div class="field"><label>向后预读页数 (page_prefetch_count)</label><input type="number" name="page_prefetch_count" value="${esc(settings.page_prefetch_count || '2')}"></div>
          <div class="field"><label>单页最大允许体积 (MiB)</label><input type="number" step="1" min="1" name="max_page_size_mib" value="${esc(bytesToMiBInput(settings.max_page_size, 50))}"></div>
          <div class="field"><label>下一卷索引预热阈值（剩余页数 1-100）</label><input type="number" name="volume_index_prefetch_remaining_pages" value="${esc(settings.volume_index_prefetch_remaining_pages || '10')}"></div>
        </div>
        <div style="display:flex;flex-wrap:wrap;gap:18px;margin:10px 0 16px">
          <label class="check-row"><input type="checkbox" name="show_nsfw" ${settings.show_nsfw === 'true' ? 'checked' : ''}><span>显示 NSFW 作品（全局默认；各账号独立开关请在个人「账号设置」调整）</span></label>
          <label class="check-row"><input type="checkbox" name="volume_index_prefetch_enabled" ${settings.volume_index_prefetch_enabled === 'true' ? 'checked' : ''}><span>启用下一卷目录索引自动预热</span></label>
        </div>
        <p style="margin:0 0 14px;font-size:12px;color:var(--text-muted)">定时扫库、Bangumi 刮削与自动补封面已迁移至「书库管理」中的单库/联合库「定时…」流水线配置。</p>
        <button type="submit" class="btn btn-primary">保存系统设置</button>
      </form>

      <div class="card">
        <div class="section-head" style="margin-bottom:8px">
          <h3 style="margin:0">已记住的压缩包密码（全服共享 · ${apwGroups.length} 组 · ${apwTotalBindings} 本）</h3>
          ${apwGroups.length > 0 ? `<button type="button" class="btn btn-sm btn-danger" id="apw-clear-all">全部清空</button>` : ''}
        </div>
        <p style="font-size:12.5px;color:var(--text-secondary);margin-top:0">在网页阅读器打开加密 ZIP / RAR 并输入正确密码后会自动记住并绑定对应漫画，同文件夹其它文件也会自动尝试复用。删除或清空密码后将同步清理对应解密页缓存，下次打开会重新提示输入密码。</p>
        ${apwGroups.length === 0 ? `<div class="empty-box" style="padding:18px">暂未记住任何压缩包密码。</div>` : `
          <div style="display:grid;gap:12px">
            ${apwGroups.map((g, idx) => `
              <div style="border:1px solid var(--border);border-radius:10px;padding:12px 14px;background:var(--bg-soft, var(--bg))">
                <div style="display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:wrap;margin-bottom:8px">
                  <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap">
                    <span class="badge">密码 #${idx + 1}</span>
                    <code data-apw-text="${esc(g.id)}" data-apw-shown="0" style="font-size:13px;padding:2px 8px;border-radius:6px;background:var(--bg-input)">••••••••</code>
                    <button type="button" class="btn btn-sm btn-ghost" data-apw-toggle="${esc(g.id)}">显示</button>
                    <span style="font-size:12px;color:var(--text-muted)">绑定 ${(g.bindings || []).length} 本 · 更新于 ${fmtTime(g.updatedAt)}</span>
                  </div>
                  <button type="button" class="btn btn-sm btn-danger" data-apw-del-group="${esc(g.id)}">删除此密码</button>
                </div>
                ${(g.bindings || []).length === 0 ? `<div style="font-size:12px;color:var(--text-muted)">暂无可见绑定条目</div>` : `
                  <div class="table-wrap"><table class="data-table">
                    <thead><tr><th>作品</th><th>卷名</th><th>书库</th><th>绑定时间</th><th>操作</th></tr></thead>
                    <tbody>
                      ${(g.bindings || []).map((b) => `
                        <tr>
                          <td>${b.seriesId ? `<a href="/series/${encodeURIComponent(b.seriesId)}" data-nav style="color:var(--accent)">${esc(b.seriesTitle || b.seriesId)}</a>` : esc(b.seriesTitle || '—')}</td>
                          <td style="font-size:12.5px">${esc(b.bookName)}</td>
                          <td style="font-size:12px;color:var(--text-secondary)">${esc(b.libraryName || '—')}</td>
                          <td style="font-size:12px;color:var(--text-muted)">${fmtTime(b.updatedAt)}</td>
                          <td><button type="button" class="btn btn-sm btn-ghost" data-apw-del-binding="${esc(b.bookId)}">解绑</button></td>
                        </tr>`).join('')}
                    </tbody>
                  </table></div>`}
              </div>`).join('')}
          </div>`}
      </div>

      <div class="card">
        <div class="section-head" style="flex-wrap:wrap;gap:10px">
          <div>
            <h3 style="margin:0 0 4px">脱敏诊断日志与排障导出</h3>
            <div style="font-size:12.5px;color:var(--text-secondary)">
              仅按白名单记录启动、扫描、归档索引、单页读取、WebDAV/SAF 错误与耗时；自动脱敏凭据、Cookie、API Key、URL 与私人路径，本地限量轮转（上限约 2 MiB，不自动上传）。
            </div>
          </div>
          <label class="check-row" style="font-weight:600;color:var(--text)">
            <input type="checkbox" id="diag-verbose-toggle" ${diagStatus.verbose ? 'checked' : ''}>
            <span>详细排障模式</span>
          </label>
        </div>
        <div style="display:flex;flex-wrap:wrap;gap:10px;align-items:center;margin:10px 0 8px;font-size:12.5px;color:var(--text-muted)">
          <span class="badge ${diagStatus.verbose ? 'badge-accent' : ''}">${diagStatus.verbose ? '详细模式' : '精简模式（默认）'}</span>
          <span>已记录 ${Number(diagStatus.eventsCount || 0).toLocaleString()} 条 · 占用 ${fmtBytes(diagStatus.totalBytes || 0)}</span>
          ${diagStatus.hasAndroidLog ? `<span class="badge badge-success">含 Android 原生事件</span>` : ''}
          <button type="button" class="btn btn-sm" id="diag-refresh-btn">刷新预览</button>
          ${isAndroidUA
            ? `<a class="btn btn-sm btn-primary" href="/__android/settings">前往手机端设置导出 (.txt)</a>`
            : `<a class="btn btn-sm btn-primary" href="/admin/api/diagnostics/export" download>导出诊断报告 (.txt)</a>`}
          <button type="button" class="btn btn-sm btn-danger" id="diag-clear-btn">清空日志</button>
        </div>
        <pre class="diag-log-view" id="diag-log-preview">${esc(formatDiagLines(diagEvents))}</pre>
      </div>
    `;

    const updCheckBtn = document.getElementById('upd-check-now');
    if (updCheckBtn) {
      updCheckBtn.onclick = async () => {
        updCheckBtn.disabled = true;
        updCheckBtn.textContent = '检查中…';
        try {
          const st = await api('/admin/api/update/check', { method: 'POST' });
          state.updateStatus = st;
          paintAdminUpdateBanner(st);
          if (st && st.lastError) toast(`检查失败：${st.lastError}`, 'error');
          else if (st && st.hasUpdate) toast(`发现新版本 ${st.latestVersion}`);
          else toast('当前已是最新版本');
          renderAdminSettings(body);
        } catch (err) {
          toast(err.message, 'error');
          updCheckBtn.disabled = false;
          updCheckBtn.textContent = '立即检查更新';
        }
      };
    }
    const updAutoToggle = document.getElementById('upd-auto-check');
    if (updAutoToggle) {
      updAutoToggle.onchange = async (e) => {
        const autoCheckEnabled = e.target.checked;
        try {
          state.updateStatus = await api('/admin/api/update/config', { method: 'PUT', body: { autoCheckEnabled } });
          toast(autoCheckEnabled ? '已开启每 24 小时自动检查更新' : '已关闭后台自动检查更新');
        } catch (err) {
          e.target.checked = !autoCheckEnabled;
          toast(err.message, 'error');
        }
      };
    }
    const updCopyDocker = document.getElementById('upd-copy-docker');
    if (updCopyDocker && upd.dockerCommand) {
      updCopyDocker.onclick = async () => {
        try {
          await navigator.clipboard.writeText(upd.dockerCommand);
          toast('已复制 Docker 拉取命令');
        } catch (_) {
          toast(upd.dockerCommand);
        }
      };
    }
    const updDismissBtn = document.getElementById('upd-dismiss-ver');
    if (updDismissBtn && upd.latestVersion) {
      updDismissBtn.onclick = async () => {
        try {
          state.updateStatus = await api('/admin/api/update/config', { method: 'PUT', body: { dismissVersion: upd.latestVersion } });
          paintAdminUpdateBanner(state.updateStatus);
          toast(`已忽略 ${upd.latestVersion} 的顶部提醒`);
          renderAdminSettings(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    }
    const updUndismissBtn = document.getElementById('upd-undismiss-ver');
    if (updUndismissBtn) {
      updUndismissBtn.onclick = async () => {
        try {
          state.updateStatus = await api('/admin/api/update/config', { method: 'PUT', body: { dismissVersion: '' } });
          paintAdminUpdateBanner(state.updateStatus);
          renderAdminSettings(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    }

    body.querySelectorAll('button[data-clear-cache]').forEach((btn) => {
      btn.onclick = async () => {
        const ct = btn.getAttribute('data-clear-cache');
        if (!(await confirmModal(`确认清理缓存 (${ct})？`, { title: '清理缓存', confirmText: '确认清理' }))) return;
        if (!btn.isConnected) return;
        try {
          await api(`/admin/api/cache/${encodeURIComponent(ct)}`, { method: 'DELETE' });
          state.coverVer = Date.now();
          toast('缓存清理完成');
          renderAdminSettings(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    });

    document.getElementById('sys-settings-form').onsubmit = async (e) => {
      e.preventDefault();
      const fd = new FormData(e.target);
      const payload = {};
      ['api_rate_per_second', 'downurl_ttl_seconds', 'page_prefetch_count', 'volume_index_prefetch_remaining_pages']
        .forEach((k) => { payload[k] = String(fd.get(k) || '').trim(); });
      const mibPairs = [
        ['range_block_size', 'range_block_size_mib', 'Range 分块大小'],
        ['range_cache_max_bytes', 'range_cache_max_mib', 'Range 缓存上限'],
        ['page_cache_max_bytes', 'page_cache_max_mib', '页面缓存上限'],
        ['max_page_size', 'max_page_size_mib', '单页最大允许体积'],
      ];
      for (const [byteKey, mibField, label] of mibPairs) {
        const converted = mibInputToBytes(fd.get(mibField));
        if (converted === null) {
          toast(`${label} 请输入有效的非负数字 (MiB)`, 'error');
          return;
        }
        payload[byteKey] = converted;
      }
      ['show_nsfw', 'volume_index_prefetch_enabled']
        .forEach((k) => { payload[k] = e.target.elements[k].checked ? 'true' : 'false'; });
      try {
        await api('/admin/api/settings', { method: 'PUT', body: payload });
        state.taxonomies.loaded = false;
        toast('系统设置已保存');
        renderAdminSettings(body);
      } catch (err) { toast(err.message, 'error'); }
    };

    const apwPwMap = new Map(apwGroups.map((g) => [g.id, g.password || '']));
    body.querySelectorAll('button[data-apw-toggle]').forEach((btn) => {
      btn.onclick = () => {
        const gid = btn.getAttribute('data-apw-toggle');
        const codeEl = body.querySelector(`code[data-apw-text="${CSS.escape(gid)}"]`);
        if (!codeEl) return;
        const shown = codeEl.getAttribute('data-apw-shown') === '1';
        if (shown) {
          codeEl.textContent = '••••••••';
          codeEl.setAttribute('data-apw-shown', '0');
          btn.textContent = '显示';
        } else {
          codeEl.textContent = apwPwMap.get(gid) || '';
          codeEl.setAttribute('data-apw-shown', '1');
          btn.textContent = '隐藏';
        }
      };
    });
    const apwClearBtn = document.getElementById('apw-clear-all');
    if (apwClearBtn) {
      apwClearBtn.onclick = async () => {
        if (!(await confirmModal('确认清空全部已记住的压缩包密码？对应加密卷缓存也将同步清理，下次打开需重新输入密码。', { title: '清空压缩包密码', confirmText: '确认清空' }))) return;
        if (!apwClearBtn.isConnected) return;
        try {
          await api('/admin/api/archive-passwords', { method: 'DELETE' });
          toast('已清空全部压缩包密码');
          renderAdminSettings(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    }
    body.querySelectorAll('button[data-apw-del-group]').forEach((btn) => {
      btn.onclick = async () => {
        if (!(await confirmModal('确认删除此条已记住的密码并解除名下全部漫画绑定？', { title: '删除压缩包密码', confirmText: '确认删除' }))) return;
        if (!btn.isConnected) return;
        try {
          await api(`/admin/api/archive-passwords/${encodeURIComponent(btn.getAttribute('data-apw-del-group'))}`, { method: 'DELETE' });
          toast('压缩包密码已删除');
          renderAdminSettings(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    });
    body.querySelectorAll('button[data-apw-del-binding]').forEach((btn) => {
      btn.onclick = async () => {
        try {
          await api(`/admin/api/archive-passwords/bindings/${encodeURIComponent(btn.getAttribute('data-apw-del-binding'))}`, { method: 'DELETE' });
          toast('已解除该卷密码绑定');
          renderAdminSettings(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    });

    const diagVerboseToggle = document.getElementById('diag-verbose-toggle');
    if (diagVerboseToggle) {
      diagVerboseToggle.onchange = async (e) => {
        const verbose = e.target.checked;
        try {
          await api('/admin/api/diagnostics/config', { method: 'PUT', body: { verbose } });
          toast(verbose ? '已开启详细诊断记录' : '已切换回精简诊断记录');
          renderAdminSettings(body);
        } catch (err) {
          e.target.checked = !verbose;
          toast(err.message, 'error');
        }
      };
    }
    const diagRefreshBtn = document.getElementById('diag-refresh-btn');
    if (diagRefreshBtn) {
      diagRefreshBtn.onclick = () => renderAdminSettings(body);
    }
    const diagClearBtn = document.getElementById('diag-clear-btn');
    if (diagClearBtn) {
      diagClearBtn.onclick = async () => {
        if (!(await confirmModal('确认清空当前所有本地诊断日志？', { title: '清空诊断日志', confirmText: '确认清空' }))) return;
        if (!diagClearBtn.isConnected) return;
        try {
          await api('/admin/api/diagnostics', { method: 'DELETE' });
          toast('诊断日志已清空');
          renderAdminSettings(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    }
  }

  // --- Admin Security & API Keys Tab ---

  // --- Admin Multi-User Management Tab ---

  async function renderAdminUsers(body) {
    const [users, libs] = await Promise.all([
      api('/admin/api/users'),
      api('/admin/api/libraries'),
    ]);
    const userList = Array.isArray(users) ? users : [];
    const topLibs = (Array.isArray(libs) ? libs : []).filter((l) => !l.unionParentId);
    const libNameMap = new Map(topLibs.map((l) => [l.id, l.name]));

    const renderLibScopeBadges = (u) => {
      if (u.role === 'admin' || u.allLibraries) {
        return `<span class="badge badge-accent">全部可见书库</span>`;
      }
      const ids = Array.isArray(u.allowedLibraryIds) ? u.allowedLibraryIds : [];
      if (ids.length === 0) {
        return `<span class="badge badge-danger">未分配任何书库</span>`;
      }
      return ids.map((lid) => `<span class="badge">${esc(libNameMap.get(lid) || lid)}</span>`).join(' ');
    };

    body.innerHTML = `
      <div class="card">
        <div class="section-head" style="margin-bottom:8px">
          <h3 style="margin:0">多用户与权限管理（${userList.length} 个账号）</h3>
          <button type="button" class="btn btn-primary btn-sm" id="btn-create-user">+ 新建普通用户</button>
        </div>
        <p style="font-size:12.5px;color:var(--text-secondary);margin-top:0;line-height:1.65">
          每位普通用户拥有完全独立的<strong>阅读进度、阅读历史、收藏夹、阅读列表、内置阅读器设置、API Key 与 Bangumi 进度追踪器</strong>；同时支持按账号精细控制可访问的书库范围与 NSFW 权限。普通用户不可访问管理后台、不可编辑作品元数据或自行修改用户名。
        </p>
        <div class="table-wrap"><table class="data-table">
          <thead>
            <tr>
              <th>用户名</th>
              <th>角色 / 状态</th>
              <th>书库权限</th>
              <th>NSFW 权限</th>
              <th>会话 / Key</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            ${userList.map((u) => {
              const isPrimaryAdmin = u.role === 'admin';
              const nsfwBadge = isPrimaryAdmin
                ? `<span class="badge badge-accent">允许${u.showNsfw ? '（当前开启）' : '（当前隐藏）'}</span>`
                : (u.allowNsfw
                  ? `<span class="badge badge-success">已授权${u.showNsfw ? '（当前开启）' : '（当前隐藏）'}</span>`
                  : `<span class="badge">禁止</span>`);
              return `
                <tr>
                  <td>
                    <strong>${esc(u.username)}</strong>
                    <div style="font-size:11.5px;color:var(--text-muted)">创建于 ${esc(fmtTime(u.createdAt))}</div>
                  </td>
                  <td>
                    <span class="badge ${isPrimaryAdmin ? 'badge-accent' : ''}">${isPrimaryAdmin ? '主管理员' : '普通用户'}</span>
                    ${u.enabled ? `<span class="badge badge-success">正常</span>` : `<span class="badge badge-danger">已停用</span>`}
                  </td>
                  <td class="cell-wrap">${renderLibScopeBadges(u)}</td>
                  <td>${nsfwBadge}</td>
                  <td style="font-size:12px;color:var(--text-secondary)">${Number(u.sessionCount || 0)} 会话 · ${Number(u.apiKeyCount || 0)} Key</td>
                  <td>
                    ${isPrimaryAdmin
                      ? `<a href="/admin?tab=security" data-nav class="btn btn-sm">管理凭据</a>`
                      : `<div class="table-actions">
                          <button type="button" class="btn btn-sm" data-usr-edit="${esc(u.id)}">编辑权限</button>
                          <button type="button" class="btn btn-sm" data-usr-reset="${esc(u.id)}">重置密码</button>
                          <button type="button" class="btn btn-sm" data-usr-toggle="${esc(u.id)}" data-usr-enabled="${u.enabled ? '1' : '0'}">${u.enabled ? '停用' : '启用'}</button>
                          <button type="button" class="btn btn-sm" data-usr-kick="${esc(u.id)}">下线</button>
                          <button type="button" class="btn btn-sm btn-danger" data-usr-del="${esc(u.id)}">删除</button>
                        </div>`}
                  </td>
                </tr>`;
            }).join('')}
          </tbody>
        </table></div>
      </div>
    `;

    if (!body || !body.isConnected) return;
    const userByID = new Map(userList.map((u) => [u.id, u]));

    const createBtn = body.querySelector('#btn-create-user');
    if (createBtn) {
      createBtn.onclick = () => {
        openUserFormModal(null, topLibs, () => renderAdminUsers(body));
      };
    }

    body.querySelectorAll('button[data-usr-edit]').forEach((btn) => {
      btn.onclick = () => {
        const u = userByID.get(btn.getAttribute('data-usr-edit'));
        if (u) openUserFormModal(u, topLibs, () => renderAdminUsers(body));
      };
    });

    body.querySelectorAll('button[data-usr-reset]').forEach((btn) => {
      btn.onclick = () => {
        const u = userByID.get(btn.getAttribute('data-usr-reset'));
        if (!u) return;
        openModal({
          title: `重置用户「${u.username}」的登录密码`,
          maxWidth: '460px',
          bodyHTML: `
            <form id="usr-reset-form">
              <p style="font-size:12.5px;color:var(--text-secondary);margin-top:0">重置密码后该用户的所有活跃浏览器会话将立即下线，需要使用新密码重新登录。</p>
              <div class="field">
                <label>新密码（至少 8 位）</label>
                <input type="password" id="usr-reset-pw" autocomplete="new-password" required minlength="8">
              </div>
              <div class="field">
                <label>确认新密码</label>
                <input type="password" id="usr-reset-pw2" autocomplete="new-password" required minlength="8">
              </div>
              <button type="submit" class="btn btn-primary">确认重置密码</button>
            </form>`,
          onMount: () => {
            document.getElementById('usr-reset-form').onsubmit = async (e) => {
              e.preventDefault();
              const pw = document.getElementById('usr-reset-pw').value;
              const pw2 = document.getElementById('usr-reset-pw2').value;
              if (pw.length < 8) { toast('密码至少需要 8 位字符', 'error'); return; }
              if (pw !== pw2) { toast('两次输入的密码不一致', 'error'); return; }
              try {
                await api(`/admin/api/users/${encodeURIComponent(u.id)}/reset-password`, {
                  method: 'POST',
                  body: { password: pw },
                });
                closeModal();
                toast(`已重置「${u.username}」的密码`);
                renderAdminUsers(body);
              } catch (err) { toast(err.message, 'error'); }
            };
          },
        });
      };
    });

    body.querySelectorAll('button[data-usr-toggle]').forEach((btn) => {
      btn.onclick = async () => {
        const uid = btn.getAttribute('data-usr-toggle');
        const u = userByID.get(uid);
        if (!u) return;
        const nextEnabled = !u.enabled;
        if (!nextEnabled) {
          const confirmed = await confirmModal(
            `确认停用用户「${u.username}」？\n停用后将立即强制下线其所有会话，且其 API Key 与 Komga / OPDS Basic 登录将被拒绝；其阅读进度与收藏夹数据会保留，重新启用即可恢复。`,
            { title: '停用用户', confirmText: '确认停用' },
          );
          if (!confirmed || !btn.isConnected) return;
        }
        try {
          await api(`/admin/api/users/${encodeURIComponent(uid)}`, {
            method: 'PUT',
            body: { enabled: nextEnabled },
          });
          toast(nextEnabled ? `已启用用户「${u.username}」` : `已停用用户「${u.username}」`);
          renderAdminUsers(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    });

    body.querySelectorAll('button[data-usr-kick]').forEach((btn) => {
      btn.onclick = async () => {
        const uid = btn.getAttribute('data-usr-kick');
        const u = userByID.get(uid);
        if (!u) return;
        if (!(await confirmModal(`确认将用户「${u.username}」的所有登录会话强制下线？`, { title: '强制下线', confirmText: '确认下线' }))) return;
        if (!btn.isConnected) return;
        try {
          const res = await api(`/admin/api/users/${encodeURIComponent(uid)}/kick`, { method: 'POST' });
          toast(`已下线 ${Number(res?.kickedSessions || 0)} 个会话`);
          renderAdminUsers(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    });

    body.querySelectorAll('button[data-usr-del]').forEach((btn) => {
      btn.onclick = async () => {
        const uid = btn.getAttribute('data-usr-del');
        const u = userByID.get(uid);
        if (!u) return;
        const confirmed = await confirmModal(
          `确认永久删除用户「${u.username}」？\n此操作将彻底清除该用户的阅读进度、阅读历史、收藏夹、阅读列表、API Key 与个人 Bangumi 追踪配置，且不可恢复（不会影响共享书库或其他用户）。`,
          { title: '永久删除用户', confirmText: '永久删除' },
        );
        if (!confirmed || !btn.isConnected) return;
        try {
          await api(`/admin/api/users/${encodeURIComponent(uid)}`, { method: 'DELETE' });
          toast(`已删除用户「${u.username}」`);
          renderAdminUsers(body);
        } catch (err) { toast(err.message, 'error'); }
      };
    });
  }

  function openUserFormModal(existing, topLibs, onSaved) {
    const isEdit = Boolean(existing);
    const curAllLibs = isEdit ? Boolean(existing.allLibraries) : true;
    const curAllowedSet = new Set(isEdit && Array.isArray(existing.allowedLibraryIds) ? existing.allowedLibraryIds : []);
    const curAllowNSFW = isEdit ? Boolean(existing.allowNsfw) : false;

    openModal({
      title: isEdit ? `编辑用户「${existing.username}」` : '新建普通用户',
      maxWidth: '560px',
      bodyHTML: `
        <form id="usr-form">
          <div class="field">
            <label>用户名（2–64 个字符，普通用户不可自行修改）</label>
            <input type="text" id="usr-f-name" value="${esc(isEdit ? existing.username : '')}" required minlength="2" maxlength="64" placeholder="例如：alice">
          </div>
          ${!isEdit ? `
            <div class="field">
              <label>初始密码（至少 8 位）</label>
              <input type="password" id="usr-f-pw" autocomplete="new-password" required minlength="8" placeholder="至少 8 位字符">
            </div>
            <div class="field">
              <label>确认初始密码</label>
              <input type="password" id="usr-f-pw2" autocomplete="new-password" required minlength="8" placeholder="再次输入密码">
            </div>` : ''}
          <div class="field">
            <label>书库访问范围</label>
            <div style="display:flex;gap:14px;flex-wrap:wrap;margin-bottom:8px">
              <label class="check-row"><input type="radio" name="usr-lib-mode" value="all" ${curAllLibs ? 'checked' : ''}><span>全部可见书库（含未来新建书库）</span></label>
              <label class="check-row"><input type="radio" name="usr-lib-mode" value="custom" ${!curAllLibs ? 'checked' : ''}><span>仅允许指定书库</span></label>
            </div>
            <div id="usr-lib-picker" class="org-picker-list" style="${curAllLibs ? 'display:none' : ''}">
              ${topLibs.length === 0
                ? `<div style="font-size:12px;color:var(--text-muted);padding:8px">当前暂无书库，创建书库后可回来勾选。</div>`
                : topLibs.map((l) => `
                  <label class="org-check-item">
                    <input type="checkbox" name="usr-lib-id" value="${esc(l.id)}" ${curAllowedSet.has(l.id) ? 'checked' : ''}>
                    <span class="org-check-name">${esc(l.name)}</span>
                    <span class="badge">${esc(providerLabel(l.providerType, l.webdavAccountName))}</span>
                    ${l.enabled === false ? `<span class="badge">全局隐藏</span>` : ''}
                  </label>`).join('')}
            </div>
          </div>
          <div class="field">
            <label class="check-row">
              <input type="checkbox" id="usr-f-nsfw" ${curAllowNSFW ? 'checked' : ''}>
              <span>允许该用户访问 NSFW 作品（开启后用户可在「账号设置」中自行开关 NSFW 显示，默认仍关闭）</span>
            </label>
          </div>
          <div style="display:flex;justify-content:flex-end;gap:8px;margin-top:14px">
            <button type="button" class="btn" id="usr-f-cancel">取消</button>
            <button type="submit" class="btn btn-primary" id="usr-f-submit">${isEdit ? '保存更改' : '创建用户'}</button>
          </div>
        </form>
      `,
      onMount: () => {
        document.getElementById('usr-f-cancel').onclick = closeModal;
        const picker = document.getElementById('usr-lib-picker');
        document.querySelectorAll('input[name="usr-lib-mode"]').forEach((r) => {
          r.onchange = () => {
            if (picker) picker.style.display = r.value === 'custom' && r.checked ? '' : (document.querySelector('input[name="usr-lib-mode"]:checked')?.value === 'custom' ? '' : 'none');
          };
        });
        document.getElementById('usr-form').onsubmit = async (e) => {
          e.preventDefault();
          const username = document.getElementById('usr-f-name').value.trim();
          const allLibraries = document.querySelector('input[name="usr-lib-mode"]:checked')?.value !== 'custom';
          const visibleLibSet = new Set(topLibs.map((l) => l.id));
          const preservedHiddenGrants = isEdit
            ? Array.from(curAllowedSet).filter((lid) => !visibleLibSet.has(lid))
            : [];
          const pickedVisible = Array.from(document.querySelectorAll('input[name="usr-lib-id"]:checked')).map((el) => el.value);
          const allowedLibraryIds = allLibraries ? [] : Array.from(new Set([...pickedVisible, ...preservedHiddenGrants]));
          const allowNsfw = Boolean(document.getElementById('usr-f-nsfw').checked);
          const btn = document.getElementById('usr-f-submit');
          btn.disabled = true;
          try {
            if (isEdit) {
              await api(`/admin/api/users/${encodeURIComponent(existing.id)}`, {
                method: 'PUT',
                body: { username, allLibraries, allowedLibraryIds, allowNsfw },
              });
              toast(`已更新用户「${username}」`);
            } else {
              const password = document.getElementById('usr-f-pw').value;
              const password2 = document.getElementById('usr-f-pw2').value;
              if (password.length < 8) { toast('密码至少需要 8 位字符', 'error'); btn.disabled = false; return; }
              if (password !== password2) { toast('两次输入的密码不一致', 'error'); btn.disabled = false; return; }
              await api('/admin/api/users', {
                method: 'POST',
                body: { username, password, allLibraries, allowedLibraryIds, allowNsfw },
              });
              toast(`已创建普通用户「${username}」`);
            }
            closeModal();
            if (onSaved) onSaved();
          } catch (err) {
            toast(err.message, 'error');
            btn.disabled = false;
          }
        };
      },
    });
  }

  // --- Collections & ReadLists Views ---

  async function loadSeriesCollectionsBar(seriesID) {
    const seq = routeSeq;
    const bar = document.getElementById('series-collections-bar');
    if (!bar) return;
    try {
      const items = await api(`/api/v1/series/${encodeURIComponent(seriesID)}/collections`);
      if (seq !== routeSeq || !bar.isConnected) return;
      if (!items || !items.length) {
        bar.style.display = 'none';
        bar.innerHTML = '';
        return;
      }
      bar.style.display = '';
      bar.innerHTML = `<span style="font-size:12px;color:var(--text-muted)">收藏夹：</span>` +
        items.map((c) => `<a href="/collections/${encodeURIComponent(c.id)}" data-nav class="chip chip-org">★ ${esc(c.name)}</a>`).join('');
    } catch (_) {}
  }

  async function loadBookReadListsBar(bookID) {
    const seq = routeSeq;
    const bar = document.getElementById('book-readlists-bar');
    if (!bar) return;
    try {
      const items = await api(`/api/v1/books/${encodeURIComponent(bookID)}/readlists`);
      if (seq !== routeSeq || !bar.isConnected) return;
      if (!items || !items.length) {
        bar.style.display = 'none';
        bar.innerHTML = '';
        return;
      }
      bar.style.display = '';
      bar.innerHTML = `<span style="font-size:12px;color:var(--text-muted)">阅读列表：</span>` +
        items.map((rl) => `<a href="/readlists/${encodeURIComponent(rl.id)}" data-nav class="chip chip-org">▤ ${esc(rl.name)}</a>`).join('');
    } catch (_) {}
  }

  function openCollectionFormModal(existing, onSaved) {
    const isEdit = !!existing;
    openModal({
      title: isEdit ? '编辑收藏夹' : '新建收藏夹',
      maxWidth: '480px',
      bodyHTML: `
        <form id="org-col-form" style="display:grid;gap:14px">
          <div class="field">
            <label for="org-col-name">收藏夹名称</label>
            <input id="org-col-name" type="text" value="${esc(existing ? existing.name : '')}" placeholder="例如：必看神作、周末补番" required>
          </div>
          <label class="org-check-row">
            <input type="checkbox" id="org-col-ordered" ${existing && existing.ordered ? 'checked' : ''}>
            <span>启用手动排序（支持在详情页调整作品顺序）</span>
          </label>
        </form>
      `,
      footHTML: `
        <button type="button" class="btn" id="org-col-cancel">取消</button>
        <button type="submit" form="org-col-form" class="btn btn-primary" id="org-col-save">${isEdit ? '保存修改' : '创建收藏夹'}</button>
      `,
      onMount: () => {
        document.getElementById('org-col-cancel').onclick = closeModal;
        focusIfDesktop(document.getElementById('org-col-name'));
        document.getElementById('org-col-form').onsubmit = async (e) => {
          e.preventDefault();
          const name = document.getElementById('org-col-name').value.trim();
          const ordered = document.getElementById('org-col-ordered').checked;
          if (!name) { toast('请输入收藏夹名称', 'error'); return; }
          const btn = document.getElementById('org-col-save');
          btn.disabled = true;
          try {
            if (isEdit) {
              await api(`/api/v1/collections/${encodeURIComponent(existing.id)}`, {
                method: 'PATCH',
                body: { name, ordered },
              });
              toast('收藏夹已更新');
            } else {
              await api('/api/v1/collections', {
                method: 'POST',
                body: { name, ordered, seriesIds: [] },
              });
              toast('收藏夹已创建');
            }
            closeModal();
            if (onSaved) onSaved();
          } catch (err) {
            toast(err.message, 'error');
            btn.disabled = false;
          }
        };
      },
    });
  }

  function openReadListFormModal(existing, onSaved) {
    const isEdit = !!existing;
    openModal({
      title: isEdit ? '编辑阅读列表' : '新建阅读列表',
      maxWidth: '500px',
      bodyHTML: `
        <form id="org-rl-form" style="display:grid;gap:14px">
          <div class="field">
            <label for="org-rl-name">阅读列表名称</label>
            <input id="org-rl-name" type="text" value="${esc(existing ? existing.name : '')}" placeholder="例如：联动阅读顺序、待看单册" required>
          </div>
          <div class="field">
            <label for="org-rl-summary">简介（可选）</label>
            <textarea id="org-rl-summary" rows="3" placeholder="记录阅读顺序说明或备注…">${esc(existing ? (existing.summary || '') : '')}</textarea>
          </div>
          <label class="org-check-row">
            <input type="checkbox" id="org-rl-ordered" ${!existing || existing.ordered ? 'checked' : ''}>
            <span>启用手动排序（按添加与调整后的顺序阅读）</span>
          </label>
        </form>
      `,
      footHTML: `
        <button type="button" class="btn" id="org-rl-cancel">取消</button>
        <button type="submit" form="org-rl-form" class="btn btn-primary" id="org-rl-save">${isEdit ? '保存修改' : '创建阅读列表'}</button>
      `,
      onMount: () => {
        document.getElementById('org-rl-cancel').onclick = closeModal;
        focusIfDesktop(document.getElementById('org-rl-name'));
        document.getElementById('org-rl-form').onsubmit = async (e) => {
          e.preventDefault();
          const name = document.getElementById('org-rl-name').value.trim();
          const summary = document.getElementById('org-rl-summary').value.trim();
          const ordered = document.getElementById('org-rl-ordered').checked;
          if (!name) { toast('请输入阅读列表名称', 'error'); return; }
          const btn = document.getElementById('org-rl-save');
          btn.disabled = true;
          try {
            if (isEdit) {
              await api(`/api/v1/readlists/${encodeURIComponent(existing.id)}`, {
                method: 'PATCH',
                body: { name, summary, ordered },
              });
              toast('阅读列表已更新');
            } else {
              await api('/api/v1/readlists', {
                method: 'POST',
                body: { name, summary, ordered, bookIds: [] },
              });
              toast('阅读列表已创建');
            }
            closeModal();
            if (onSaved) onSaved();
          } catch (err) {
            toast(err.message, 'error');
            btn.disabled = false;
          }
        };
      },
    });
  }

  async function openAddSeriesToCollectionModal(seriesID, seriesTitle, onChanged) {
    let collections = [];
    try {
      const page = await api('/api/v1/collections?unpaged=true&sort=name,asc');
      collections = (page && page.content) || [];
    } catch (err) {
      toast(err.message, 'error');
      return;
    }
    const initialSet = new Set();
    collections.forEach((c) => {
      if ((c.seriesIds || []).includes(seriesID)) initialSet.add(c.id);
    });

    const renderListHTML = () => {
      if (!collections.length) {
        return `<div class="empty-box" style="padding:18px">还没有收藏夹，请在下方输入名称快速创建并加入。</div>`;
      }
      return `<div class="org-picker-list">${collections.map((c) => `
        <label class="org-check-item">
          <input type="checkbox" data-pick-col="${esc(c.id)}" ${(c.seriesIds || []).includes(seriesID) ? 'checked' : ''}>
          <span class="org-check-name">${esc(c.name)}</span>
          <span class="badge">${(c.seriesIds || []).length} 部</span>
        </label>
      `).join('')}</div>`;
    };

    openModal({
      title: `管理收藏夹 · ${seriesTitle}`,
      maxWidth: '520px',
      bodyHTML: `
        <div style="display:grid;gap:14px">
          <div id="org-col-picker-wrap">${renderListHTML()}</div>
          <form id="org-col-quick-create" class="org-quick-create">
            <input type="text" id="org-col-quick-name" placeholder="输入新收藏夹名称并直接加入…">
            <button type="submit" class="btn btn-sm btn-primary">新建并加入</button>
          </form>
        </div>
      `,
      footHTML: `
        <button type="button" class="btn" id="org-col-pick-cancel">关闭</button>
        <button type="button" class="btn btn-primary" id="org-col-pick-save">保存更改</button>
      `,
      onMount: (body) => {
        document.getElementById('org-col-pick-cancel').onclick = closeModal;
        document.getElementById('org-col-quick-create').onsubmit = async (e) => {
          e.preventDefault();
          const input = document.getElementById('org-col-quick-name');
          const name = input.value.trim();
          if (!name) return;
          try {
            const created = await api('/api/v1/collections', {
              method: 'POST',
              body: { name, ordered: false, seriesIds: [seriesID] },
            });
            collections.push(created);
            initialSet.add(created.id);
            input.value = '';
            document.getElementById('org-col-picker-wrap').innerHTML = renderListHTML();
            toast(`已创建并加入「${created.name}」`);
            if (onChanged) onChanged();
          } catch (err) { toast(err.message, 'error'); }
        };
        document.getElementById('org-col-pick-save').onclick = async () => {
          const btn = document.getElementById('org-col-pick-save');
          btn.disabled = true;
          try {
            const boxes = Array.from(body.querySelectorAll('input[data-pick-col]'));
            let changed = 0;
            for (const box of boxes) {
              const cid = box.getAttribute('data-pick-col');
              const checked = box.checked;
              const wasIn = initialSet.has(cid);
              if (checked === wasIn) continue;
              const col = collections.find((x) => x.id === cid);
              if (!col) continue;
              const nextIDs = checked
                ? [...(col.seriesIds || []), seriesID]
                : (col.seriesIds || []).filter((x) => x !== seriesID);
              await api(`/api/v1/collections/${encodeURIComponent(cid)}`, {
                method: 'PATCH',
                body: { seriesIds: nextIDs },
              });
              changed++;
            }
            if (changed > 0) toast('收藏夹已更新');
            closeModal();
            if (onChanged) onChanged();
          } catch (err) {
            toast(err.message, 'error');
            btn.disabled = false;
          }
        };
      },
    });
  }

  async function openAddBookToReadListModal(bookID, bookName, onChanged) {
    let readlists = [];
    try {
      const page = await api('/api/v1/readlists?unpaged=true&sort=name,asc');
      readlists = (page && page.content) || [];
    } catch (err) {
      toast(err.message, 'error');
      return;
    }
    const initialSet = new Set();
    readlists.forEach((rl) => {
      if ((rl.bookIds || []).includes(bookID)) initialSet.add(rl.id);
    });

    const renderListHTML = () => {
      if (!readlists.length) {
        return `<div class="empty-box" style="padding:18px">还没有阅读列表，请在下方输入名称快速创建并加入。</div>`;
      }
      return `<div class="org-picker-list">${readlists.map((rl) => `
        <label class="org-check-item">
          <input type="checkbox" data-pick-rl="${esc(rl.id)}" ${(rl.bookIds || []).includes(bookID) ? 'checked' : ''}>
          <span class="org-check-name">${esc(rl.name)}</span>
          <span class="badge">${(rl.bookIds || []).length} 册</span>
        </label>
      `).join('')}</div>`;
    };

    openModal({
      title: `加入阅读列表 · ${bookName}`,
      maxWidth: '520px',
      bodyHTML: `
        <div style="display:grid;gap:14px">
          <div id="org-rl-picker-wrap">${renderListHTML()}</div>
          <form id="org-rl-quick-create" class="org-quick-create">
            <input type="text" id="org-rl-quick-name" placeholder="输入新阅读列表名称并直接加入…">
            <button type="submit" class="btn btn-sm btn-primary">新建并加入</button>
          </form>
        </div>
      `,
      footHTML: `
        <button type="button" class="btn" id="org-rl-pick-cancel">关闭</button>
        <button type="button" class="btn btn-primary" id="org-rl-pick-save">保存更改</button>
      `,
      onMount: (body) => {
        document.getElementById('org-rl-pick-cancel').onclick = closeModal;
        document.getElementById('org-rl-quick-create').onsubmit = async (e) => {
          e.preventDefault();
          const input = document.getElementById('org-rl-quick-name');
          const name = input.value.trim();
          if (!name) return;
          try {
            const created = await api('/api/v1/readlists', {
              method: 'POST',
              body: { name, summary: '', ordered: true, bookIds: [bookID] },
            });
            readlists.push(created);
            initialSet.add(created.id);
            input.value = '';
            document.getElementById('org-rl-picker-wrap').innerHTML = renderListHTML();
            toast(`已创建并加入「${created.name}」`);
            if (onChanged) onChanged();
          } catch (err) { toast(err.message, 'error'); }
        };
        document.getElementById('org-rl-pick-save').onclick = async () => {
          const btn = document.getElementById('org-rl-pick-save');
          btn.disabled = true;
          try {
            const boxes = Array.from(body.querySelectorAll('input[data-pick-rl]'));
            let changed = 0;
            for (const box of boxes) {
              const rid = box.getAttribute('data-pick-rl');
              const checked = box.checked;
              const wasIn = initialSet.has(rid);
              if (checked === wasIn) continue;
              const rl = readlists.find((x) => x.id === rid);
              if (!rl) continue;
              const nextIDs = checked
                ? [...(rl.bookIds || []), bookID]
                : (rl.bookIds || []).filter((x) => x !== bookID);
              await api(`/api/v1/readlists/${encodeURIComponent(rid)}`, {
                method: 'PATCH',
                body: { bookIds: nextIDs },
              });
              changed++;
            }
            if (changed > 0) toast('阅读列表已更新');
            closeModal();
            if (onChanged) onChanged();
          } catch (err) {
            toast(err.message, 'error');
            btn.disabled = false;
          }
        };
      },
    });
  }

  async function renderCollections() {
    const seq = routeSeq;
    document.title = '收藏夹 · MangaAlpha';
    const sp = new URLSearchParams(location.search);
    const search = (sp.get('search') || '').trim();
    renderShell('collections', `<div class="boot-splash" style="min-height:40vh"><div class="spinner"></div><p>正在加载收藏夹…</p></div>`);
    try {
      const qs = new URLSearchParams({ size: '100', sort: 'name,asc' });
      if (search) qs.set('search', search);
      const page = await api(`/api/v1/collections?${qs.toString()}`);
      if (seq !== routeSeq) return;
      const items = (page && page.content) || [];
      const content = document.getElementById('page-content');
      if (!content) return;
      content.innerHTML = `
        <section class="section">
          <div class="section-head" style="flex-wrap:wrap;gap:10px">
            <h2>收藏夹（${page ? page.totalElements : items.length}）</h2>
            <div class="org-toolbar">
              <form id="collections-filter-form" class="org-search-form">
                <input type="text" id="collections-search-input" value="${esc(search)}" placeholder="筛选收藏夹…">
                <button type="submit" class="btn btn-sm">搜索</button>
              </form>
              <button type="button" class="btn btn-primary btn-sm" id="btn-create-collection">+ 新建收藏夹</button>
            </div>
          </div>
          ${items.length ? `
            <div class="poster-grid">
              ${items.map((c) => `
                <a href="/collections/${encodeURIComponent(c.id)}" data-nav class="poster-card">
                  <div class="poster-thumb">
                    <img loading="lazy" src="/api/v1/collections/${encodeURIComponent(c.id)}/thumbnail?v=${state.coverVer}" alt="${esc(c.name)}">
                    ${c.ordered ? `<span class="poster-badge-top-left">手动排序</span>` : ''}
                    <span class="poster-badge-top-right">${(c.seriesIds || []).length} 部</span>
                  </div>
                  <div class="poster-body">
                    <div class="poster-title" title="${esc(c.name)}">${esc(c.name)}</div>
                    <div class="poster-sub">共 ${(c.seriesIds || []).length} 部作品${c.filtered ? ' · 含已隐藏项' : ''}</div>
                  </div>
                </a>
              `).join('')}
            </div>
          ` : `
            <div class="empty-box">
              <p style="margin:0 0 12px">${search ? '没有找到匹配的收藏夹。' : '还没有创建任何收藏夹。你可以在这里创建，或从任意漫画详情页快速加入，与 Koharia 实时同步。'}</p>
              <button type="button" class="btn btn-primary btn-sm" id="btn-empty-create-col">+ 创建收藏夹</button>
            </div>
          `}
        </section>
      `;
      document.getElementById('collections-filter-form').onsubmit = (e) => {
        e.preventDefault();
        const val = document.getElementById('collections-search-input').value.trim();
        navigate(val ? `/collections?search=${encodeURIComponent(val)}` : '/collections');
      };
      const openCreate = () => openCollectionFormModal(null, () => renderCollections());
      document.getElementById('btn-create-collection').onclick = openCreate;
      const emptyBtn = document.getElementById('btn-empty-create-col');
      if (emptyBtn) emptyBtn.onclick = openCreate;
    } catch (err) {
      if (seq !== routeSeq) return;
      toast(err.message, 'error');
    }
  }

  async function renderCollectionDetail(collectionID) {
    const seq = routeSeq;
    renderShell('collections', `<div class="boot-splash" style="min-height:40vh"><div class="spinner"></div><p>正在加载收藏夹…</p></div>`);
    try {
      const [col, seriesPage] = await Promise.all([
        api(`/api/v1/collections/${encodeURIComponent(collectionID)}`),
        api(`/api/v1/collections/${encodeURIComponent(collectionID)}/series?unpaged=true`),
        ensureLibrariesLoaded(),
      ]);
      if (seq !== routeSeq) return;
      const items = (seriesPage && seriesPage.content) || [];
      document.title = `${col.name} · 收藏夹 · MangaAlpha`;
      const content = document.getElementById('page-content');
      if (!content) return;
      content.innerHTML = `
        <div style="margin-bottom:12px;font-size:13px;color:var(--text-muted)">
          <a href="/collections" data-nav style="color:var(--accent)">← 返回收藏夹列表</a>
          <span> / </span>
          <span>${esc(col.name)}</span>
        </div>
        <section class="card org-header-card">
          <div class="org-header-cover">
            <img src="/api/v1/collections/${encodeURIComponent(col.id)}/thumbnail?v=${state.coverVer}" alt="${esc(col.name)}">
          </div>
          <div class="org-header-main">
            <div class="detail-badges">
              <span class="badge badge-accent">收藏夹</span>
              <span class="badge">共 ${items.length} 部可见作品</span>
              <span class="badge">${col.ordered ? '手动排序' : '按书名排序'}</span>
              ${col.filtered ? `<span class="badge badge-warn">含隐藏/NSFW 过滤项</span>` : ''}
            </div>
            <h1 style="margin:6px 0 10px;font-size:22px">${esc(col.name)}</h1>
            <div class="detail-cta">
              <button type="button" class="btn btn-sm" id="btn-edit-col">编辑收藏夹</button>
              <button type="button" class="btn btn-sm btn-danger" id="btn-delete-col">删除收藏夹</button>
            </div>
          </div>
        </section>
        <section class="section" style="margin-top:18px">
          ${items.length ? `
            <div class="poster-grid">
              ${items.map((s, idx) => {
                const meta = s.metadata || {};
                const title = meta.title || s.name || '未命名';
                const total = Number(s.booksCount || 0);
                const unread = Number(s.booksUnreadCount || 0);
                const read = Number(s.booksReadCount || 0);
                const pct = total > 0 ? Math.min(100, Math.round((read / total) * 100)) : 0;
                return `
                  <div class="poster-card org-member-card">
                    <a href="/series/${encodeURIComponent(s.id)}" data-nav style="display:flex;flex-direction:column;flex:1">
                      <div class="poster-thumb">
                        <img loading="lazy" src="/api/v1/series/${encodeURIComponent(s.id)}/thumbnail?v=${state.coverVer}" alt="${esc(title)}">
                        ${col.ordered ? `<span class="poster-badge-top-left">#${idx + 1}</span>` : ''}
                        ${unread > 0 ? `<span class="poster-badge-top-right">${unread}</span>` : ''}
                        ${read > 0 ? `<div class="poster-progress-bar"><span style="width:${pct}%"></span></div>` : ''}
                      </div>
                      <div class="poster-body">
                        <div class="poster-title" title="${esc(title)}">${esc(title)}</div>
                        <div class="poster-sub">${s.oneshot ? '单本' : `共 ${total} 册`}</div>
                        ${librarySourceText(s.libraryId) ? `<div class="poster-source" title="${esc(librarySourceText(s.libraryId))}">${esc(librarySourceText(s.libraryId))}</div>` : ''}
                      </div>
                    </a>
                    <div class="org-card-actions">
                      ${col.ordered ? `
                        <button type="button" class="btn btn-sm" data-col-move="${idx}" data-dir="-1" ${idx === 0 ? 'disabled' : ''} title="前移">↑</button>
                        <button type="button" class="btn btn-sm" data-col-move="${idx}" data-dir="1" ${idx === items.length - 1 ? 'disabled' : ''} title="后移">↓</button>
                      ` : ''}
                      <button type="button" class="btn btn-sm" data-col-remove="${esc(s.id)}">移除</button>
                    </div>
                  </div>
                `;
              }).join('')}
            </div>
          ` : `<div class="empty-box">该收藏夹暂无可见作品。请前往任意漫画详情页点击「+ 收藏夹」添加。</div>`}
        </section>
      `;

      document.getElementById('btn-edit-col').onclick = () => openCollectionFormModal(col, () => renderCollectionDetail(collectionID));
      const deleteColBtn = document.getElementById('btn-delete-col');
      deleteColBtn.onclick = async () => {
        if (!(await confirmModal(`确定删除收藏夹「${col.name}」？这不会删除其中的漫画文件。`, { title: '删除收藏夹', confirmText: '确认删除' }))) return;
        if (!deleteColBtn.isConnected) return;
        try {
          await api(`/api/v1/collections/${encodeURIComponent(collectionID)}`, { method: 'DELETE' });
          toast('收藏夹已删除');
          navigate('/collections', true);
        } catch (err) { toast(err.message, 'error'); }
      };

      const visibleIDs = items.map((s) => s.id);
      const saveSeriesOrder = async (nextIDs) => {
        try {
          await api(`/api/v1/collections/${encodeURIComponent(collectionID)}`, {
            method: 'PATCH',
            body: { seriesIds: nextIDs },
          });
          renderCollectionDetail(collectionID);
        } catch (err) { toast(err.message, 'error'); }
      };
      content.querySelectorAll('button[data-col-move]').forEach((btn) => {
        btn.onclick = () => {
          const i = Number(btn.getAttribute('data-col-move'));
          const d = Number(btn.getAttribute('data-dir'));
          const j = i + d;
          if (j < 0 || j >= visibleIDs.length) return;
          const next = visibleIDs.slice();
          [next[i], next[j]] = [next[j], next[i]];
          saveSeriesOrder(next);
        };
      });
      content.querySelectorAll('button[data-col-remove]').forEach((btn) => {
        btn.onclick = () => {
          const sid = btn.getAttribute('data-col-remove');
          saveSeriesOrder(visibleIDs.filter((id) => id !== sid));
        };
      });
    } catch (err) {
      if (seq !== routeSeq) return;
      toast(err.message, 'error');
    }
  }

  async function renderReadLists() {
    const seq = routeSeq;
    document.title = '阅读列表 · MangaAlpha';
    const sp = new URLSearchParams(location.search);
    const search = (sp.get('search') || '').trim();
    renderShell('readlists', `<div class="boot-splash" style="min-height:40vh"><div class="spinner"></div><p>正在加载阅读列表…</p></div>`);
    try {
      const qs = new URLSearchParams({ size: '100', sort: 'name,asc' });
      if (search) qs.set('search', search);
      const page = await api(`/api/v1/readlists?${qs.toString()}`);
      if (seq !== routeSeq) return;
      const items = (page && page.content) || [];
      const content = document.getElementById('page-content');
      if (!content) return;
      content.innerHTML = `
        <section class="section">
          <div class="section-head" style="flex-wrap:wrap;gap:10px">
            <h2>阅读列表（${page ? page.totalElements : items.length}）</h2>
            <div class="org-toolbar">
              <form id="readlists-filter-form" class="org-search-form">
                <input type="text" id="readlists-search-input" value="${esc(search)}" placeholder="筛选阅读列表…">
                <button type="submit" class="btn btn-sm">搜索</button>
              </form>
              <button type="button" class="btn btn-primary btn-sm" id="btn-create-readlist">+ 新建阅读列表</button>
            </div>
          </div>
          ${items.length ? `
            <div class="poster-grid">
              ${items.map((rl) => `
                <a href="/readlists/${encodeURIComponent(rl.id)}" data-nav class="poster-card">
                  <div class="poster-thumb">
                    <img loading="lazy" src="/api/v1/readlists/${encodeURIComponent(rl.id)}/thumbnail?v=${state.coverVer}" alt="${esc(rl.name)}">
                    ${rl.ordered ? `<span class="poster-badge-top-left">有序</span>` : ''}
                    <span class="poster-badge-top-right">${(rl.bookIds || []).length} 册</span>
                  </div>
                  <div class="poster-body">
                    <div class="poster-title" title="${esc(rl.name)}">${esc(rl.name)}</div>
                    <div class="poster-sub" title="${esc(rl.summary || '')}">${esc(rl.summary || `共 ${(rl.bookIds || []).length} 册`)}${rl.filtered ? ' · 含已隐藏项' : ''}</div>
                  </div>
                </a>
              `).join('')}
            </div>
          ` : `
            <div class="empty-box">
              <p style="margin:0 0 12px">${search ? '没有找到匹配的阅读列表。' : '还没有创建任何阅读列表。你可以在这里创建，或将不同作品的单卷加入列表，与 Koharia 实时同步。'}</p>
              <button type="button" class="btn btn-primary btn-sm" id="btn-empty-create-rl">+ 创建阅读列表</button>
            </div>
          `}
        </section>
      `;
      document.getElementById('readlists-filter-form').onsubmit = (e) => {
        e.preventDefault();
        const val = document.getElementById('readlists-search-input').value.trim();
        navigate(val ? `/readlists?search=${encodeURIComponent(val)}` : '/readlists');
      };
      const openCreate = () => openReadListFormModal(null, () => renderReadLists());
      document.getElementById('btn-create-readlist').onclick = openCreate;
      const emptyBtn = document.getElementById('btn-empty-create-rl');
      if (emptyBtn) emptyBtn.onclick = openCreate;
    } catch (err) {
      if (seq !== routeSeq) return;
      toast(err.message, 'error');
    }
  }

  async function renderReadListDetail(readListID) {
    const seq = routeSeq;
    renderShell('readlists', `<div class="boot-splash" style="min-height:40vh"><div class="spinner"></div><p>正在加载阅读列表…</p></div>`);
    try {
      const [rl, booksPage] = await Promise.all([
        api(`/api/v1/readlists/${encodeURIComponent(readListID)}`),
        api(`/api/v1/readlists/${encodeURIComponent(readListID)}/books?unpaged=true`),
        ensureLibrariesLoaded(),
      ]);
      if (seq !== routeSeq) return;
      const books = (booksPage && booksPage.content) || [];
      document.title = `${rl.name} · 阅读列表 · MangaAlpha`;
      const content = document.getElementById('page-content');
      if (!content) return;
      content.innerHTML = `
        <div style="margin-bottom:12px;font-size:13px;color:var(--text-muted)">
          <a href="/readlists" data-nav style="color:var(--accent)">← 返回阅读列表</a>
          <span> / </span>
          <span>${esc(rl.name)}</span>
        </div>
        <section class="card org-header-card">
          <div class="org-header-cover">
            <img src="/api/v1/readlists/${encodeURIComponent(rl.id)}/thumbnail?v=${state.coverVer}" alt="${esc(rl.name)}">
          </div>
          <div class="org-header-main">
            <div class="detail-badges">
              <span class="badge badge-accent">阅读列表</span>
              <span class="badge">共 ${books.length} 册可见单卷</span>
              <span class="badge">${rl.ordered ? '手动排序' : '按文件名排序'}</span>
              ${rl.filtered ? `<span class="badge badge-warn">含隐藏/NSFW 过滤项</span>` : ''}
            </div>
            <h1 style="margin:6px 0 8px;font-size:22px">${esc(rl.name)}</h1>
            ${rl.summary ? `<p class="detail-summary" style="margin:0 0 12px">${esc(rl.summary)}</p>` : ''}
            <div class="detail-cta">
              <button type="button" class="btn btn-sm" id="btn-edit-rl">编辑阅读列表</button>
              <button type="button" class="btn btn-sm btn-danger" id="btn-delete-rl">删除阅读列表</button>
            </div>
          </div>
        </section>
        <section class="card" style="margin-top:18px">
          <div class="section-head" style="margin-bottom:12px">
            <h2>包含单册（${books.length}）</h2>
          </div>
          ${books.length ? `
            <div class="volume-list">
              ${books.map((b, idx) => {
                const rp = b.readProgress;
                const pgCount = (b.media && b.media.pagesCount) || 0;
                const startPg = rp && !rp.completed && rp.page > 0 ? rp.page : 1;
                let stateBadge = `<span class="badge">未读</span>`;
                if (rp && rp.completed) stateBadge = `<span class="badge badge-success">已读</span>`;
                else if (rp && rp.page > 0) stateBadge = `<span class="badge badge-accent">第 ${rp.page}${pgCount > 0 ? ` / ${pgCount}` : ''} 页</span>`;
                return `
                  <div class="volume-row">
                    <div class="volume-info">
                      <div class="volume-title">
                        ${rl.ordered ? `<span class="badge" style="margin-right:6px">#${idx + 1}</span>` : ''}
                        <a href="/book/${encodeURIComponent(b.id)}" data-nav>${esc(b.name)}</a>
                      </div>
                      <div class="volume-meta">
                        ${stateBadge}
                        ${librarySourceText(b.libraryId) ? `<span class="badge">${esc(librarySourceText(b.libraryId))}</span>` : ''}
                        <a href="/series/${encodeURIComponent(b.seriesId)}" data-nav style="color:var(--accent)">${esc(b.seriesTitle || '查看系列')}</a>
                        <span>${esc(b.size || '')}</span>
                      </div>
                    </div>
                    <div class="volume-actions">
                      <a href="/reader/series/${encodeURIComponent(b.seriesId)}/book/${encodeURIComponent(b.id)}?page=${startPg}" class="btn btn-primary btn-sm">${rp && !rp.completed && rp.page > 0 ? '继续读' : '阅读'}</a>
                      ${rl.ordered ? `
                        <button type="button" class="btn btn-sm" data-rl-move="${idx}" data-dir="-1" ${idx === 0 ? 'disabled' : ''} title="上移">↑</button>
                        <button type="button" class="btn btn-sm" data-rl-move="${idx}" data-dir="1" ${idx === books.length - 1 ? 'disabled' : ''} title="下移">↓</button>
                      ` : ''}
                      <button type="button" class="btn btn-sm" data-rl-remove="${esc(b.id)}">移除</button>
                    </div>
                  </div>
                `;
              }).join('')}
            </div>
          ` : `<div class="empty-box">该阅读列表暂无可见单册。请前往漫画详情页或单册页点击「+ 列表 / + 阅读列表」添加。</div>`}
        </section>
      `;

      document.getElementById('btn-edit-rl').onclick = () => openReadListFormModal(rl, () => renderReadListDetail(readListID));
      const deleteRlBtn = document.getElementById('btn-delete-rl');
      deleteRlBtn.onclick = async () => {
        if (!(await confirmModal(`确定删除阅读列表「${rl.name}」？这不会删除其中的漫画文件。`, { title: '删除阅读列表', confirmText: '确认删除' }))) return;
        if (!deleteRlBtn.isConnected) return;
        try {
          await api(`/api/v1/readlists/${encodeURIComponent(readListID)}`, { method: 'DELETE' });
          toast('阅读列表已删除');
          navigate('/readlists', true);
        } catch (err) { toast(err.message, 'error'); }
      };

      const visibleIDs = books.map((b) => b.id);
      const saveBookOrder = async (nextIDs) => {
        try {
          await api(`/api/v1/readlists/${encodeURIComponent(readListID)}`, {
            method: 'PATCH',
            body: { bookIds: nextIDs },
          });
          renderReadListDetail(readListID);
        } catch (err) { toast(err.message, 'error'); }
      };
      content.querySelectorAll('button[data-rl-move]').forEach((btn) => {
        btn.onclick = () => {
          const i = Number(btn.getAttribute('data-rl-move'));
          const d = Number(btn.getAttribute('data-dir'));
          const j = i + d;
          if (j < 0 || j >= visibleIDs.length) return;
          const next = visibleIDs.slice();
          [next[i], next[j]] = [next[j], next[i]];
          saveBookOrder(next);
        };
      });
      content.querySelectorAll('button[data-rl-remove]').forEach((btn) => {
        btn.onclick = () => {
          const bid = btn.getAttribute('data-rl-remove');
          saveBookOrder(visibleIDs.filter((id) => id !== bid));
        };
      });
    } catch (err) {
      if (seq !== routeSeq) return;
      toast(err.message, 'error');
    }
  }

  // --- Reading History Timeline (/history) ---

  function historyTimestampMs(book) {
    const rp = book && book.readProgress;
    const raw = (rp && (rp.readDate || rp.lastModified || rp.created)) || '';
    if (!raw) return 0;
    const ms = Date.parse(raw);
    return Number.isNaN(ms) ? 0 : ms;
  }

  function historyDayKey(ms) {
    if (!ms) return 'unknown';
    const d = new Date(ms);
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    return `${y}-${m}-${day}`;
  }

  function historyDayLabel(dayKey) {
    if (!dayKey || dayKey === 'unknown') return '更早';
    const parts = dayKey.split('-').map(Number);
    if (parts.length !== 3) return dayKey;
    const target = new Date(parts[0], parts[1] - 1, parts[2]);
    const now = new Date();
    const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    const diffDays = Math.round((today.getTime() - target.getTime()) / 86400000);
    if (diffDays === 0) return '今天';
    if (diffDays === 1) return '1 天前';
    if (diffDays >= 2 && diffDays <= 6) return `${diffDays} 天前`;
    return dayKey;
  }

  function historyTimeText(ms) {
    if (!ms) return '';
    try {
      return new Date(ms).toLocaleTimeString('zh-CN', { hour: 'numeric', minute: '2-digit', hour12: true });
    } catch (_) {
      const d = new Date(ms);
      return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
    }
  }

  function groupHistoryTimeline(books) {
    const dayOrder = [];
    const dayMap = new Map();
    for (const b of books) {
      const ms = historyTimestampMs(b);
      const dayKey = historyDayKey(ms);
      if (!dayMap.has(dayKey)) {
        const dayObj = { dayKey, label: historyDayLabel(dayKey), seriesOrder: [], seriesMap: new Map() };
        dayMap.set(dayKey, dayObj);
        dayOrder.push(dayObj);
      }
      const dayObj = dayMap.get(dayKey);
      const sid = b.seriesId || b.id;
      if (!dayObj.seriesMap.has(sid)) {
        const entry = { key: `${dayKey}::${sid}`, seriesId: sid, primary: b, others: [] };
        dayObj.seriesMap.set(sid, entry);
        dayObj.seriesOrder.push(entry);
      } else {
        dayObj.seriesMap.get(sid).others.push(b);
      }
    }
    return dayOrder.map((d) => ({
      dayKey: d.dayKey,
      label: d.label,
      entries: d.seriesOrder,
    }));
  }

  function renderHistoryEntryHTML(entry) {
    const b = entry.primary;
    const rp = b.readProgress || {};
    const pg = Number(rp.page) || 1;
    const totalPg = Number((b.media && b.media.pagesCount) || 0);
    const completed = Boolean(rp.completed);
    const readerPage = completed ? 1 : pg;
    const readerURL = `/reader/series/${encodeURIComponent(b.seriesId)}/book/${encodeURIComponent(b.id)}?page=${readerPage}`;
    const seriesURL = `/series/${encodeURIComponent(b.seriesId)}`;
    const seriesTitle = b.seriesTitle || b.name || '未命名';
    const bookLabel = (b.metadata && b.metadata.title) ? b.metadata.title : b.name;
    const timeStr = historyTimeText(historyTimestampMs(b));
    const sourceText = librarySourceText(b.libraryId);
    const statusBadge = completed
      ? `<span class="badge badge-success">已读完</span>`
      : `<span class="badge badge-accent">第 ${pg}${totalPg > 0 ? ` / ${totalPg}` : ''} 页</span>`;

    const subHTML = entry.others.length > 0 ? `
      <div class="history-entry-footer">
        <button type="button" class="history-more-btn" data-hist-toggle="${esc(entry.key)}" data-hist-count="${entry.others.length}" aria-expanded="false">
          ▸ 当天另有 ${entry.others.length} 卷
        </button>
        <a href="${seriesURL}" data-nav style="font-size:12px;color:var(--text-muted)">作品详情 →</a>
      </div>
      <div class="history-sub-list" data-hist-children="${esc(entry.key)}" hidden>
        ${entry.others.map((ob) => {
          const orp = ob.readProgress || {};
          const opg = Number(orp.page) || 1;
          const ototal = Number((ob.media && ob.media.pagesCount) || 0);
          const odone = Boolean(orp.completed);
          const oUrl = `/reader/series/${encodeURIComponent(ob.seriesId)}/book/${encodeURIComponent(ob.id)}?page=${odone ? 1 : opg}`;
          const oName = (ob.metadata && ob.metadata.title) ? ob.metadata.title : ob.name;
          const oTime = historyTimeText(historyTimestampMs(ob));
          return `
            <a class="history-sub-item" href="${oUrl}">
              <span class="history-sub-name" title="${esc(oName)}">${esc(oName)}</span>
              <span class="history-sub-meta">
                ${odone ? `<span class="badge badge-success">已读完</span>` : `<span class="badge badge-accent">第 ${opg}${ototal > 0 ? ` / ${ototal}` : ''} 页</span>`}
                ${oTime ? `<span>${esc(oTime)}</span>` : ''}
              </span>
            </a>`;
        }).join('')}
      </div>` : '';

    return `
      <article class="history-entry">
        <div class="history-row">
          <a class="history-cover" href="${seriesURL}" data-nav title="查看作品详情：${esc(seriesTitle)}">
            <img loading="lazy" src="/api/v1/series/${encodeURIComponent(b.seriesId)}/thumbnail?v=${state.coverVer}" alt="${esc(seriesTitle)}">
          </a>
          <a class="history-main" href="${readerURL}" title="打开阅读器：${esc(bookLabel)}">
            <div class="history-title">${esc(seriesTitle)}</div>
            <div class="history-sub">
              <span class="history-vol" title="${esc(bookLabel)}">${esc(bookLabel)}</span>
              ${statusBadge}
              ${timeStr ? `<span class="history-time">${esc(timeStr)}</span>` : ''}
            </div>
            ${sourceText ? `<div class="history-source">${esc(sourceText)}</div>` : ''}
          </a>
        </div>
        ${subHTML}
      </article>`;
  }

  async function renderHistory() {
    const seq = routeSeq;
    document.title = '阅读历史 · MangaAlpha';
    const sp = new URLSearchParams(location.search);
    const search = (sp.get('search') || '').trim();
    renderShell('history', `<div class="boot-splash" style="min-height:40vh"><div class="spinner"></div><p>正在加载阅读历史…</p></div>`);

    const PAGE_SIZE = 80;
    const loadedBooks = [];
    const seenBookIDs = new Set();
    let nextPage = 0;
    let totalPages = 1;
    let totalElements = 0;
    let loadingMore = false;

    const fetchBatch = async (minAdditionalGroups = 15) => {
      let loops = 0;
      const beforeGroups = groupHistoryTimeline(loadedBooks).reduce((acc, g) => acc + g.entries.length, 0);
      while (nextPage < totalPages && loops < 4) {
        loops++;
        const qs = new URLSearchParams();
        qs.append('read_status', 'IN_PROGRESS');
        qs.append('read_status', 'READ');
        qs.set('sort', 'readProgress.readDate,desc');
        qs.set('page', String(nextPage));
        qs.set('size', String(PAGE_SIZE));
        if (search) qs.set('search', search);
        const page = await api(`/api/v1/books?${qs.toString()}`);
        if (seq !== routeSeq) return false;
        const rows = (page && page.content) || [];
        totalElements = Number((page && page.totalElements) || 0);
        totalPages = Number((page && page.totalPages) || 0);
        nextPage++;
        for (const r of rows) {
          if (!r || !r.id || seenBookIDs.has(r.id)) continue;
          seenBookIDs.add(r.id);
          loadedBooks.push(r);
        }
        const curGroups = groupHistoryTimeline(loadedBooks).reduce((acc, g) => acc + g.entries.length, 0);
        if (curGroups - beforeGroups >= minAdditionalGroups || rows.length === 0) break;
      }
      return true;
    };

    const paint = () => {
      const content = document.getElementById('page-content');
      if (!content || seq !== routeSeq) return;
      const days = groupHistoryTimeline(loadedBooks);
      const hasMore = nextPage < totalPages;
      content.innerHTML = `
        <section class="section">
          <div class="section-head">
            <h2>历史${totalElements > 0 ? `（${totalElements.toLocaleString()} 卷记录）` : ''}</h2>
            <div class="org-toolbar history-toolbar">
              <form id="history-filter-form" class="org-search-form">
                <input type="text" id="history-search-input" value="${esc(search)}" placeholder="搜索历史漫画或卷名…">
                <button type="submit" class="btn btn-sm">搜索</button>
                <button type="button" class="btn btn-sm" id="btn-history-refresh">刷新</button>
              </form>
            </div>
          </div>
          ${days.length ? `
            <div class="history-timeline">
              ${days.map((day) => `
                <section class="history-day-group">
                  <div class="history-day-head">${esc(day.label)}</div>
                  <div class="history-list">
                    ${day.entries.map((entry) => renderHistoryEntryHTML(entry)).join('')}
                  </div>
                </section>
              `).join('')}
            </div>
            ${hasMore ? `
              <div class="pagination">
                <button type="button" class="btn" id="btn-history-more" ${loadingMore ? 'disabled' : ''}>${loadingMore ? '加载中…' : '加载更多历史'}</button>
              </div>` : ''}
          ` : `
            <div class="empty-box">
              <p style="margin:0">${search ? '没有找到匹配的阅读历史记录。' : '还没有阅读历史记录。打开任意漫画开始阅读后，这里会按时间线记录你的阅读足迹。'}</p>
            </div>
          `}
        </section>
      `;

      document.getElementById('history-filter-form').onsubmit = (e) => {
        e.preventDefault();
        const val = document.getElementById('history-search-input').value.trim();
        navigate(val ? `/history?search=${encodeURIComponent(val)}` : '/history');
      };
      document.getElementById('btn-history-refresh').onclick = () => renderHistory();
      content.querySelectorAll('button[data-hist-toggle]').forEach((btn) => {
        btn.onclick = () => {
          const key = btn.getAttribute('data-hist-toggle');
          const count = btn.getAttribute('data-hist-count') || '0';
          const list = content.querySelector(`[data-hist-children="${CSS.escape(key)}"]`);
          if (!list) return;
          const expanded = list.hasAttribute('hidden');
          if (expanded) list.removeAttribute('hidden');
          else list.setAttribute('hidden', '');
          btn.setAttribute('aria-expanded', expanded ? 'true' : 'false');
          btn.textContent = expanded ? `▾ 收起当天其余 ${count} 卷` : `▸ 当天另有 ${count} 卷`;
        };
      });
      const moreBtn = document.getElementById('btn-history-more');
      if (moreBtn) {
        moreBtn.onclick = async () => {
          if (loadingMore) return;
          loadingMore = true;
          moreBtn.disabled = true;
          moreBtn.textContent = '加载中…';
          try {
            const ok = await fetchBatch(15);
            if (!ok || seq !== routeSeq) return;
            loadingMore = false;
            paint();
          } catch (err) {
            loadingMore = false;
            toast(err.message, 'error');
            paint();
          }
        };
      }
    };

    try {
      await Promise.all([ensureLibrariesLoaded(), fetchBatch(15)]);
      if (seq !== routeSeq) return;
      paint();
    } catch (err) {
      if (seq !== routeSeq) return;
      toast(err.message, 'error');
    }
  }

  // --- Personal Account Settings Page (/account) ---

  async function renderAccountPage() {
    const seq = routeSeq;
    document.title = '账号设置 · MangaAlpha';
    renderShell('account', `<div class="boot-splash" style="min-height:40vh"><div class="spinner"></div><p>正在加载账号设置…</p></div>`);

    try {
      const [me, keys, sessions, tracker] = await Promise.all([
        api('/admin/api/me'),
        api('/admin/api/auth/api-keys'),
        api('/admin/api/auth/sessions'),
        api('/admin/api/bangumi/tracker?limit=20').catch(() => null),
      ]);
      if (seq !== routeSeq) return;
      const content = document.getElementById('page-content');
      if (!content) return;

      const user = (me && me.user) || {};
      const visibleLibs = (me && Array.isArray(me.visibleLibraries)) ? me.visibleLibraries : [];
      const clients = (me && me.clients) || {};
      const komgaURL = clients.komgaUrl || location.origin;
      const koreaderURL = clients.koreaderOpdsUrl || `${location.origin}/api/opds/v1.2`;
      const komgaOpdsURL = clients.komgaOpdsUrl || `${location.origin}/opds/v1.2/catalog`;
      const tr = tracker || {};
      const trRecords = Array.isArray(tr.records) ? tr.records : [];
      const isAdmin = Boolean(me && me.isAdmin);
      const presetKey = (new URLSearchParams(location.search).get('presetKey') || '').trim();

      content.innerHTML = `
        <div class="section-head" style="margin-bottom:14px">
          <div>
            <h2 style="margin:0">账号设置</h2>
            <div style="font-size:12.5px;color:var(--text-secondary);margin-top:2px">以下设置仅影响当前登录账号，不会修改其他用户或全服配置。</div>
          </div>
          ${isAdmin ? `<a href="/admin" data-nav class="btn btn-sm">前往管理后台 →</a>` : ''}
        </div>

        <div class="grid-2">
          <div class="card">
            <h3>我的账号与权限</h3>
            <div style="display:grid;gap:8px;font-size:13px;color:var(--text-secondary)">
              <div>用户名：<strong style="color:var(--text)">${esc(user.username || state.auth.username)}</strong>
                <span class="badge ${isAdmin ? 'badge-accent' : ''}" style="margin-left:6px">${isAdmin ? '管理员' : '普通用户'}</span>
              </div>
              <div>可访问书库：
                ${visibleLibs.length === 0
                  ? `<span class="badge">暂无可见书库</span>`
                  : visibleLibs.map((l) => `<a href="/libraries/${encodeURIComponent(l.id)}" data-nav class="badge">${esc(l.name)}</a>`).join(' ')}
              </div>
              ${user.allowNsfw ? `
                <div style="margin-top:6px;padding:10px 12px;border-radius:8px;background:var(--bg-input);border:1px solid var(--border)">
                  <label class="check-row" style="font-size:13px;font-weight:600;color:var(--text)">
                    <input type="checkbox" id="acc-show-nsfw" ${user.showNsfw ? 'checked' : ''}>
                    <span>在书架、Komga 客户端与 OPDS 中显示 NSFW 作品</span>
                  </label>
                  <div style="font-size:12px;color:var(--text-muted);margin-top:4px">此开关仅影响当前账号视图，不会影响其他用户。</div>
                </div>` : ''}
            </div>
          </div>

          <form class="card" id="acc-pw-form">
            <h3>${isAdmin ? '修改管理员用户名 / 密码' : '修改登录密码'}</h3>
            <p style="font-size:12.5px;color:var(--text-secondary);margin-top:0">
              当前用户名：<code>${esc(user.username || state.auth.username)}</code>${isAdmin ? '（可仅改用户名、仅改密码或同时修改）' : '（用户名由管理员统一维护，不可自助修改）'}
            </p>
            <div class="field">
              <label>当前密码</label>
              <input type="password" id="acc-cur-pw" autocomplete="current-password" required>
            </div>
            ${isAdmin ? `
              <div class="field">
                <label>新用户名（留空保持 <code>${esc(user.username || state.auth.username)}</code>）</label>
                <input type="text" id="acc-new-user" autocomplete="username" placeholder="${esc(user.username || state.auth.username)}">
              </div>` : ''}
            <div class="field">
              <label>新密码（${isAdmin ? '留空不改密码，修改需至少 8 位' : '至少 8 位'}）</label>
              <input type="password" id="acc-new-pw" autocomplete="new-password" ${isAdmin ? '' : 'required'} minlength="8">
            </div>
            <div class="field">
              <label>确认新密码</label>
              <input type="password" id="acc-new-pw2" autocomplete="new-password" ${isAdmin ? '' : 'required'} minlength="8">
            </div>
            <button type="submit" class="btn btn-primary">${isAdmin ? '更新凭据（自动注销其他设备会话）' : '保存新密码（自动注销其他设备会话）'}</button>
          </form>
        </div>

        ${displayPrefCardHTML()}

        <div class="grid-2">
          <div class="card">
            <h3>外部阅读客户端接入地址</h3>
            <p style="font-size:12.5px;color:var(--text-secondary);margin-top:0">使用当前账号的用户名与密码（或右侧生成的个人 API Key）即可登录以下客户端，阅读进度与收藏夹与网页端实时同步：</p>
            <div style="display:grid;gap:10px">
              <div>
                <div style="font-size:12px;color:var(--text-secondary);margin-bottom:4px"><strong>Komga 客户端</strong>（Koharia / Mihon 等）</div>
                <div style="display:flex;gap:6px;align-items:center">
                  <div class="code-box" style="flex:1;margin:0">${esc(komgaURL)}</div>
                  <button type="button" class="btn btn-sm" data-acc-copy="${esc(komgaURL)}">复制</button>
                </div>
              </div>
              <div>
                <div style="font-size:12px;color:var(--text-secondary);margin-bottom:4px"><strong>KOReader 墨水屏 OPDS</strong>（<code>opdsforcomic.koplugin</code>）</div>
                <div style="display:flex;gap:6px;align-items:center">
                  <div class="code-box" style="flex:1;margin:0">${esc(koreaderURL)}</div>
                  <button type="button" class="btn btn-sm" data-acc-copy="${esc(koreaderURL)}">复制</button>
                </div>
              </div>
              <div>
                <div style="font-size:12px;color:var(--text-secondary);margin-bottom:4px"><strong>标准 Komga OPDS v1.2</strong></div>
                <div style="display:flex;gap:6px;align-items:center">
                  <div class="code-box" style="flex:1;margin:0">${esc(komgaOpdsURL)}</div>
                  <button type="button" class="btn btn-sm" data-acc-copy="${esc(komgaOpdsURL)}">复制</button>
                </div>
              </div>
            </div>
            <div style="margin-top:12px;padding-top:12px;border-top:1px solid var(--border)">
              <div style="font-size:12.5px;font-weight:600;margin-bottom:6px">Bangumi 浏览器油猴脚本</div>
              <div style="display:flex;flex-wrap:wrap;gap:8px;align-items:center">
                <a class="btn btn-sm" href="/bgm/userscript/latest.user.js" target="_blank" rel="noopener noreferrer">① 安装 / 更新油猴脚本</a>
                <button type="button" class="btn btn-sm btn-primary" id="acc-us-pair-key">② 一键配对个人专属 Key 到油猴</button>
              </div>
              <div id="bgm-us-status" style="margin-top:6px;font-size:12px;color:var(--text-muted)"></div>
            </div>
          </div>

          <div class="card" ${presetKey ? 'style="border-color:var(--accent);box-shadow:0 0 0 2px var(--accent-soft)"' : ''}>
            <h3>我的 API Key（仅作用于当前账号权限）</h3>
            <p style="font-size:12.5px;color:var(--text-secondary);margin-top:0">${isAdmin
              ? '支持通过 <code>Authorization: Bearer &lt;密钥&gt;</code> 或 <code>X-API-Key: &lt;密钥&gt;</code> 访问 Komga 接口、油猴脚本与 AI Agent 接口（<code>GET /api/agent/v1/manifest</code>），随时可单独吊销。'
              : '可用于 Komga 客户端（<code>X-API-Key</code>）或油猴脚本免密认证，随时可单独吊销。'}</p>
            <div class="inline-action-bar" style="margin-bottom:12px">
              <input type="text" id="acc-ak-name" value="${esc(presetKey)}" placeholder="备注名称，例如：手机 Koharia" style="height:36px;padding:0 10px;border-radius:8px;border:1px solid var(--border-strong);background:var(--bg-input);color:var(--text)">
              <button type="button" class="btn btn-primary" id="acc-ak-create">生成 API Key</button>
            </div>
            <div id="acc-ak-revealed" style="display:none;margin-bottom:12px"></div>
            ${(keys || []).length === 0 ? `<div class="empty-box" style="padding:18px">暂未创建个人 API Key。</div>` : `
              <div class="table-wrap"><table class="data-table">
                <thead><tr><th>名称</th><th>前缀</th><th>最近使用</th><th>操作</th></tr></thead>
                <tbody>
                  ${keys.map((k) => `
                    <tr>
                      <td><strong>${esc(k.name)}</strong></td>
                      <td><code>${esc(k.keyPrefix)}…</code></td>
                      <td style="font-size:12px;color:var(--text-muted)">${fmtTime(k.lastUsedAt || k.createdAt)}</td>
                      <td><button type="button" class="btn btn-sm btn-danger" data-acc-del-ak="${esc(k.id)}">吊销</button></td>
                    </tr>`).join('')}
                </tbody>
              </table></div>`}
          </div>
        </div>

        ${tracker ? `
        <div class="card">
          <div class="section-head" style="margin-bottom:8px">
            <h3>我的 Bangumi 进度追踪器（单向自动同步）</h3>
            <div style="display:flex;flex-wrap:wrap;gap:6px;align-items:center">
              ${tr.hasToken
                ? `<span class="badge ${tr.enabled ? 'badge-success' : ''}">${tr.enabled ? '已启用自动同步' : '已绑定（未开启同步）'}${tr.nickname || tr.username ? ` · ${esc(tr.nickname || tr.username)}${tr.username ? ` (@${esc(tr.username)})` : ''}` : ''}</span>`
                : `<span class="badge">未绑定 Bangumi 账号</span>`}
              ${tr.hasToken ? `<span class="badge ${tr.autoRefresh ? 'badge-accent' : ''}">${tr.autoRefresh ? 'OAuth 自动续期' : 'Personal Access Token'}</span>` : ''}
              ${tr.hasToken && tr.expired ? `<span class="badge badge-danger">授权已过期</span>` : (tr.hasToken && tr.expiresSoon ? `<span class="badge" style="color:var(--warn);border-color:var(--warn)">即将到期</span>` : '')}
            </div>
          </div>
          <p style="margin:0 0 12px;font-size:12.5px;color:var(--text-secondary);line-height:1.65">
            绑定你自己的 Bangumi 账号后，你在本站的新增阅读进度会自动单向同步到你的 Bangumi 收藏（与其他用户的进度和 Bangumi 账号完全隔离）。
          </p>
          ${tr.hasToken ? `
            <div style="display:flex;flex-wrap:wrap;gap:14px;align-items:center;margin-bottom:10px;padding:8px 10px;border-radius:8px;background:var(--bg-input);border:1px solid var(--border);font-size:12px;color:var(--text-secondary)">
              <span>当前方式：<strong>${tr.autoRefresh ? 'Bangumi OAuth（自动续期）' : 'Personal Access Token'}</strong></span>
              ${tr.expiresAt ? `<span>到期：<strong style="color:${tr.expired ? 'var(--danger)' : tr.expiresSoon ? 'var(--warn)' : 'var(--text)'}">${esc(fmtTime(tr.expiresAt))}</strong></span>` : ''}
              ${tr.lastRefreshAt ? `<span>最近刷新：${esc(fmtTime(tr.lastRefreshAt))}</span>` : ''}
              ${tr.lastRefreshError ? `<span style="color:var(--danger)">最近刷新失败：${esc(tr.lastRefreshError)}</span>` : ''}
            </div>` : ''}
          <div style="display:flex;flex-wrap:wrap;gap:8px;align-items:center;margin-bottom:12px">
            <button type="button" class="btn btn-bgm" id="acc-bgm-oauth-start">${tr.hasToken && tr.autoRefresh ? '重新进行 Bangumi OAuth 授权' : '使用 Bangumi OAuth 一键授权（推荐）'}</button>
            ${tr.hasToken && tr.autoRefresh ? `<button type="button" class="btn btn-sm" id="acc-bgm-oauth-refresh">立即刷新授权</button>` : ''}
            ${tr.hasToken ? `<button type="button" class="btn btn-sm" id="acc-bgm-test">测试连接</button>` : ''}
            ${tr.hasToken ? `<button type="button" class="btn btn-sm btn-danger" id="acc-bgm-clear">解绑清空</button>` : ''}
          </div>
          <form id="acc-bgm-form" style="display:grid;gap:10px">
            <div style="display:flex;flex-wrap:wrap;gap:16px;align-items:center">
              <label class="check-row" style="font-size:13px;font-weight:600;color:var(--text)">
                <input type="checkbox" id="acc-bgm-enabled" ${tr.enabled ? 'checked' : ''}>
                <span>启用自动单向同步</span>
              </label>
              <label style="display:inline-flex;align-items:center;gap:8px;font-size:12.5px;color:var(--text-secondary)">
                <span>NSFW 策略：</span>
                <select id="acc-bgm-nsfw-mode" style="height:32px;padding:0 8px;border-radius:7px;border:1px solid var(--border-strong);background:var(--bg-input);color:var(--text)">
                  <option value="skip" ${(tr.nsfwMode || 'skip') === 'skip' ? 'selected' : ''}>跳过 NSFW 作品不同步（默认）</option>
                  <option value="private" ${tr.nsfwMode === 'private' ? 'selected' : ''}>以私密收藏（private）同步 NSFW</option>
                </select>
              </label>
              <label class="check-row" style="font-size:12.5px;color:var(--text-secondary)">
                <input type="checkbox" id="acc-bgm-private-default" ${tr.privateDefault ? 'checked' : ''}>
                <span>新建普通作品收藏时默认设为私密</span>
              </label>
              <button type="submit" class="btn btn-primary btn-sm" id="acc-bgm-save">保存追踪设置</button>
            </div>
            <details style="border:1px solid var(--border);border-radius:8px;padding:8px 10px;background:var(--bg-input)">
              <summary style="cursor:pointer;font-size:12.5px;font-weight:600;color:var(--text-secondary)">备选方式：手动粘贴 Personal Access Token 或 OAuth 回调链接</summary>
              <div style="display:grid;gap:10px;margin-top:8px">
                <div>
                  <div style="font-size:12px;color:var(--text-muted);margin-bottom:4px">Personal Access Token（<a href="https://next.bgm.tv/demo/access-token" target="_blank" rel="noopener noreferrer" style="color:var(--accent)">前往获取 ↗</a>）：</div>
                  <div class="inline-action-bar">
                    <input type="password" id="acc-bgm-token" autocomplete="off" placeholder="粘贴 Bangumi Personal Access Token" style="height:34px;padding:0 10px;border-radius:8px;border:1px solid var(--border-strong);background:var(--bg-card);color:var(--text)">
                    <button type="submit" class="btn btn-sm btn-primary">验证并保存 PAT</button>
                    <button type="button" class="btn btn-sm" id="acc-bgm-pat-test">测试输入 Token</button>
                  </div>
                </div>
                <div>
                  <div style="font-size:12px;color:var(--text-muted);margin-bottom:4px">若 OAuth 弹窗被拦截，可手动粘贴回调 URL 或授权码 code：</div>
                  <div class="inline-action-bar">
                    <input type="text" id="acc-bgm-oauth-manual-code" placeholder="粘贴回调链接（含 ?code=...）或纯 code" style="height:34px;padding:0 10px;border-radius:8px;border:1px solid var(--border-strong);background:var(--bg-card);color:var(--text)">
                    <button type="button" class="btn btn-sm" id="acc-bgm-oauth-manual-submit">提交完成授权</button>
                  </div>
                </div>
              </div>
            </details>
          </form>
          <div id="acc-bgm-test-result" style="margin-top:8px;font-size:12.5px"></div>
          ${trRecords.length > 0 ? `
            <div style="margin-top:12px">
              <div style="font-size:12.5px;font-weight:600;margin-bottom:6px;color:var(--text-secondary)">我的最近同步记录（最多 20 条）</div>
              <div class="table-wrap"><table class="data-table">
                <thead><tr><th>作品</th><th>Bangumi 条目</th><th>状态</th><th>远端进度</th><th>更新时间 / 说明</th><th>操作</th></tr></thead>
                <tbody>
                  ${trRecords.map((rec) => {
                    const statusBadge = rec.status === 'synced'
                      ? `<span class="badge badge-success">已同步</span>`
                      : rec.status === 'skipped'
                        ? `<span class="badge">已跳过</span>`
                        : `<span class="badge badge-danger">失败</span>`;
                    const progParts = [];
                    const tLbl = fmtBgmCollectionType(rec.remoteType);
                    if (tLbl) progParts.push(tLbl);
                    if (rec.remoteVolStatus > 0) progParts.push(`第 ${rec.remoteVolStatus} 卷`);
                    if (rec.remotePrivate) progParts.push('私密');
                    return `
                      <tr>
                        <td>${rec.seriesId ? `<a href="/series/${encodeURIComponent(rec.seriesId)}" data-nav style="color:var(--accent);font-weight:500">${esc(rec.seriesTitle || rec.seriesId)}</a>` : esc(rec.seriesTitle || '—')}</td>
                        <td><a href="https://bgm.tv/subject/${rec.bgmSubjectId}" target="_blank" rel="noopener noreferrer" class="badge badge-bgm">#${rec.bgmSubjectId} ↗</a></td>
                        <td>${statusBadge}</td>
                        <td style="font-size:12px">${esc(progParts.join(' · ') || '—')}</td>
                        <td class="cell-wrap" style="font-size:12px;color:${rec.status === 'failed' ? 'var(--danger)' : 'var(--text-muted)'}">${esc(rec.lastError || fmtTime(rec.lastSyncedAt || rec.updatedAt))}</td>
                        <td>${rec.seriesId && rec.status === 'failed' ? `<button type="button" class="btn btn-sm" data-acc-retry-tracker="${esc(rec.seriesId)}">重试</button>` : ''}</td>
                      </tr>`;
                  }).join('')}
                </tbody>
              </table></div>
            </div>` : ''}
        </div>` : ''}

        <div class="card">
          <h3>我的活跃登录会话（${(sessions || []).length}）</h3>
          <div class="table-wrap"><table class="data-table">
            <thead><tr><th>会话标识</th><th>浏览器 / 客户端</th><th>最近活跃</th><th>过期时间</th><th>操作</th></tr></thead>
            <tbody>
              ${(sessions || []).map((s) => {
                const sid = s.id || s.tokenPrefix || '';
                const expTxt = (!s.expiresAt || String(s.expiresAt).startsWith('9999-')) ? '永久有效（自动续期）' : fmtTime(s.expiresAt);
                return `
                  <tr>
                    <td><code>${esc(sid)}</code> ${s.current ? '<span class="badge badge-success">当前设备</span>' : ''}</td>
                    <td style="font-size:12px;color:var(--text-secondary);max-width:340px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc(s.userAgent || '—')}</td>
                    <td>${fmtTime(s.lastSeenAt)}</td>
                    <td>${esc(expTxt)}</td>
                    <td>${!s.current && sid ? `<button type="button" class="btn btn-sm btn-danger" data-acc-del-sess="${esc(sid)}">强制下线</button>` : ''}</td>
                  </tr>`;
              }).join('')}
            </tbody>
          </table></div>
        </div>
      `;

      const nsfwToggle = document.getElementById('acc-show-nsfw');
      if (nsfwToggle) {
        nsfwToggle.onchange = async (e) => {
          const want = e.target.checked;
          const startUser = state.auth.userId || '';
          nsfwToggle.disabled = true;
          try {
            const res = await api('/admin/api/me/preferences', { method: 'PUT', body: { showNsfw: want } });
            if ((state.auth.userId || '') !== startUser) return;
            const persisted = (res && typeof res.showNsfw === 'boolean') ? res.showNsfw : want;
            nsfwToggle.checked = persisted;
            state.auth.showNsfw = persisted;
            state.taxonomies.loaded = false;
            toast(persisted ? '已开启显示 NSFW 作品' : '已隐藏 NSFW 作品');
          } catch (err) {
            nsfwToggle.checked = !want;
            toast(err.message, 'error');
          } finally {
            nsfwToggle.disabled = false;
          }
        };
      }

      bindDisplayPrefControls(content);

      document.getElementById('acc-pw-form').onsubmit = async (e) => {
        e.preventDefault();
        const currentPassword = document.getElementById('acc-cur-pw').value;
        const newUsername = isAdmin ? (document.getElementById('acc-new-user')?.value || '').trim() : '';
        const newPassword = document.getElementById('acc-new-pw').value;
        const newPassword2 = document.getElementById('acc-new-pw2').value;
        if (!newUsername && !newPassword) { toast('请填写要修改的新用户名或新密码', 'error'); return; }
        if (newPassword) {
          if (newPassword.length < 8) { toast('新密码至少需要 8 位字符', 'error'); return; }
          if (newPassword !== newPassword2) { toast('两次输入的新密码不一致', 'error'); return; }
        }
        try {
          const updated = await api('/admin/api/auth/credentials', {
            method: 'PUT',
            body: isAdmin ? { currentPassword, newUsername, newPassword } : { currentPassword, newPassword },
            allow401: true,
          });
          if (updated && updated.username) state.auth.username = updated.username;
          toast(isAdmin ? '管理员凭据已更新，其他设备会话已自动下线' : '登录密码已更新，其他设备会话已自动下线');
          renderAccountPage();
        } catch (err) { toast(err.message, 'error'); }
      };

      content.querySelectorAll('button[data-acc-copy]').forEach((btn) => {
        btn.onclick = async () => {
          const ok = await copyTextSafeGlobal(btn.getAttribute('data-acc-copy') || '');
          toast(ok ? '已复制到剪贴板' : '请手动选中复制', ok ? 'ok' : 'error');
        };
      });

      const accUsPairBtn = document.getElementById('acc-us-pair-key');
      if (accUsPairBtn) {
        accUsPairBtn.onclick = async () => {
          accUsPairBtn.disabled = true;
          const popup = window.open('about:blank', '_blank');
          try {
            const res = await api('/admin/api/bangumi/userscript/pair-key', { method: 'POST' });
            const hash = `server=${encodeURIComponent(location.origin)}&key=${encodeURIComponent(res.key)}`;
            const pairUrl = `${location.origin}${res.pairPath || '/bgm/userscript/pair'}#${hash}`;
            if (popup && !popup.closed) popup.location.href = pairUrl;
            else window.location.href = pairUrl;
            toast('已打开配对页同步个人专属 Key 到油猴');
          } catch (err) {
            if (popup && !popup.closed) popup.close();
            toast(err.message || '配对失败', 'error');
          } finally {
            accUsPairBtn.disabled = false;
          }
        };
      }

      document.getElementById('acc-ak-create').onclick = async () => {
        const name = document.getElementById('acc-ak-name').value.trim();
        if (!name) { toast('请填写 API Key 备注名称', 'error'); return; }
        const startSeq = routeSeq;
        const startUser = state.auth.userId || '';
        try {
          const res = await api('/admin/api/auth/api-keys', { method: 'POST', body: { name } });
          if (startSeq !== routeSeq || (state.auth.userId || '') !== startUser) return;
          await renderAccountPage();
          if (startSeq !== routeSeq || (state.auth.userId || '') !== startUser) return;
          const rev = document.getElementById('acc-ak-revealed');
          if (rev) {
            rev.style.display = '';
            rev.innerHTML = `
              <div style="padding:10px 12px;border-radius:8px;background:var(--success-soft);border:1px solid var(--success)">
                <div style="font-size:12px;font-weight:600;color:var(--success);margin-bottom:4px">请立即复制保存，此完整密钥仅显示一次：</div>
                <div class="code-box" style="user-select:all">${esc(res.key)}</div>
              </div>`;
          }
        } catch (err) { toast(err.message, 'error'); }
      };

      content.querySelectorAll('button[data-acc-del-ak]').forEach((btn) => {
        btn.onclick = async () => {
          if (!(await confirmModal('确认吊销该 API Key？', { title: '吊销 API Key', confirmText: '确认吊销' }))) return;
          if (!btn.isConnected) return;
          try {
            await api(`/admin/api/auth/api-keys/${encodeURIComponent(btn.getAttribute('data-acc-del-ak'))}`, { method: 'DELETE' });
            toast('API Key 已吊销');
            renderAccountPage();
          } catch (err) { toast(err.message, 'error'); }
        };
      });

      content.querySelectorAll('button[data-acc-del-sess]').forEach((btn) => {
        btn.onclick = async () => {
          try {
            await api(`/admin/api/auth/sessions/${encodeURIComponent(btn.getAttribute('data-acc-del-sess'))}`, { method: 'DELETE' });
            toast('会话已移除');
            renderAccountPage();
          } catch (err) { toast(err.message, 'error'); }
        };
      });

      if (tracker) {
        const oauthStartBtn = document.getElementById('acc-bgm-oauth-start');
        if (oauthStartBtn) {
          oauthStartBtn.onclick = async () => {
            oauthStartBtn.disabled = true;
            const box = document.getElementById('acc-bgm-test-result');
            if (box) box.innerHTML = `<span style="color:var(--text-secondary)">正在生成 Bangumi OAuth 授权链接…</span>`;
            try {
              const rep = await api('/admin/api/bangumi/tracker/oauth/start', { method: 'POST' });
              state.bgmOAuthLastState = rep.state || '';
              const popup = window.open(rep.authorizeUrl, 'mangaalpha_bgm_oauth', 'width=560,height=720');
              if (!popup && box) {
                box.innerHTML = `<span style="color:var(--warn)">浏览器拦截了弹窗，请点此 <a href="${esc(rep.authorizeUrl)}" target="_blank" rel="noopener noreferrer" style="color:var(--accent);font-weight:600">手动打开 Bangumi 授权页 ↗</a></span>`;
              } else if (box) {
                box.innerHTML = `<span style="color:var(--text-secondary)">已打开 Bangumi 授权窗口，请在弹出的页面中点击「同意授权」…</span>`;
              }
            } catch (err) {
              toast(err.message || '启动 OAuth 授权失败', 'error');
              if (box) box.innerHTML = `<span style="color:var(--danger)">❌ ${esc(err.message)}</span>`;
            } finally {
              oauthStartBtn.disabled = false;
            }
          };
        }
        const oauthRefreshBtn = document.getElementById('acc-bgm-oauth-refresh');
        if (oauthRefreshBtn) {
          oauthRefreshBtn.onclick = async () => {
            oauthRefreshBtn.disabled = true;
            try {
              await api('/admin/api/bangumi/tracker/oauth/refresh', { method: 'POST' });
              toast('Bangumi OAuth 授权已刷新续期');
              renderAccountPage();
            } catch (err) {
              toast(err.message || '刷新失败', 'error');
              oauthRefreshBtn.disabled = false;
            }
          };
        }
        const manualBtn = document.getElementById('acc-bgm-oauth-manual-submit');
        if (manualBtn) {
          manualBtn.onclick = async () => {
            const val = (document.getElementById('acc-bgm-oauth-manual-code')?.value || '').trim();
            if (!val) { toast('请先粘贴回调链接或授权码 code', 'error'); return; }
            manualBtn.disabled = true;
            try {
              await api('/admin/api/bangumi/tracker/oauth/exchange', {
                method: 'POST',
                body: { codeOrUrl: val, state: state.bgmOAuthLastState || '' },
              });
              toast('Bangumi OAuth 授权绑定成功');
              renderAccountPage();
            } catch (err) {
              toast(err.message || '换取 OAuth Token 失败', 'error');
              manualBtn.disabled = false;
            }
          };
        }
        const formEl = document.getElementById('acc-bgm-form');
        if (formEl) {
          formEl.onsubmit = async (e) => {
            e.preventDefault();
            const tokVal = (document.getElementById('acc-bgm-token')?.value || '').trim();
            const payload = {
              enabled: Boolean(document.getElementById('acc-bgm-enabled')?.checked),
              nsfwMode: document.getElementById('acc-bgm-nsfw-mode')?.value || 'skip',
              privateDefault: Boolean(document.getElementById('acc-bgm-private-default')?.checked),
            };
            if (tokVal) payload.accessToken = tokVal;
            try {
              await api('/admin/api/bangumi/tracker/config', { method: 'PUT', body: payload });
              toast(tokVal ? 'Bangumi Token 验证通过，追踪器配置已保存' : 'Bangumi 追踪器配置已保存');
              renderAccountPage();
            } catch (err) { toast(err.message || '保存失败', 'error'); }
          };
        }
        const runAccTrackerTest = async (btn, useInput) => {
          if (!btn) return;
          btn.disabled = true;
          const box = document.getElementById('acc-bgm-test-result');
          const tokVal = useInput ? (document.getElementById('acc-bgm-token')?.value || '').trim() : '';
          if (box) box.innerHTML = `<span style="color:var(--text-secondary)">正在连接 api.bgm.tv 验证授权…</span>`;
          try {
            const rep = await api('/admin/api/bangumi/tracker/test', {
              method: 'POST',
              body: tokVal ? { accessToken: tokVal } : {},
            });
            if (rep && rep.ok && box) {
              box.innerHTML = `<span style="color:var(--success)">✅ 授权有效：${esc(rep.nickname || rep.username)}${rep.username ? ` (@${esc(rep.username)})` : ''} · 延迟 ${rep.latencyMs} ms</span>`;
            } else if (box) {
              box.innerHTML = `<span style="color:var(--danger)">❌ 验证失败：${esc((rep && rep.error) || 'Token 无效')}</span>`;
            }
          } catch (err) {
            if (box) box.innerHTML = `<span style="color:var(--danger)">❌ ${esc(err.message)}</span>`;
          } finally {
            btn.disabled = false;
          }
        };
        const tBtn = document.getElementById('acc-bgm-test');
        if (tBtn) tBtn.onclick = () => runAccTrackerTest(tBtn, false);
        const ptBtn = document.getElementById('acc-bgm-pat-test');
        if (ptBtn) ptBtn.onclick = () => runAccTrackerTest(ptBtn, true);
        const clearBtn = document.getElementById('acc-bgm-clear');
        if (clearBtn) {
          clearBtn.onclick = async () => {
            if (!(await confirmModal('确认解绑并清空个人 Bangumi 追踪器授权？', { title: '解绑 Bangumi 追踪器', confirmText: '确认解绑' }))) return;
            if (!clearBtn.isConnected) return;
            try {
              await api('/admin/api/bangumi/tracker/config', { method: 'PUT', body: { clearToken: true } });
              toast('已解绑 Bangumi 追踪器');
              renderAccountPage();
            } catch (err) { toast(err.message, 'error'); }
          };
        }
        content.querySelectorAll('button[data-acc-retry-tracker]').forEach((btn) => {
          btn.onclick = async () => {
            const sid = btn.getAttribute('data-acc-retry-tracker');
            btn.disabled = true;
            btn.textContent = '同步中…';
            try {
              await api(`/admin/api/series/${encodeURIComponent(sid)}/bangumi/tracker/retry`, { method: 'POST' });
              toast('已重试同步至 Bangumi');
              renderAccountPage();
            } catch (err) {
              toast(err.message || '重试同步失败', 'error');
              renderAccountPage();
            }
          };
        });
      }
    } catch (err) {
      if (seq !== routeSeq) return;
      toast(err.message, 'error');
    }
  }

  // --- AI Agent One-Click Authorization Page (/agent-auth/:requestID) ---

  async function renderAgentAuthPage(requestID) {
    if (!isAdminUser()) {
      document.title = '权限不足 · MangaAlpha';
      renderShell('account', `
        <div class="card" style="max-width:520px;margin:24px auto;text-align:center">
          <h2 style="margin:0 0 8px">需要管理员权限</h2>
          <p style="font-size:13px;color:var(--text-secondary);margin:0 0 16px">AI Agent 管理授权仅限管理员账号批准。请切换为管理员账号后再访问此链接。</p>
          <a href="/" data-nav class="btn btn-primary">返回书架首页</a>
        </div>`);
      return;
    }
    document.title = 'AI Agent 授权确认 · MangaAlpha';
    renderShell('admin', `<div class="boot-splash" style="min-height:40vh"><div class="spinner"></div><p>正在加载授权请求…</p></div>`);
    const content = document.getElementById('page-content');
    if (!content) return;

    const copySafe = async (txt) => {
      if (navigator.clipboard && window.isSecureContext) {
        try { await navigator.clipboard.writeText(txt); return true; } catch (_) {}
      }
      const ta = document.createElement('textarea');
      ta.value = txt;
      ta.setAttribute('readonly', '');
      ta.style.position = 'fixed';
      ta.style.left = '-9999px';
      document.body.appendChild(ta);
      ta.select();
      let ok = false;
      try { ok = document.execCommand('copy'); } catch (_) {}
      document.body.removeChild(ta);
      return ok;
    };

    try {
      const req = await api(`/admin/api/agent/auth-requests/${encodeURIComponent(requestID)}`);
      const isPending = req.status === 'pending';
      const defaultKeyName = `AI Agent · ${req.clientName || 'Assistant'}`;
      content.innerHTML = `
        <div class="card" style="max-width:560px;margin:24px auto">
          <div class="section-head" style="margin-bottom:10px">
            <h2 style="margin:0;font-size:20px">AI Agent 授权确认</h2>
            <span id="agent-auth-status-badge" class="badge ${req.status === 'approved' || req.status === 'consumed' ? 'badge-success' : req.status === 'rejected' ? 'badge-danger' : 'badge-accent'}">
              ${req.status === 'pending' ? '等待批准' : req.status === 'approved' ? '已批准（等待 Agent 领取）' : req.status === 'consumed' ? '已批准并已被领取' : '已拒绝'}
            </span>
          </div>
          <div style="padding:14px 16px;border-radius:10px;background:var(--bg-input);border:1px solid var(--border);text-align:center;margin-bottom:14px">
            <div style="font-size:12px;color:var(--text-muted);margin-bottom:4px">安全核对码（请确认与 AI Agent 对话中展示的完全一致）</div>
            <div style="font-size:28px;font-weight:800;letter-spacing:4px;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--accent)">${esc(req.verificationCode)}</div>
          </div>
          <div style="display:grid;gap:6px;font-size:13px;color:var(--text-secondary);margin-bottom:14px">
            <div>申请客户端：<strong style="color:var(--text)">${esc(req.clientName)}</strong></div>
            <div>申请用途：<strong style="color:var(--text)">${esc(req.purpose)}</strong></div>
            <div>请求来源 IP：<code>${esc(req.clientIp || '—')}</code> · 过期时间：${esc(fmtTime(req.expiresAt))}</div>
          </div>
          <div style="font-size:12px;color:var(--text-muted);line-height:1.6;padding:8px 10px;border-radius:8px;background:var(--bg-input);border:1px solid var(--border);margin-bottom:14px">
            批准后将自动签发一个专属 API Key 并一次性交付给该 AI Agent（不暴露你的登录密码）。你随时可以在
            <a href="/account" data-nav style="color:var(--accent)">「账号设置 → 我的 API Key」</a> 查看或吊销。
          </div>
          ${isPending ? `
            <div class="field" style="margin-bottom:14px">
              <label>API Key 备注名称（方便后续在「账号设置」识别与吊销）</label>
              <input type="text" id="agent-auth-key-name" value="${esc(defaultKeyName)}" maxlength="80">
            </div>
            <div style="display:flex;flex-wrap:wrap;gap:10px;align-items:center">
              <button type="button" class="btn btn-primary" id="agent-auth-approve">批准授权并签发 API Key</button>
              <button type="button" class="btn btn-danger" id="agent-auth-reject">拒绝请求</button>
              <a href="/admin?tab=bangumi" data-nav class="btn">返回管理后台</a>
            </div>
          ` : `
            <div style="display:flex;flex-wrap:wrap;gap:10px;align-items:center">
              <a href="/account" data-nav class="btn btn-primary">查看 / 管理 API Key</a>
              <a href="/admin?tab=bangumi" data-nav class="btn">返回 Bangumi 刮削中心</a>
            </div>
          `}
          <div id="agent-auth-result" style="margin-top:14px"></div>
        </div>
      `;

      if (isPending) {
        const approveBtn = document.getElementById('agent-auth-approve');
        const rejectBtn = document.getElementById('agent-auth-reject');
        const resultBox = document.getElementById('agent-auth-result');
        approveBtn.onclick = async () => {
          approveBtn.disabled = true;
          rejectBtn.disabled = true;
          const keyName = (document.getElementById('agent-auth-key-name')?.value || '').trim() || defaultKeyName;
          try {
            const res = await api(`/admin/api/agent/auth-requests/${encodeURIComponent(requestID)}/approve`, {
              method: 'POST',
              body: { keyName },
            });
            toast('已批准授权并签发 API Key');
            const stBadge = document.getElementById('agent-auth-status-badge');
            if (stBadge) {
              stBadge.className = 'badge badge-success';
              stBadge.textContent = '已批准（等待 Agent 领取）';
            }
            if (resultBox) {
              resultBox.innerHTML = `
                <div style="padding:12px 14px;border-radius:10px;background:var(--success-soft);border:1px solid var(--success)">
                  <div style="font-size:13px;font-weight:700;color:var(--success);margin-bottom:6px">✅ 授权已批准！支持自动轮询的 AI Agent 正在自动领取密钥并开始匹配。</div>
                  <div style="font-size:12px;color:var(--text-secondary);margin-bottom:8px">如果你的 Agent 处于纯网页对话环境不支持自动轮询，也可点击下方按钮手动复制密钥发回对话（此完整密钥仅显示一次）：</div>
                  <div style="display:flex;flex-wrap:wrap;gap:8px;align-items:center">
                    <code class="code-box" style="flex:1;min-width:200px;margin:0;user-select:all">${esc(res.key)}</code>
                    <button type="button" class="btn btn-sm btn-primary" id="agent-auth-copy-key">复制密钥</button>
                  </div>
                  <div style="margin-top:10px;font-size:12px">
                    <a href="/account" data-nav style="color:var(--accent)">前往「账号设置」查看 / 随时吊销 →</a>
                  </div>
                </div>`;
              const copyBtn = document.getElementById('agent-auth-copy-key');
              if (copyBtn) {
                copyBtn.onclick = async () => {
                  const ok = await copySafe(res.key);
                  toast(ok ? '已复制 API Key' : '请手动选中复制', ok ? 'info' : 'error');
                };
              }
            }
          } catch (err) {
            toast(err.message || '批准失败', 'error');
            approveBtn.disabled = false;
            rejectBtn.disabled = false;
          }
        };
        rejectBtn.onclick = async () => {
          approveBtn.disabled = true;
          rejectBtn.disabled = true;
          try {
            await api(`/admin/api/agent/auth-requests/${encodeURIComponent(requestID)}/reject`, { method: 'POST' });
            toast('已拒绝该授权请求');
            renderAgentAuthPage(requestID);
          } catch (err) {
            toast(err.message || '操作失败', 'error');
            approveBtn.disabled = false;
            rejectBtn.disabled = false;
          }
        };
      }
    } catch (err) {
      content.innerHTML = `
        <div class="card" style="max-width:520px;margin:24px auto;text-align:center">
          <h2 style="margin:0 0 8px">授权请求不可用</h2>
          <p style="font-size:13px;color:var(--text-secondary);margin:0 0 16px">${esc(err.message || '授权请求不存在或已过期，请让 AI Agent 重新发起申请。')}</p>
          <a href="/account" data-nav class="btn btn-primary">前往「账号设置」</a>
          <a href="/admin?tab=bangumi" data-nav class="btn" style="margin-left:8px">返回 Bangumi 刮削中心</a>
        </div>`;
    }
  }

  window.addEventListener('message', (event) => {
    if (event.origin !== location.origin) return;
    const data = event.data;
    if (!data) return;
    if (data.type === 'MANGAALPHA_USERSCRIPT_PAIRED' && data.ok) {
      toast('油猴脚本已自动接收并保存专属 Key');
      const stEl = document.getElementById('bgm-us-status');
      if (stEl) {
        stEl.innerHTML = `<span style="color:var(--success)">✅ 油猴脚本已成功保存服务器地址与专属 Key，现在打开任意 Bangumi 条目页即可生效。</span>`;
      }
      return;
    }
    if (data.type !== 'MANGAALPHA_BGM_OAUTH_DONE') return;
    toast(data.message || (data.ok ? 'Bangumi OAuth 授权成功' : 'Bangumi OAuth 授权失败'), data.ok ? 'ok' : 'error');
    if (location.pathname === '/account' || location.pathname === '/account/') {
      renderAccountPage();
      return;
    }
    const target = state.bgmOAuthRefreshTarget || document.getElementById('admin-tab-body');
    if (target && state.adminTab === 'bangumi') {
      renderAdminBangumi(target);
    }
  });

  // --- Router ---

  function updateFooterVersion(auth) {
    const el = document.getElementById('site-footer-version');
    const sep = document.getElementById('site-footer-sep');
    if (!el) return;
    const raw = (auth && auth.authenticated && typeof auth.version === 'string') ? auth.version.trim() : '';
    if (!raw) {
      el.hidden = true;
      if (sep) sep.hidden = true;
      el.innerHTML = '';
      el.removeAttribute('href');
      el.removeAttribute('title');
      return;
    }
    const stableMatch = /^v\d+\.\d+\.\d+$/.test(raw);
    const previewMatch = raw.match(/^(v\d+\.\d+\.\d+)-(?:local-)?preview\b/i);
    const verLabel = previewMatch ? `${previewMatch[1]}-preview` : raw;
    const codename = (typeof auth.codename === 'string') ? auth.codename.trim() : '';
    const quote = (typeof auth.codenameQuote === 'string') ? auth.codenameQuote.trim() : '';
    el.innerHTML = `<span class="site-footer-ver-num">${esc(verLabel)}</span>${codename ? `<span class="site-footer-codename">${esc(codename)}</span>` : ''}`;
    let tip = raw;
    if (codename) tip += ` · ${codename}`;
    if (quote) tip += `\n${quote}`;
    el.title = tip;
    el.href = stableMatch
      ? `https://github.com/xJogger/MangaAlpha/releases/tag/${encodeURIComponent(raw)}`
      : 'https://github.com/xJogger/MangaAlpha/releases';
    el.hidden = false;
    if (sep) sep.hidden = false;
  }

  let routeSeq = 0;
  async function route() {
    const seq = ++routeSeq;
    closeConfirmModal(false);
    try {
      const st = await api('/admin/api/auth/status', { allow401: true });
      if (seq !== routeSeq) return;
      state.auth = st || { claimed: false, authenticated: false, username: '' };
    } catch (_) {}

    if (seq !== routeSeq) return;
    closeConfirmModal(false);
    updateFooterVersion(state.auth);
    const p = location.pathname;
    if (!state.auth.claimed) {
      if (p !== '/setup') {
        history.replaceState(null, '', '/setup');
      }
      renderSetup();
      return;
    }

    if (p === '/setup') {
      navigate(state.auth.authenticated ? '/' : '/login', true);
      return;
    }

    if (p === '/login') {
      if (state.auth.authenticated) {
        const redir = new URLSearchParams(location.search).get('redirect') || '/';
        navigate(safeRedirectTarget(redir), true);
        return;
      }
      renderLogin();
      return;
    }

    if (!state.auth.authenticated) {
      const redir = encodeURIComponent(p + location.search);
      history.replaceState(null, '', `/login?redirect=${redir}`);
      renderLogin();
      return;
    }

    await ensureDisplayPrefs();
    if (seq !== routeSeq) return;

    if (p === '/' || p === '') {
      await renderHome();
      return;
    }
    if (p === '/libraries') {
      await renderLibraries('');
      return;
    }
    if (p.startsWith('/libraries/')) {
      const libID = decodeURIComponent(p.slice('/libraries/'.length));
      await renderLibraries(libID);
      return;
    }
    if (p === '/collections') {
      await renderCollections();
      return;
    }
    if (p.startsWith('/collections/')) {
      const cid = decodeURIComponent(p.slice('/collections/'.length));
      await renderCollectionDetail(cid);
      return;
    }
    if (p === '/readlists') {
      await renderReadLists();
      return;
    }
    if (p.startsWith('/readlists/')) {
      const rid = decodeURIComponent(p.slice('/readlists/'.length));
      await renderReadListDetail(rid);
      return;
    }
    if (p === '/history') {
      await renderHistory();
      return;
    }
    if (p.startsWith('/series/')) {
      const sid = decodeURIComponent(p.slice('/series/'.length));
      await renderSeriesDetail(sid);
      return;
    }
    if (p.startsWith('/book/')) {
      const bid = decodeURIComponent(p.slice('/book/'.length));
      await renderBookDetail(bid);
      return;
    }
    if (p.startsWith('/books/')) {
      const bid = decodeURIComponent(p.slice('/books/'.length));
      await renderBookDetail(bid);
      return;
    }
    if (p.startsWith('/agent-auth/')) {
      const rid = decodeURIComponent(p.slice('/agent-auth/'.length));
      await renderAgentAuthPage(rid);
      return;
    }
    if (p === '/account' || p === '/account/') {
      await renderAccountPage();
      return;
    }
    if (p === '/admin' || p === '/admin/') {
      await renderAdmin();
      return;
    }

    renderShell('', `<div class="empty-box">页面不存在（404） · <a href="/" data-nav style="color:var(--accent)">返回书架首页</a></div>`);
  }

  route();
})();

window.addEventListener('fk115-folder', (event) => {
  const input = document.getElementById('nl-local-path');
  if (input && typeof event.detail === 'string') input.value = event.detail;
});
