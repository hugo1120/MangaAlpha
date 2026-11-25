const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { test } = require('node:test');
const vm = require('node:vm');

const appSource = readFileSync('internal/httpserver/static/ui/app.js', 'utf8');
const cssSource = readFileSync('internal/httpserver/static/ui/app.css', 'utf8');

function decodeHTML(str) {
  return String(str)
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'")
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&amp;/g, '&');
}

function parseAttrs(raw) {
  const attrs = new Map();
  const re = /([a-zA-Z0-9_:-]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>/]+)))?/g;
  let m;
  while ((m = re.exec(raw)) !== null) {
    const key = m[1].toLowerCase();
    const val = m[2] ?? m[3] ?? m[4] ?? '';
    attrs.set(key, decodeHTML(val));
  }
  return attrs;
}

function parseStyle(raw) {
  const style = {};
  for (const part of String(raw || '').split(';')) {
    const idx = part.indexOf(':');
    if (idx <= 0) continue;
    const k = part.slice(0, idx).trim();
    const v = part.slice(idx + 1).trim();
    style[k] = v;
  }
  return style;
}

function createFakeDOM() {
  const containers = new Set();

  function parseElementsFromHTML(html, owner) {
    const elements = [];
    const stack = [];
    const tagRe = /<\/?([a-zA-Z0-9-]+)([^>]*)>/g;
    let m;
    while ((m = tagRe.exec(html)) !== null) {
      const full = m[0];
      const tag = m[1].toLowerCase();
      const isClose = full.startsWith('</');
      if (isClose) {
        if (tag === 'select') {
          for (let i = stack.length - 1; i >= 0; i--) {
            if (stack[i].tagName === 'SELECT') {
              const sel = stack[i];
              stack.splice(i, 1);
              if (sel._options.length > 0 && !sel._hasSelectedOption) {
                sel.value = sel._options[0].value;
              }
              break;
            }
          }
        }
        continue;
      }

      const attrs = parseAttrs(m[2] || '');
      if (tag === 'option') {
        const optVal = attrs.has('value') ? attrs.get('value') : '';
        const isSel = attrs.has('selected');
        for (let i = stack.length - 1; i >= 0; i--) {
          if (stack[i].tagName === 'SELECT') {
            const sel = stack[i];
            sel._options.push({ value: optVal, selected: isSel });
            if (isSel) {
              sel.value = optVal;
              sel._hasSelectedOption = true;
            }
            break;
          }
        }
        continue;
      }

      const el = {
        tagName: tag.toUpperCase(),
        id: attrs.get('id') || '',
        attrs,
        style: parseStyle(attrs.get('style') || ''),
        isConnected: true,
        disabled: attrs.has('disabled'),
        checked: attrs.has('checked'),
        open: attrs.has('open'),
        value: attrs.has('value') ? attrs.get('value') : (tag === 'input' && (attrs.get('type') || '').toLowerCase() === 'checkbox' ? 'on' : ''),
        textContent: '',
        _options: [],
        _hasSelectedOption: false,
        _listeners: new Map(),
        _owner: owner,
        getAttribute(name) {
          const k = String(name).toLowerCase();
          return this.attrs.has(k) ? this.attrs.get(k) : null;
        },
        setAttribute(name, val) {
          this.attrs.set(String(name).toLowerCase(), String(val));
        },
        addEventListener(type, fn) {
          const list = this._listeners.get(type) || [];
          list.push(fn);
          this._listeners.set(type, list);
        },
        querySelectorAll(sel) {
          return queryOnList(elements, sel);
        },
        querySelector(sel) {
          return queryOnList(elements, sel)[0] || null;
        },
        contains() {
          return false;
        },
        focus() {},
        setSelectionRange() {},
        remove() {},
      };
      if (tag === 'select') {
        stack.push(el);
      }
      elements.push(el);
    }

    // Populate innerHTML / textContent for non-void tags cleanly enough for assertions
    for (const el of elements) {
      if (el.tagName === 'TEXTAREA' && el.id) {
        const taRe = new RegExp(`<textarea[^>]*id="${el.id}"[^>]*>([\\s\\S]*?)<\\/textarea>`, 'i');
        const tm = taRe.exec(html);
        if (tm) el.value = decodeHTML(tm[1]);
      }
    }
    return elements;
  }

  function matchesSelector(el, singleSel) {
    const s = singleSel.trim();
    if (!s) return false;
    if (s.startsWith('#')) {
      return el.id === s.slice(1);
    }
    const tagAttr = s.match(/^([a-zA-Z0-9-]+)?\[([a-zA-Z0-9_-]+)(?:="([^"]*)")?\]$/);
    if (tagAttr) {
      const [, t, attr, expectedVal] = tagAttr;
      if (t && el.tagName !== t.toUpperCase()) return false;
      if (!el.attrs.has(attr.toLowerCase())) return false;
      if (expectedVal !== undefined && el.attrs.get(attr.toLowerCase()) !== expectedVal) return false;
      return true;
    }
    if (/^[a-zA-Z0-9-]+$/.test(s)) {
      return el.tagName === s.toUpperCase();
    }
    return false;
  }

  function queryOnList(list, selector) {
    const parts = String(selector).split(',').map((p) => p.trim()).filter(Boolean);
    return list.filter((el) => parts.some((p) => matchesSelector(el, p)));
  }

  function makeContainer(id = '') {
    const box = {
      id,
      isConnected: true,
      style: {},
      _html: '',
      _elements: [],
      get innerHTML() {
        return this._html;
      },
      set innerHTML(val) {
        for (const old of this._elements) old.isConnected = false;
        this._html = String(val || '');
        this._elements = parseElementsFromHTML(this._html, this);
        // Handle dynamic select innerHTML updates like #bgm-source-select
        for (const el of this._elements) {
          if (el.tagName === 'SELECT') {
            let selHTML = '';
            Object.defineProperty(el, 'innerHTML', {
              get() { return selHTML; },
              set(v) {
                selHTML = String(v || '');
                const parsed = parseElementsFromHTML(`<select>${selHTML}</select>`, this);
                const innerSel = parsed[0];
                if (innerSel) {
                  el._options = innerSel._options;
                  el._hasSelectedOption = innerSel._hasSelectedOption;
                  if (el._options.length > 0) {
                    el.value = innerSel._hasSelectedOption ? innerSel.value : el._options[0].value;
                  }
                }
              },
              configurable: true,
            });
          } else if (el.tagName === 'DIV') {
            let divHTML = '';
            Object.defineProperty(el, 'innerHTML', {
              get() { return divHTML; },
              set(v) {
                divHTML = String(v || '');
                const parsed = parseElementsFromHTML(divHTML, box);
                for (const old of el._subElements || []) old.isConnected = false;
                el._subElements = parsed;
                // Replace inside box._elements
                box._elements = box._elements.filter((x) => !x._subParent || x._subParent !== el);
                for (const p of parsed) {
                  p._subParent = el;
                  box._elements.push(p);
                }
              },
              configurable: true,
            });
            el.querySelectorAll = (sel) => queryOnList(el._subElements || [], sel);
            el.querySelector = (sel) => queryOnList(el._subElements || [], sel)[0] || null;
          }
        }
      },
      querySelectorAll(sel) {
        return queryOnList(this._elements, sel);
      },
      querySelector(sel) {
        return queryOnList(this._elements, sel)[0] || null;
      },
    };
    containers.add(box);
    return box;
  }

  const createdAnchors = [];
  const doc = {
    activeElement: null,
    body: {
      appendChild() {},
    },
    createElement(tag) {
      const el = { tagName: String(tag).toUpperCase(), style: {}, click() { if (el.tagName === 'A') createdAnchors.push(el); }, remove() {} };
      return el;
    },
    getElementById(id) {
      for (const c of containers) {
        if (c.id === id) return c;
        for (const el of c._elements) {
          if (el.id === id) return el;
        }
      }
      return null;
    },
    querySelectorAll(sel) {
      const out = [];
      for (const c of containers) out.push(...queryOnList(c._elements, sel));
      return out;
    },
    querySelector(sel) {
      return doc.querySelectorAll(sel)[0] || null;
    },
  };

  return { doc, makeContainer, createdAnchors };
}

function flushMicrotasks() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

function loadAdminScraperContext({ apiImpl, confirmResult = true, stateInit = {} } = {}) {
  const start = appSource.indexOf('  // --- Admin Bangumi Scraper Center ---');
  const end = appSource.indexOf('  // --- Admin Jobs Tab (Adaptive Polling ONLY when active!) ---', start);
  assert.ok(start >= 0 && end > start, 'expected Admin Bangumi Scraper Center boundaries');

  const { doc, makeContainer, createdAnchors } = createFakeDOM();
  const body = makeContainer('admin-tab-body');
  const modalSubhead = makeContainer('modal-subhead');
  const modalBody = makeContainer('modal-body');
  const modalFoot = makeContainer('modal-foot');

  const pollCalls = [];
  let stopPollCalls = 0;
  let lastModal = null;
  const toasts = [];
  const confirmCalls = [];
  const blobDownloads = [];

  class FakeBlob {
    constructor(parts, opts) {
      this.parts = parts;
      this.opts = opts;
    }
  }

  const ctx = vm.createContext({
    document: doc,
    location: { origin: 'http://127.0.0.1:25600', pathname: '/admin', search: '?tab=bangumi' },
    state: Object.assign({
      auth: { userId: 'admin', isAdmin: true },
      adminTab: 'bangumi',
      bgmProxyProbeHTML: '',
      bgmAgentIncludeNsfw: false,
      taxonomies: { loaded: true },
      extSourceNames: {},
      extMetaGuideOpen: false,
      extMetaRepoDraft: null,
      extMetaBatchDraft: null,
      extMetaExportDraft: null,
    }, stateInit),
    esc: (v) => (v == null ? '' : String(v)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;')),
    fmtBytes: (n) => `${Number(n || 0)} B`,
    fmtTime: (t) => t || '—',
    providerLabel: (p) => p || 'local',
    toast: (msg, kind = 'ok') => toasts.push({ msg, kind }),
    confirmModal: async (msg, opts) => {
      confirmCalls.push({ msg, opts });
      return confirmResult;
    },
    openModal: (opts) => {
      lastModal = opts;
      modalSubhead.innerHTML = opts.subheadHTML || '';
      modalBody.innerHTML = opts.bodyHTML || '';
      modalFoot.innerHTML = opts.footHTML || '';
      if (opts.onMount) opts.onMount(modalBody);
    },
    closeModal: () => {
      modalSubhead.innerHTML = '';
      modalBody.innerHTML = '';
      modalFoot.innerHTML = '';
    },
    schedulePoll: (fn, ms) => pollCalls.push({ fn, ms }),
    stopPolling: () => { stopPollCalls += 1; },
    api: apiImpl,
    Blob: FakeBlob,
    URL: {
      createObjectURL: (b) => {
        blobDownloads.push(b);
        return 'blob:fake-url';
      },
      revokeObjectURL: () => {},
    },
    setTimeout,
  });

  const exports = vm.runInContext(
    appSource.slice(start, end) + '\n({ renderAdminBangumi, paintAdminBangumi, openExtMetaSourceModal, openLibraryChainModal, openMappingExportPreviewModal });',
    ctx,
  );
  return {
    ...exports,
    ctx,
    doc,
    body,
    modalBody,
    modalFoot,
    pollCalls,
    getStopPollCalls: () => stopPollCalls,
    getLastModal: () => lastModal,
    toasts,
    confirmCalls,
    blobDownloads,
    createdAnchors,
  };
}

function sampleBangumiStatus() {
  return {
    totalSeries: 100,
    matchedCount: 80,
    pendingCount: 10,
    unmatchedCount: 10,
    nsfwCount: 4,
    showNsfw: false,
    proxyUrl: '',
    archive: { ready: true, subjectCount: 50000, updating: false },
    archiveSource: { source: 'zh_cn', customUrl: '' },
    archiveSchedule: { enabled: false, frequency: 'weekly', dayOfWeek: 4, timeOfDay: '06:00' },
  };
}

function sampleOverview(overrides = {}) {
  return Object.assign({
    recommendedRepoUrl: 'https://raw.githubusercontent.com/BlackMuseCode/mangaalpha-metadata-repo/main/repo.json',
    repos: [
      {
        id: 'repo-1',
        name: 'Community Open Metadata',
        url: 'https://raw.githubusercontent.com/BlackMuseCode/mangaalpha-metadata-repo/main/repo.json',
        repoVersion: 'v1.0',
        description: 'Open metadata sources',
        homepage: 'https://github.com/BlackMuseCode/mangaalpha-metadata-repo',
        enabled: true,
        lastSyncedAt: '2026-11-25T09:00:00Z',
        lastError: '',
      },
    ],
    sources: [
      {
        id: 'src-dict',
        repoId: 'repo-1',
        sourceKey: 'zh-tags',
        kind: 'tag_dictionary',
        schemaType: 'tag_translation_text_v1',
        name: '中文标签词典',
        downloadUrl: 'https://example.test/db.text.json.gz',
        enabled: true,
        ready: true,
        entryCount: 12000,
        fileSize: 1024000,
        autoUpdate: 'weekly',
        tagDisplayMode: 'zh',
        lastUpdatedAt: '2026-11-25T09:05:00Z',
        lastError: '',
      },
      {
        id: 'src-sqlite-1',
        repoId: 'repo-1',
        sourceKey: 'open-gallery',
        kind: 'offline_sqlite',
        schemaType: 'gallery_v1',
        name: '通用离线图库 A',
        downloadUrl: 'https://example.test/gallery.sqlite.zst',
        localPath: '/data/metadata/gallery.sqlite',
        tagDictSourceId: 'src-dict',
        tagDisplayMode: 'zh',
        autoUpdate: 'manual',
        onlineApiUrl: '',
        onlineEnabled: false,
        enabled: true,
        ready: true,
        entryCount: 450000,
        fileSize: 52428800,
        lastUpdatedAt: '2026-11-25T09:10:00Z',
        lastError: '',
      },
      {
        id: 'src-bgm-zh',
        repoId: 'repo-1',
        sourceKey: 'bgm-zh',
        kind: 'bangumi_archive',
        schemaType: 'bangumi_archive',
        name: 'Bangumi 中文增强库',
        enabled: true,
        ready: true,
        entryCount: 50000,
        autoUpdate: 'weekly',
        tagDisplayMode: 'zh',
      },
    ],
    libraryChains: {
      'lib-1': ['src-sqlite-1'],
    },
    activeSyncs: [],
    onlineEnrichStatus: {
      running: false,
      circuitOpen: false,
      cooldownUntil: '',
      processed: 0,
      matched: 0,
      failed: 0,
      lastError: '',
    },
  }, overrides);
}

const sampleLibraries = [
  { id: 'lib-1', name: '本地漫画库', providerType: 'local', enabled: true },
  { id: 'lib-2', name: 'WebDAV 书库', providerType: 'webdav', enabled: true },
  { id: 'lib-union', name: '联合书库', providerType: 'union', enabled: true },
];

test('recommended repo pre-fill populates input without auto-submitting, then subscribes on confirm', async () => {
  const requests = [];
  const h = loadAdminScraperContext({
    apiImpl: async (url, opts = {}) => {
      requests.push([url, opts.method || 'GET', opts.body ? JSON.parse(JSON.stringify(opts.body)) : null]);
      if (url === '/admin/api/bangumi/status') return sampleBangumiStatus();
      if (url.startsWith('/admin/api/bangumi/runs')) return [];
      if (url === '/admin/api/libraries') return sampleLibraries;
      if (url.startsWith('/admin/api/bangumi/tracker')) return {};
      if (url === '/admin/api/metadata/overview') return sampleOverview({ repos: [] });
      if (url === '/admin/api/metadata/scrape-runs') return [];
      return {};
    },
  });

  h.paintAdminBangumi(h.body, sampleBangumiStatus(), [], sampleLibraries, {}, sampleOverview({ repos: [] }), []);
  assert.equal(h.pollCalls.length, 0, 'idle page must not schedule polling');
  assert.ok(h.getStopPollCalls() >= 1, 'idle page must call stopPolling');

  const repoInput = h.doc.getElementById('extmeta-repo-url');
  const fillBtn = h.doc.getElementById('extmeta-repo-fill-rec');
  const subBtn = h.doc.getElementById('extmeta-repo-subscribe-btn');

  assert.equal(repoInput.value, '');
  fillBtn.onclick();
  assert.equal(
    repoInput.value,
    'https://raw.githubusercontent.com/BlackMuseCode/mangaalpha-metadata-repo/main/repo.json',
  );
  assert.equal(requests.length, 0, 'filling recommended URL must NOT auto-submit');

  await subBtn.onclick();
  assert.deepEqual(requests[0], [
    '/admin/api/metadata/repos',
    'POST',
    { url: 'https://raw.githubusercontent.com/BlackMuseCode/mangaalpha-metadata-repo/main/repo.json' },
  ]);
});

test('source config modal, custom source creation, and sync invoke exact API contracts', async () => {
  const requests = [];
  const h = loadAdminScraperContext({
    apiImpl: async (url, opts = {}) => {
      requests.push([url, opts.method || 'GET', opts.body ? JSON.parse(JSON.stringify(opts.body)) : null]);
      if (url === '/admin/api/bangumi/status') return sampleBangumiStatus();
      if (url.startsWith('/admin/api/bangumi/runs')) return [];
      if (url === '/admin/api/libraries') return sampleLibraries;
      if (url.startsWith('/admin/api/bangumi/tracker')) return {};
      if (url === '/admin/api/metadata/overview') return sampleOverview();
      if (url === '/admin/api/metadata/scrape-runs') return [];
      return {};
    },
  });

  h.paintAdminBangumi(h.body, sampleBangumiStatus(), [], sampleLibraries, {}, sampleOverview(), []);

  // 1. Edit existing source config
  const editBtns = h.body.querySelectorAll('button[data-extmeta-src-edit="src-sqlite-1"]');
  assert.equal(editBtns.length, 1);
  editBtns[0].onclick();

  h.doc.getElementById('ems-enabled').checked = false;
  h.doc.getElementById('ems-tag-dict').value = 'src-dict';
  h.doc.getElementById('ems-tag-mode').value = 'bilingual';
  h.doc.getElementById('ems-auto-update').value = 'weekly';
  h.doc.getElementById('ems-local-path').value = '/data/metadata/custom.sqlite';
  h.doc.getElementById('ems-download-url').value = 'https://example.test/v2.sqlite.zst';
  h.doc.getElementById('ems-online-url').value = 'https://api.example.test/lookup';
  h.doc.getElementById('ems-online-enabled').checked = true;
  await h.doc.getElementById('ems-save-btn').onclick();

  assert.deepEqual(requests[0], [
    '/admin/api/metadata/sources/src-sqlite-1',
    'PUT',
    {
      enabled: false,
      tagDictSourceId: 'src-dict',
      tagDisplayMode: 'bilingual',
      autoUpdate: 'weekly',
      localPath: '/data/metadata/custom.sqlite',
      downloadUrl: 'https://example.test/v2.sqlite.zst',
      onlineApiUrl: 'https://api.example.test/lookup',
      onlineEnabled: true,
    },
  ]);

  // 2. Add custom source
  requests.length = 0;
  h.doc.getElementById('extmeta-source-add-btn').onclick();
  h.doc.getElementById('emas-name').value = '本地单表源';
  h.doc.getElementById('emas-kind').value = 'offline_sqlite';
  h.doc.getElementById('emas-schema').value = 'mangaalpha_meta_v1';
  h.doc.getElementById('emas-download-url').value = 'https://example.test/meta.sqlite.zst';
  h.doc.getElementById('emas-local-path').value = '/data/meta.sqlite';
  h.doc.getElementById('emas-online-url').value = '';
  h.doc.getElementById('emas-tag-dict').value = 'src-dict';
  h.doc.getElementById('emas-tag-mode').value = 'zh';
  h.doc.getElementById('emas-auto-update').value = 'monthly';
  h.doc.getElementById('emas-enabled').checked = true;
  await h.doc.getElementById('emas-create-btn').onclick();

  assert.deepEqual(requests[0], [
    '/admin/api/metadata/sources',
    'POST',
    {
      name: '本地单表源',
      kind: 'offline_sqlite',
      schemaType: 'mangaalpha_meta_v1',
      downloadUrl: 'https://example.test/meta.sqlite.zst',
      localPath: '/data/meta.sqlite',
      onlineApiUrl: '',
      tagDictSourceId: 'src-dict',
      tagDisplayMode: 'zh',
      autoUpdate: 'monthly',
      enabled: true,
    },
  ]);

  // 3. Force sync
  requests.length = 0;
  const syncBtns = h.body.querySelectorAll('button[data-extmeta-src-sync="src-sqlite-1"]');
  const forceBtn = syncBtns.find((b) => b.getAttribute('data-force') === 'true');
  assert.ok(forceBtn);
  await forceBtn.onclick();
  assert.deepEqual(requests[0], [
    '/admin/api/metadata/sources/src-sqlite-1/sync',
    'POST',
    { force: true },
  ]);
});

test('library priority chain modal reorders sources and saves ordered sourceIds', async () => {
  const requests = [];
  const h = loadAdminScraperContext({
    apiImpl: async (url, opts = {}) => {
      requests.push([url, opts.method || 'GET', opts.body ? JSON.parse(JSON.stringify(opts.body)) : null]);
      if (url === '/admin/api/bangumi/status') return sampleBangumiStatus();
      if (url.startsWith('/admin/api/bangumi/runs')) return [];
      if (url === '/admin/api/libraries') return sampleLibraries;
      if (url.startsWith('/admin/api/bangumi/tracker')) return {};
      if (url === '/admin/api/metadata/overview') return sampleOverview();
      if (url === '/admin/api/metadata/scrape-runs') return [];
      return {};
    },
  });

  h.paintAdminBangumi(h.body, sampleBangumiStatus(), [], sampleLibraries, {}, sampleOverview(), []);

  // Only physical libraries should have chain edit buttons (not union library)
  assert.equal(h.body.querySelectorAll('button[data-extmeta-chain-edit="lib-union"]').length, 0);
  const editChainBtn = h.body.querySelector('button[data-extmeta-chain-edit="lib-1"]');
  assert.ok(editChainBtn);
  editChainBtn.onclick();

  // Currently selected: ['src-sqlite-1']. Check 'src-bgm-zh' then move it up to #1.
  const bgmCb = h.modalBody.querySelector('input[data-mchain-id="src-bgm-zh"]');
  assert.ok(bgmCb);
  bgmCb.checked = true;
  bgmCb.onchange();

  const moveUpBtn = h.modalBody.querySelectorAll('button[data-mchain-move="src-bgm-zh"]')
    .find((b) => b.getAttribute('data-dir') === '-1');
  assert.ok(moveUpBtn);
  moveUpBtn.onclick();

  await h.doc.getElementById('mchain-save').onclick();
  assert.deepEqual(requests[0], [
    '/admin/api/libraries/lib-1/metadata-sources',
    'PUT',
    { sourceIds: ['src-bgm-zh', 'src-sqlite-1'] },
  ]);
});

test('multi-source batch scrape defaults allowOnline=false and polls ONLY while running', async () => {
  const requests = [];
  const h = loadAdminScraperContext({
    apiImpl: async (url, opts = {}) => {
      requests.push([url, opts.method || 'GET', opts.body ? JSON.parse(JSON.stringify(opts.body)) : null]);
      if (url === '/admin/api/bangumi/status') return sampleBangumiStatus();
      if (url.startsWith('/admin/api/bangumi/runs')) return [];
      if (url === '/admin/api/libraries') return sampleLibraries;
      if (url.startsWith('/admin/api/bangumi/tracker')) return {};
      if (url === '/admin/api/metadata/overview') return sampleOverview();
      if (url === '/admin/api/metadata/scrape-runs') return [];
      return {};
    },
  });

  // Active sync triggers polling & displays progress bar
  const runningOverview = sampleOverview({
    activeSyncs: [{
      jobId: 'sync-1',
      sourceId: 'src-sqlite-1',
      status: 'running',
      phase: 'building_sidecar',
      downloadedBytes: 512,
      totalBytes: 1024,
      indexedEntries: 1200,
      error: '',
    }],
  });
  h.paintAdminBangumi(h.body, sampleBangumiStatus(), [], sampleLibraries, {}, runningOverview, []);
  assert.equal(h.pollCalls.length, 1, 'active sync must schedule polling');
  assert.match(h.body.innerHTML, /building_sidecar/);
  assert.match(h.body.innerHTML, /50%/);

  // Batch scrape defaults to allowOnline = false
  const allowOnlineCb = h.doc.getElementById('extmeta-batch-online');
  assert.equal(allowOnlineCb.checked, false);

  h.doc.getElementById('extmeta-batch-lib').value = 'lib-1';
  h.doc.getElementById('extmeta-batch-mode').value = 'retry_unmatched';
  await h.doc.getElementById('extmeta-batch-start').onclick();

  assert.deepEqual(requests[0], [
    '/admin/api/metadata/scrape',
    'POST',
    { libraryId: 'lib-1', mode: 'retry_unmatched', allowOnline: false },
  ]);

  // Idle state cancels polling
  const stopBefore = h.getStopPollCalls();
  h.paintAdminBangumi(h.body, sampleBangumiStatus(), [], sampleLibraries, {}, sampleOverview(), []);
  assert.equal(h.pollCalls.length, 1, 'idle repaint must not add new poll timer');
  assert.ok(h.getStopPollCalls() > stopBefore, 'idle repaint must stop polling');
});

test('sanitized mapping export defaults includeNsfw=false and invokes preview:true before download', async () => {
  const requests = [];
  const previewPayload = {
    count: 2,
    excludedNsfw: 5,
    skippedPrivacy: 1,
    sample: [
      {
        schema: 'mangaalpha_mapping_v1',
        key: '水星领航员',
        rawKey: 'ARIA',
        author: '天野梢',
        targetSource: 'bangumi',
        targetId: '539',
        isNsfw: false,
        origin: 'manual',
      },
      {
        schema: 'mangaalpha_mapping_v1',
        key: '魔法少女',
        rawKey: 'Mahou Shoujo',
        author: '作者A',
        targetSource: 'mangaalpha_meta_v1',
        targetId: 'm-1',
        isNsfw: false,
        origin: 'agent',
      },
    ],
    ndjson: '{"schema":"mangaalpha_mapping_v1","key":"水星领航员","targetSource":"bangumi","targetId":"539"}\n',
  };

  const h = loadAdminScraperContext({
    apiImpl: async (url, opts = {}) => {
      requests.push([url, opts.method || 'GET', opts.body ? JSON.parse(JSON.stringify(opts.body)) : null]);
      if (url === '/admin/api/metadata/mappings/export') return previewPayload;
      return {};
    },
  });

  h.paintAdminBangumi(h.body, sampleBangumiStatus(), [], sampleLibraries, {}, sampleOverview(), []);

  const includeNsfwCb = h.doc.getElementById('extmeta-export-include-nsfw');
  assert.equal(includeNsfwCb.checked, false, 'includeNsfw must default to unchecked');

  const originCbs = h.body.querySelectorAll('input[data-extmeta-export-origin]');
  const checkedOrigins = originCbs.filter((c) => c.checked).map((c) => c.value);
  assert.deepEqual(checkedOrigins, ['manual', 'agent']);

  await h.doc.getElementById('extmeta-export-preview-btn').onclick();
  assert.deepEqual(requests[0], [
    '/admin/api/metadata/mappings/export',
    'POST',
    {
      libraryIds: [],
      origins: ['manual', 'agent'],
      includeNsfw: false,
      preview: true,
    },
  ]);

  assert.match(h.modalBody.innerHTML, /可导出脱敏条数：2/);
  assert.match(h.modalBody.innerHTML, /已排除 NSFW：5/);
  assert.match(h.modalBody.innerHTML, /水星领航员/);

  await h.doc.getElementById('extmeta-export-download-btn').onclick();
  assert.equal(h.blobDownloads.length, 1);
  assert.equal(h.createdAnchors.length, 1);
  assert.equal(h.createdAnchors[0].download, 'mangaalpha-mappings.ndjson');
});

test('series match modal switches to ready external source, queries candidates, binds, and unbinds', async () => {
  const modalStart = appSource.indexOf('  // --- Bangumi Match Modal ---');
  const modalEnd = appSource.indexOf('  // --- Metadata Edit Modal ---', modalStart);
  const detailStart = appSource.indexOf('  // --- Series Detail View (/series/:id) ---');
  assert.ok(detailStart >= 0 && modalStart > detailStart && modalEnd > modalStart);

  const requests = [];
  const { doc, makeContainer } = createFakeDOM();
  const pageContent = makeContainer('page-content');
  const modalSubhead = makeContainer('modal-subhead');
  const modalBody = makeContainer('modal-body');
  const modalFoot = makeContainer('modal-foot');
  let detailRenders = 0;

  const ctx = vm.createContext({
    document: doc,
    location: { origin: 'http://127.0.0.1:25600', pathname: '/series/ser-1', search: '' },
    routeSeq: 1,
    state: {
      auth: { userId: 'admin', isAdmin: true },
      coverVer: 1,
      taxonomies: { loaded: true },
      extSourceNames: {},
    },
    isAdminUser: () => true,
    renderShell: () => { detailRenders += 1; },
    providerLabel: (p) => p || 'local',
    renderSeriesTrackerBadge: () => '',
    loadSeriesVersions: () => {},
    loadSeriesCollectionsBar: () => {},
    bindMaintCancelButtons: () => {},
    openMetadataEditModal: () => {},
    openAddSeriesToCollectionModal: () => {},
    openAddBookToReadListModal: () => {},
    schedulePoll: () => {},
    stopPolling: () => {},
    fmtBytes: (n) => `${n} B`,
    esc: (v) => (v == null ? '' : String(v)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;')),
    toast: () => {},
    confirmModal: async () => true,
    openModal: (opts) => {
      modalSubhead.innerHTML = opts.subheadHTML || '';
      modalBody.innerHTML = opts.bodyHTML || '';
      modalFoot.innerHTML = opts.footHTML || '';
      if (opts.onMount) opts.onMount(modalBody);
    },
    closeModal: () => {},
    api: async (url, opts = {}) => {
      requests.push([url, opts.method || 'GET', opts.body ? JSON.parse(JSON.stringify(opts.body)) : null]);
      if (url === '/admin/api/metadata/overview') {
        return sampleOverview();
      }
      if (url === '/admin/api/series/ser-1/detail') {
        return detail;
      }
      if (url.startsWith('/admin/api/maintenance-jobs')) {
        return [];
      }
      if (url.startsWith('/admin/api/series/ser-1/bangumi/candidates')) {
        return { parsed: { candidates: ['测试作品'] }, candidates: [] };
      }
      if (url.startsWith('/admin/api/series/ser-1/metadata/candidates')) {
        return {
          sourceId: 'src-sqlite-1',
          query: 'https://gallery.example.test/g/12345/abc/',
          candidates: [
            {
              sourceId: 'src-sqlite-1',
              entryId: '12345',
              title: '测试外部条目',
              titleAlt: 'Test External Work',
              authors: ['画师甲'],
              category: 'Non-H',
              tags: ['中文译名标签', '全彩'],
              summary: '外部条目的简介内容',
              coverUrl: 'https://cdn.example.test/thumb/12345.jpg',
              rating: 4.7,
              isNsfw: false,
              score: 0.96,
              matchReason: 'exact_id',
            },
          ],
        };
      }
      return {};
    },
  });

  const { paintSeriesDetail, openBangumiMatchModal } = vm.runInContext(
    appSource.slice(detailStart, modalEnd) + '\n({ paintSeriesDetail, openBangumiMatchModal });',
    ctx,
  );

  const detail = {
    series: { id: 'ser-1', oneshot: false, booksCount: 2, booksReadCount: 0, booksInProgressCount: 0, metadata: { title: '测试作品', status: 'ONGOING', genres: [], tags: [], alternateTitles: [] } },
    rawMeta: { bgmSubjectId: 0, extSourceId: 'src-sqlite-1', extEntryId: '12345', matchOrigin: 'manual', authors: [] },
    originalName: '测试作品',
    relativePath: '测试作品',
    totalSize: '12 MB',
    library: { id: 'lib-1', name: '本地漫画库', providerType: 'local' },
    books: [],
    bookExtras: {},
  };

  // 1. Series detail shows external source badge & unbind button
  paintSeriesDetail('ser-1', detail, []);
  assert.match(pageContent.innerHTML, /扩展源：/);
  const clearDetailBtn = doc.getElementById('btn-clear-ext-meta');
  assert.ok(clearDetailBtn);
  await clearDetailBtn.onclick();
  assert.deepEqual(requests[0], ['/admin/api/series/ser-1/metadata/bind', 'DELETE', null]);

  // 2. Match modal opens on Bangumi default, discovers ready offline source, switches, queries & binds
  openBangumiMatchModal('ser-1', detail);
  await flushMicrotasks();

  const sourceWrap = doc.getElementById('bgm-source-switcher-wrap');
  const sourceSel = doc.getElementById('bgm-source-select');
  assert.equal(sourceWrap.style.display, '');
  assert.equal(sourceSel.value, 'bangumi');

  requests.length = 0;
  sourceSel.value = 'src-sqlite-1';
  doc.getElementById('bgm-search-q').value = 'https://gallery.example.test/g/12345/abc/';
  sourceSel.onchange();
  await flushMicrotasks();

  assert.equal(
    requests[0][0],
    '/admin/api/series/ser-1/metadata/candidates?sourceId=src-sqlite-1&q=https%3A%2F%2Fgallery.example.test%2Fg%2F12345%2Fabc%2F',
  );
  const candidatesHTML = doc.getElementById('bgm-candidates-box').innerHTML;
  assert.match(candidatesHTML, /\/admin\/api\/metadata\/sources\/src-sqlite-1\/entries\/12345\/cover\?u=/);
  assert.match(candidatesHTML, /中文译名标签/);
  assert.match(candidatesHTML, /匹配度 96%/);

  const bindBtn = modalBody.querySelector('button[data-bind-ext-entry="12345"]');
  assert.ok(bindBtn);
  await bindBtn.onclick();
  assert.deepEqual(requests[1], [
    '/admin/api/series/ser-1/metadata/bind',
    'POST',
    { sourceId: 'src-sqlite-1', entryId: '12345', origin: 'manual' },
  ]);

  // 3. Modal unbind button in fresh open modal calls DELETE /metadata/bind
  openBangumiMatchModal('ser-1', detail);
  await flushMicrotasks();
  requests.length = 0;
  const clearModalBtn = doc.getElementById('extmeta-clear-btn');
  assert.ok(clearModalBtn);
  await clearModalBtn.onclick();
  assert.deepEqual(requests[0], ['/admin/api/series/ser-1/metadata/bind', 'DELETE', null]);
  assert.ok(detailRenders >= 3);
});

test('app.css external metadata classes use theme variables without hardcoded colors', () => {
  const idx = cssSource.indexOf('/* External Metadata & Multi-Source Scraper UI */');
  assert.ok(idx >= 0, 'expected new CSS section in app.css');
  const addedCSS = cssSource.slice(idx);
  assert.doesNotMatch(addedCSS, /#[0-9a-fA-F]{3,8}\b|rgb\(|rgba\(/);
});
