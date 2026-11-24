const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { test } = require('node:test');
const vm = require('node:vm');

const source = readFileSync('internal/httpserver/static/ui/app.js', 'utf8');
const start = source.indexOf('  async function browse115Drawer(');
const end = source.indexOf('  // --- Admin Bangumi Scraper Center ---', start);
assert.ok(start >= 0 && end > start);

function createDrawerHarness() {
  const clicks = new Map();
  const drawer = {
    isConnected: true,
    html: '',
    style: {},
    get innerHTML() { return this.html; },
    set innerHTML(val) { this.html = val; clicks.clear(); },
    querySelectorAll(selector) {
      const attr = selector.slice(1, -1);
      const regex = new RegExp(`${attr}="([^"]*)"(?:[^>]*data-b115-name="([^"]*)")?`, 'g');
      const items = [...this.html.matchAll(regex)].map((m) => ({
        getAttribute: (k) => (k === attr ? m[1] : (k === 'data-b115-name' ? m[2] : null)),
      }));
      clicks.set(attr, items);
      return items;
    },
  };
  return { drawer, clicks };
}

test('115 folder browser opens subfolders via f.id, navigates back, and picks folders', async () => {
  const fixtures = new Map([
    ['0', { cid: '0', folders: [{ id: '1001', name: '漫画库' }, { id: '', name: '损坏目录' }] }],
    ['1001', { cid: '1001', folders: [{ id: '2002', name: '单行本' }] }],
    ['2002', { cid: '2002', folders: [] }],
  ]);

  const { drawer, clicks } = createDrawerHarness();
  const cidInput = { value: '' };
  const nameInput = { value: '' };
  const upBtn = {};
  const pickCurBtn = {};
  const errUpBtn = {};
  const requested = [];

  const ctx = vm.createContext({
    document: {
      getElementById: (id) => ({
        'nl-browser-drawer': drawer,
        'nl-cid': cidInput,
        'nl-name': nameInput,
        'b115-up': drawer.html.includes('id="b115-up"') ? upBtn : null,
        'b115-pick-cur': drawer.html.includes('id="b115-pick-cur"') ? pickCurBtn : null,
        'b115-err-up': drawer.html.includes('id="b115-err-up"') ? errUpBtn : null,
      })[id] || null,
    },
    esc: (v) => (v == null ? '' : String(v)),
    api: async (url) => {
      const parsed = new URL(url, 'http://test.invalid');
      assert.equal(parsed.pathname, '/admin/api/115/folders');
      const cid = parsed.searchParams.get('cid');
      requested.push(cid);
      assert.ok(fixtures.has(cid), `unexpected cid requested: ${cid}`);
      return fixtures.get(cid);
    },
  });

  const { browse115Drawer } = vm.runInContext(
    source.slice(start, end) + '\n({ browse115Drawer, browseLocalDrawer, browseWebDAVDrawer });',
    ctx,
  );

  await browse115Drawer('0', [{ cid: '0', name: '115 根目录' }]);
  assert.equal(clicks.get('data-b115-open').length, 1);
  assert.match(drawer.html, /\(1001\)/);
  assert.doesNotMatch(drawer.html, /损坏目录/);

  // Click into child folder 1001 -> must NOT jump back to 0!
  await clicks.get('data-b115-open')[0].onclick();
  assert.match(drawer.html, /115 根目录 \/ 漫画库/);
  assert.match(drawer.html, /\(CID: 1001\)/);
  assert.match(drawer.html, /\(2002\)/);

  // Drill into leaf folder 2002 and pick current directory
  await clicks.get('data-b115-open')[0].onclick();
  assert.match(drawer.html, /此目录下没有子文件夹/);
  pickCurBtn.onclick();
  assert.equal(cidInput.value, '2002');
  assert.equal(nameInput.value, '单行本');
  assert.equal(drawer.style.display, 'none');

  // Navigate back up to 1001 and pick child 2002 directly
  cidInput.value = '';
  nameInput.value = '';
  await upBtn.onclick();
  clicks.get('data-b115-pick')[0].onclick();
  assert.equal(cidInput.value, '2002');
  assert.equal(nameInput.value, '单行本');
  assert.deepEqual(requested, ['0', '1001', '2002', '1001']);
});

test('late 115 folder response does not overwrite local picker or cleared drawer', async () => {
  const { drawer } = createDrawerHarness();
  const localPathInput = { value: '' };
  const nameInput = { value: '' };
  const pending = [];

  const ctx = vm.createContext({
    document: {
      getElementById: (id) => ({
        'nl-browser-drawer': drawer,
        'nl-local-path': localPathInput,
        'nl-name': nameInput,
        'bloc-pick-cur': {},
      })[id] || null,
    },
    esc: (v) => (v == null ? '' : String(v)),
    api: (url) => new Promise((resolve, reject) => {
      pending.push({ url, resolve, reject });
    }),
  });

  const { browse115Drawer, browseLocalDrawer } = vm.runInContext(
    source.slice(start, end) + '\n({ browse115Drawer, browseLocalDrawer });',
    ctx,
  );

  // Start slow 115 fetch, then switch to local picker which resolves first.
  const slow115 = browse115Drawer('0', [{ cid: '0', name: '115 根目录' }]);
  const fastLocal = browseLocalDrawer('/manga');
  assert.equal(pending.length, 2);

  pending[1].resolve({ current: '/manga', parent: '/', folders: [{ name: 'LocalComics', path: '/manga/LocalComics' }] });
  await fastLocal;
  assert.match(drawer.html, /LocalComics/);

  // Late 115 response must be ignored rather than overwriting the visible local picker.
  pending[0].resolve({ cid: '0', folders: [{ id: '9999', name: 'Stale115Dir' }] });
  await slow115;
  assert.match(drawer.html, /LocalComics/);
  assert.doesNotMatch(drawer.html, /Stale115Dir/);

  // Provider switch invalidates drawer._browseSeq so an in-flight request cannot repaint.
  const staleAfterSwitch = browse115Drawer('0', [{ cid: '0', name: '115 根目录' }]);
  drawer._browseSeq += 1;
  drawer.style.display = 'none';
  drawer.innerHTML = '';
  pending[2].resolve({ cid: '0', folders: [{ id: '8888', name: 'AfterSwitchDir' }] });
  await staleAfterSwitch;
  assert.equal(drawer.html, '');
  assert.equal(drawer.style.display, 'none');
});
