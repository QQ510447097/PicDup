/* PicDup frontend — vanilla ES module, no build step. */

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

const PHASE_LABELS = {
  idle: '准备就绪',
  discovering: '正在发现图片',
  analyzing: '正在分析视觉指纹',
  comparing: '正在比对与分组',
  finalizing: '正在整理结果',
  done: '扫描完成',
  cancelled: '扫描已取消',
  error: '扫描失败',
};

const KIND_LABELS = { exact: '完全重复', similar: '相似图片' };
const SORT_LABELS = { wasted: '可释放空间', count: '文件数量', name: '名称' };

const state = {
  boot: null,
  desktop: false,
  settings: null,
  roots: loadRoots(),
  view: 'home',
  homePhase: 'idle',
  status: null,
  pollTimer: null,
  scanHandled: true,
  result: null,
  counts: { exactGroups: 0, similarGroups: 0, exactFiles: 0, similarFiles: 0, reclaimExact: 0, reclaimSimilar: 0 },
  stats: null,
  kind: 'exact',
  sort: 'wasted',
  query: '',
  groups: [],
  total: 0,
  offset: 0,
  limit: 24,
  carouselIndex: 0,
  carouselWheelAcc: 0,
  carouselWheelLock: 0,
  loadingResults: false,
  resultsSeq: 0,
  searchTimer: null,
  selection: new Set(),
  selectionBytes: 0,
  candidateSizes: new Map(),
  candidateNames: new Map(),
  groupById: new Map(),
  activeGroup: null,
  previewPath: null,
  groupCleanupPending: false,
  compareMode: 'single',
  compareZoom: 1,
  comparePanX: 0,
  comparePanY: 0,
  compareDrag: null,
  compareBasePath: null,
  compareTargetPath: null,
  trash: null,
  trashLoading: false,
  trashSummary: { batches: 0, files: 0, bytes: 0 },
  cache: { files: 0, bytes: 0, fingerprintFiles: 0, fingerprintBytes: 0 },
  settingsSaveTimer: null,
  droppedTimer: null,
  cleanupBusy: false,
  overlayOpen: false,
  lastFocus: null,
};

/* ---------------- helpers ---------------- */

function esc(value) {
  return String(value ?? '').replace(/[&<>"']/g, (ch) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[ch]));
}

function icon(name, cls = '') {
  return `<svg class="icon ${cls}" aria-hidden="true"><use href="#i-${name}"/></svg>`;
}

function fmtBytes(bytes) {
  const n = Number(bytes) || 0;
  if (n <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.min(units.length - 1, Math.floor(Math.log(n) / Math.log(1024)));
  const value = n / Math.pow(1024, i);
  const digits = i === 0 ? 0 : value >= 100 ? 0 : value >= 10 ? 1 : 1;
  return `${value.toFixed(digits)} ${units[i]}`;
}

function fmtCount(n) {
  return new Intl.NumberFormat('zh-CN').format(Number(n) || 0);
}

function fmtTime(input, full = false) {
  if (!input) return '—';
  const d = new Date(input);
  if (Number.isNaN(d.getTime())) return '—';
  const opts = full
    ? { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }
    : { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false };
  return d.toLocaleString('zh-CN', opts).replace(/\//g, '/');
}

function fmtDuration(ms) {
  const raw = Math.max(0, Number(ms) || 0);
  if (raw < 1000) return raw < 100 ? `${Math.max(1, Math.round(raw))} 毫秒` : `${(raw / 1000).toFixed(2)} 秒`;
  const s = Math.round(raw / 1000);
  if (s < 60) return `${s} 秒`;
  const m = Math.floor(s / 60);
  const rest = s % 60;
  return `${m} 分 ${rest} 秒`;
}

function basename(p) {
  const parts = String(p || '').split(/[\\/]/).filter(Boolean);
  return parts.length ? parts[parts.length - 1] : String(p || '');
}

function dirname(p) {
  const s = String(p || '');
  const idx = Math.max(s.lastIndexOf('\\'), s.lastIndexOf('/'));
  return idx > 0 ? s.slice(0, idx) : s;
}

function debounce(fn, wait) {
  let timer = null;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), wait);
  };
}

function similarityPreset(value) {
  const v = Number(value) || 0;
  if (v <= 5) return '严格';
  if (v <= 10) return '平衡';
  return '宽松';
}

function similarityPercentOf(value) {
  // The pHash hamming threshold maps to a rough perceptual similarity score.
  const v = Math.max(0, Math.min(24, Number(value) || 0));
  return Math.round((1 - v / 64) * 100);
}

function loadRoots() {
  try {
    const parsed = JSON.parse(localStorage.getItem('picdup.roots') || '[]');
    const list = Array.isArray(parsed) ? parsed.filter((p) => typeof p === 'string' && p.trim()) : [];
    return list.slice(0, 1);
  } catch {
    return [];
  }
}

function saveRoots() {
  try {
    localStorage.setItem('picdup.roots', JSON.stringify(state.roots.slice(0, 1)));
  } catch {
    /* ignore quota errors */
  }
}

function currentRoot() {
  return state.roots[0] || '';
}

function rootLabel() {
  return currentRoot() || '尚未选择文件夹';
}

function selectionPaths() {
  return Array.from(state.selection);
}

function lookupSize(path) {
  return state.candidateSizes.get(path) || 0;
}

function lookupName(path) {
  return state.candidateNames.get(path) || basename(path);
}

/* ---------------- API ---------------- */

async function request(path, { method = 'GET', body, timeout = 30000 } = {}) {
  let timer = null;
  let controller = null;
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  if (timeout > 0) {
    controller = new AbortController();
    opts.signal = controller.signal;
    timer = setTimeout(() => controller.abort(), timeout);
  }
  try {
    const res = await fetch(path, opts);
    const text = await res.text();
    let data = null;
    if (text) {
      try { data = JSON.parse(text); } catch { data = text; }
    }
    if (!res.ok) {
      const message = (data && (data.error || data.message)) || `请求失败 (${res.status})`;
      throw new Error(message);
    }
    return data;
  } catch (err) {
    if (err.name === 'AbortError') throw new Error('请求超时，请重试');
    if (err instanceof TypeError) throw new Error('无法连接本地服务，请重新启动 PicDup');
    throw err;
  } finally {
    if (timer) clearTimeout(timer);
  }
}

/* ---------------- boot & chrome ---------------- */

async function init() {
  bindEvents();
  applyThemeFromCache();
  try {
    const boot = await request('/api/bootstrap');
    state.boot = boot;
    state.desktop = Boolean(boot.desktop);
    state.settings = boot.settings;
    state.status = boot.status;
    state.trashSummary = boot.trash || state.trashSummary;
    state.cache = boot.cache || state.cache;
    state.counts = boot.counts || state.counts;
    state.result = boot.result || null;
    state.stats = boot.result ? boot.result.stats : null;
    if (!state.roots.length && boot.result && Array.isArray(boot.result.roots)) {
      state.roots = boot.result.roots.slice(0, 1);
      saveRoots();
    }
    if (boot.result && boot.counts) {
      if (boot.counts.exactGroups === 0 && boot.counts.similarGroups > 0) state.kind = 'similar';
      if (boot.counts.similarGroups === 0 && boot.counts.exactGroups > 0) state.kind = 'exact';
    }
    applyTheme(state.settings.theme, false);
    state.scanHandled = !boot.status?.scanning;
    state.homePhase = boot.status?.scanning ? 'scanning' : 'idle';
    renderSidebar();
    updateChrome();

    if (boot.status?.scanning) {
      setView('home');
      startPolling();
    } else if (boot.result) {
      setView('results');
      loadResults(true);
    } else {
      setView('home');
    }
    if (state.desktop) {
      // The Wails runtime registers platform drag/drop handling. It is only
      // needed in the desktop window; the browser/server build stays standalone.
      import('/wails/runtime.js').catch(() => {});
      startDroppedPolling();
    }
  } catch (err) {
    showBootError(err);
    return;
  }
  const bootEl = $('#boot');
  const appEl = $('#app');
  appEl.hidden = false;
  if (bootEl) {
    bootEl.classList.add('hide');
    setTimeout(() => bootEl.remove(), 420);
  }
}

function showBootError(err) {
  const bootEl = $('#boot');
  if (!bootEl) return;
  bootEl.innerHTML = `
    <div class="boot-mark" style="animation:none;opacity:.9">${icon('alert')}</div>
    <div class="boot-text" style="max-width:460px;text-align:center;line-height:1.7;color:var(--red)">
      无法载入 PicDup：${esc(err.message || err)}
    </div>
    <button class="btn btn-primary" data-action="retry-init" type="button">重新连接</button>
  `;
  bindEvents();
}

function applyThemeFromCache() {
  try {
    const theme = localStorage.getItem('picdup.theme');
    if (theme === 'light' || theme === 'dark') document.documentElement.dataset.theme = theme;
  } catch { /* ignore */ }
}

function resolvedTheme(theme) {
  if (theme === 'system') {
    return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  }
  return theme === 'light' ? 'light' : 'dark';
}

function applyTheme(theme, persist = true) {
  const resolved = resolvedTheme(theme);
  document.documentElement.dataset.theme = resolved;
  document.documentElement.style.colorScheme = resolved;
  const meta = $('meta[name="theme-color"]');
  if (meta) meta.content = resolved === 'dark' ? '#0b1220' : '#f1f4fa';
  const btn = $('#btn-theme');
  if (btn) {
    btn.innerHTML = icon(resolved === 'dark' ? 'moon' : 'sun');
    btn.title = resolved === 'dark' ? '切换到浅色主题' : '切换到深色主题';
  }
  if (persist) {
    try { localStorage.setItem('picdup.theme', theme); } catch { /* ignore */ }
  }
}

function viewMeta(view) {
  const trash = state.trashSummary || { files: 0, bytes: 0 };
  switch (view) {
    case 'results':
      return { crumb: '扫描结果', sub: state.result ? `${state.result.roots?.[0] || rootLabel()} · ${fmtTime(state.result.finishedAt, true)}` : '扫描结果' };
    case 'trash':
      return { crumb: '隔离区', sub: `${fmtCount(trash.files)} 个文件 · ${fmtBytes(trash.bytes)} · 文件暂存于此，可随时恢复` };
    case 'settings':
      return { crumb: '设置', sub: '扫描偏好与清理方式，修改会自动保存' };
    default: {
      if (state.homePhase === 'scanning') return { crumb: '正在扫描', sub: rootLabel() };
      if (state.homePhase === 'complete') return { crumb: '扫描完成', sub: `${rootLabel()} · 结果已保存在本机` };
      return { crumb: '开始扫描', sub: '一次扫描一个文件夹，所有处理都在本机完成' };
    }
  }
}

function updateTopbarActions() {
  const slot = $('#topbar-view-actions');
  if (!slot) return;
  if (state.view === 'results') {
    slot.innerHTML = `
      <button class="btn btn-sm" type="button" data-action="adjust-home">${icon('sliders')} 调整范围</button>
      <button class="btn btn-sm btn-primary" type="button" data-action="rescan">${icon('refresh')} 重新扫描</button>
    `;
    return;
  }
  if (state.view === 'trash') {
    const files = state.trashSummary?.files || 0;
    slot.innerHTML = `<button class="btn btn-sm btn-danger" type="button" data-action="empty-trash" ${files ? '' : 'disabled'}>${icon('broom')} 清空隔离区</button>`;
    return;
  }
  slot.innerHTML = '';
}

function updateChrome() {
  const meta = viewMeta(state.view);
  const crumb = $('#crumb');
  const sub = $('#topbar-sub');
  if (crumb) crumb.textContent = meta.crumb;
  if (sub) sub.textContent = meta.sub;
  $$('.nav-item[data-nav]').forEach((btn) => {
    btn.classList.toggle('active', btn.dataset.nav === state.view);
    btn.setAttribute('aria-current', btn.dataset.nav === state.view ? 'page' : 'false');
  });
  const resultsBadge = $('#nav-results-badge');
  if (resultsBadge) {
    const totalGroups = (state.counts.exactGroups || 0) + (state.counts.similarGroups || 0);
    resultsBadge.hidden = totalGroups === 0;
    resultsBadge.textContent = fmtCount(totalGroups);
  }
  const trashBadge = $('#nav-trash-badge');
  if (trashBadge) {
    trashBadge.hidden = !state.trashSummary.files;
    trashBadge.textContent = fmtCount(state.trashSummary.files);
  }
  updateStatusPill();
  updateTopbarActions();
  const version = $('#version-pill');
  if (version && state.boot?.version) version.textContent = `v${state.boot.version}`;
}

function updateStatusPill() {
  const pill = $('#scan-status-pill');
  if (!pill) return;
  const status = state.status;
  if (status?.cleaning) {
    pill.hidden = false;
    pill.style.cursor = 'default';
    pill.innerHTML = `<span class="status-dot"></span><span>正在清理文件…</span>`;
    return;
  }
  if (status?.scanning) {
    const p = status.progress || {};
    pill.hidden = false;
    pill.style.cursor = 'pointer';
    pill.dataset.action = 'show-scan';
    pill.innerHTML = `<span class="status-dot"></span><span>${Math.round(p.percent || 0)}% · ${esc(PHASE_LABELS[p.phase] || '扫描中')}</span>`;
    return;
  }
  pill.hidden = true;
  delete pill.dataset.action;
}

function renderSidebar() {
  updateChromeBadges();
}

function updateChromeBadges() {
  const resultsBadge = $('#nav-results-badge');
  if (resultsBadge) {
    const totalGroups = (state.counts.exactGroups || 0) + (state.counts.similarGroups || 0);
    resultsBadge.hidden = totalGroups === 0;
    resultsBadge.textContent = fmtCount(totalGroups);
  }
  const trashBadge = $('#nav-trash-badge');
  if (trashBadge) {
    trashBadge.hidden = !state.trashSummary.files;
    trashBadge.textContent = fmtCount(state.trashSummary.files);
  }
  updateStatusPill();
  updateTopbarActions();
  const sub = $('#topbar-sub');
  if (sub) sub.textContent = viewMeta(state.view).sub;
}

function updateRangeVisual(range, value) {
  const min = Number(range.min || 2);
  const max = Number(range.max || 16);
  const pct = max > min ? ((Number(value) - min) / (max - min)) * 100 : 0;
  range.style.setProperty('--range-progress', `${Math.max(0, Math.min(100, pct))}%`);
}

/* ---------------- roots / folder picker ---------------- */

function addRoot(path) {
  if (!path) return false;
  const cleaned = String(path).trim().replace(/^"|"$/g, '');
  if (!cleaned) return false;
  const key = cleaned.toLowerCase().replace(/[\/]+$/, '');
  const current = currentRoot();
  if (current && current.toLowerCase().replace(/[\/]+$/, '') === key) return false;
  state.roots = [cleaned];
  state.homePhase = 'idle';
  saveRoots();
  if (state.view === 'home') renderView();
  updateChromeBadges();
  return true;
}

async function addFolderFlow() {
  if (state.status?.scanning || state.status?.cleaning) {
    toast('当前正在处理文件', { type: 'info', msg: '请等待当前任务完成后再更换文件夹。' });
    return;
  }
  if (!state.boot?.picker) {
    toast('当前模式无法打开文件夹选择器', { type: 'warn', msg: '请在 PicDup 桌面窗口中使用。' });
    return;
  }
  try {
    const res = await request('/api/folders/pick', { method: 'POST', body: {}, timeout: 0 });
    if (res?.path) {
      if (addRoot(res.path)) {
        if (state.view !== 'home') setView('home');
        toast('已选择文件夹', { type: 'success', msg: basename(res.path) });
      }
    }
  } catch (err) {
    toast('无法打开文件夹选择器', { type: 'error', msg: err.message });
  }
}

/* ---------------- scanning ---------------- */

function scanOptions() {
  const s = state.settings || {};
  return {
    includeSubfolders: s.includeSubfolders !== false,
    findSimilar: s.findSimilar !== false,
    similarity: Number(s.similarity ?? 8),
    includeHidden: Boolean(s.includeHidden),
    minFileSizeKB: Math.max(0, Number(s.minFileSizeKB) || 0),
    keeperMode: s.keeperMode || 'resolution',
    excludeDirs: Array.isArray(s.excludeDirs) ? s.excludeDirs : [],
  };
}

async function startScan() {
  if (state.status?.scanning) return;
  if (!state.roots.length) {
    toast('请先选择一个文件夹', { type: 'warn' });
    setView('home');
    return;
  }
  const prev = { phase: state.homePhase, status: state.status };
  state.selection.clear();
  state.selectionBytes = 0;
  state.scanHandled = false;
  state.homePhase = 'scanning';
  state.status = {
    scanning: true,
    cleaning: false,
    progress: { phase: 'discovering', percent: 0, filesFound: 0, filesProcessed: 0, filesFailed: 0, elapsedMs: 0, current: '' },
    hasResult: Boolean(state.result),
  };
  state.boot.status = state.status;
  if (state.view === 'home') {
    updateChrome();
    updateScanBack();
    syncHomeFrontButton();
    setScanFlipped(true);
  } else {
    setView('home');
  }
  try {
    const res = await request('/api/scan', { method: 'POST', body: { roots: state.roots, options: scanOptions() } });
    state.status = { scanning: true, cleaning: false, progress: res.progress, hasResult: Boolean(state.result) };
    state.boot.status = state.status;
    updateChrome();
    syncHomeFrontButton();
    if (state.view === 'home') {
      updateScanBack();
      updateScanning(res.progress);
    }
    startPolling();
    renderSidebar();
  } catch (err) {
    state.homePhase = prev.phase || 'idle';
    state.status = prev.status;
    state.boot.status = prev.status;
    if (state.view === 'home') {
      updateChrome();
      updateScanBack();
      syncHomeFrontButton();
      setScanFlipped(state.homePhase !== 'idle');
    }
    toast('无法开始扫描', { type: 'error', msg: err.message });
  }
}

async function cancelScan() {
  try {
    const st = await request('/api/scan/cancel', { method: 'POST', body: {} });
    state.status = st;
    updateStatusPill();
    updateScanning(st.progress);
    if (!st.scanning) {
      state.homePhase = 'idle';
      if (state.view === 'home') {
        updateChrome();
        syncHomeFrontButton();
        setScanFlipped(false);
      }
    }
    toast('正在取消扫描…', { type: 'info' });
  } catch (err) {
    toast('取消失败', { type: 'error', msg: err.message });
  }
}

function startPolling() {
  if (state.pollTimer) return;
  const tick = async () => {
    state.pollTimer = null;
    try {
      const st = await request('/api/scan', { timeout: 10000 });
      const wasScanning = Boolean(state.status?.scanning);
      state.status = st;
      state.boot = { ...(state.boot || {}), status: st };
      updateStatusPill();
      if (st.cleaning) return;
      if (st.scanning) {
        if (state.view === 'home' && state.homePhase === 'scanning') updateScanning(st.progress);
        state.pollTimer = setTimeout(tick, 550);
        return;
      }
      if (wasScanning) {
        await handleScanFinished(st);
      } else if (state.homePhase === 'scanning') {
        // A scan was running but finished before this poll; reload state.
        await handleScanFinished(st);
      }
    } catch (err) {
      state.pollTimer = setTimeout(tick, 1600);
    }
  };
  tick();
}

async function handleScanFinished(st) {
  if (state.scanHandled) return;
  state.scanHandled = true;
  const phase = st?.progress?.phase;
  if (phase === 'cancelled') {
    state.homePhase = 'idle';
    renderSidebar();
    if (state.view === 'home') {
      updateChrome();
      syncHomeFrontButton();
      setScanFlipped(false);
    }
    toast('扫描已取消', { type: 'info', msg: '已保留上一次的扫描结果。' });
    return;
  }
  if (phase === 'error') {
    state.homePhase = 'idle';
    renderSidebar();
    if (state.view === 'home') {
      updateChrome();
      syncHomeFrontButton();
      setScanFlipped(false);
    }
    toast('扫描失败', { type: 'error', msg: st.progress.message || '发生未知错误' });
    return;
  }
  try {
    await refreshBootstrap();
  } catch (err) {
    toast('刷新结果失败', { type: 'error', msg: err.message });
  }
  state.selection.clear();
  state.selectionBytes = 0;
  const c = state.counts || {};
  if (state.kind === 'exact' && (c.exactGroups || 0) === 0 && (c.similarGroups || 0) > 0) state.kind = 'similar';
  if (state.kind === 'similar' && (c.similarGroups || 0) === 0 && (c.exactGroups || 0) > 0) state.kind = 'exact';
  renderSidebar();
  if (state.result) {
    if (st) state.status = st;
    state.homePhase = 'complete';
    await loadResults(true);
    const groups = (c.exactGroups || 0) + (c.similarGroups || 0);
    if (state.settings?.autoSelect && groups > 0) {
      await smartSelect({ silent: true });
    }
    const onHome = state.view === 'home';
    if (onHome) {
      updateChrome();
      updateScanBack();
      refreshHomeRecentModule();
      syncHomeFrontButton();
      setScanFlipped(true);
    }
    if (!onHome) {
      if (groups === 0) {
        toast('没有发现重复或相似图片', { type: 'success', msg: '这次扫描很干净。', timeout: 5000 });
      } else {
        toast('扫描完成', {
          type: 'success',
          msg: `发现 ${fmtCount(c.exactGroups || 0)} 组重复、${fmtCount(c.similarGroups || 0)} 组相似图片，结果已保存在本机`,
          timeout: 6000,
        });
      }
    }
  } else {
    state.homePhase = 'idle';
    if (state.view === 'home') {
      updateChrome();
      syncHomeFrontButton();
      setScanFlipped(false);
    }
  }
}

async function refreshBootstrap() {
  const boot = await request('/api/bootstrap');
  state.boot = boot;
  state.desktop = Boolean(boot.desktop);
  state.settings = boot.settings;
  state.status = boot.status;
  state.trashSummary = boot.trash || state.trashSummary;
  state.cache = boot.cache || state.cache;
  state.counts = boot.counts || state.counts;
  state.result = boot.result || null;
  state.stats = boot.result ? boot.result.stats : null;
  applyTheme(state.settings.theme, false);
}

/* ---------------- views ---------------- */

function setView(view) {
  state.view = view;
  updateChrome();
  renderSidebar();
  renderView();
  const wrap = $('#view-wrap');
  if (wrap) wrap.scrollTop = 0;
  if (view === 'trash') loadTrash();
  if (view === 'results') updateSelectionUI();
}

function renderView() {
  const wrap = $('#view-wrap');
  if (!wrap) return;
  switch (state.view) {
    case 'results':
      renderResults();
      updateSelectionUI();
      break;
    case 'trash':
      wrap.innerHTML = renderTrash();
      break;
    case 'settings':
      wrap.innerHTML = renderSettings();
      break;
    default:
      wrap.innerHTML = renderHome();
      hydrateImages(wrap);
  }
  $$('.range', wrap).forEach((range) => updateRangeVisual(range, range.value));
}

/* ---------------- home ---------------- */

function renderHome() {
  const phase = state.homePhase || 'idle';
  const flipped = phase === 'scanning' || phase === 'complete';
  const root = currentRoot();
  const recentClass = state.result ? 'has-recent' : 'has-tips';
  return `
    <section class="view home-view">
      <div class="home-grid">
        <div class="scan-flip ${flipped ? 'is-flipped' : ''}" id="scan-flip">
          <div class="scan-flip-inner">
            <div class="scan-face scan-face-front scan-panel ${root ? 'has-folder' : ''}" id="dropzone" data-file-drop-target="true" ${flipped ? 'inert' : ''}>
              ${renderHomeFront()}
            </div>
            <div class="scan-face scan-face-back scan-panel ${phase === 'complete' ? 'is-complete' : 'is-scanning'}" id="scan-back" ${flipped ? '' : 'inert'}>
              ${phase === 'complete' ? renderHomeBackComplete() : renderHomeBackProgress()}
            </div>
          </div>
        </div>
        <aside class="home-side">
          ${renderHomeSettingsModule()}
          <div class="card home-extra ${recentClass}" id="home-recent-box">
            ${renderHomeRecentContent()}
          </div>
        </aside>
      </div>
    </section>
  `;
}

function renderHomeFront() {
  const root = currentRoot();
  const scanning = Boolean(state.status?.scanning);
  const cleaning = Boolean(state.status?.cleaning);
  const disabled = !root || scanning || cleaning;
  const folderName = root ? basename(root) : '';
  return `
    <div class="scan-panel-inner">
      <div class="scan-panel-icon">
        ${icon(root ? 'folder' : 'folder-plus')}
        ${root ? `<span class="scan-panel-check">${icon('check')}</span>` : ''}
      </div>
      <div class="scan-panel-eyebrow">${root ? '已选择文件夹' : '本地图片去重'}</div>
      <h2>${root ? '准备扫描这个文件夹' : '把文件夹拖到这里'}</h2>
      <p class="scan-panel-sub">${root ? '确认后开始扫描，也可以随时更换文件夹。' : '或点击下方按钮选择文件夹；一次只扫描一个文件夹。'}</p>
      ${root ? `
        <div class="selected-folder" title="${esc(root)}">
          <div class="selected-folder-icon">${icon('folder')}</div>
          <div class="selected-folder-body">
            <strong>${esc(folderName)}</strong>
            <span>${esc(root)}</span>
          </div>
          <span class="chip chip-violet">已选择</span>
        </div>
      ` : ''}
      <div class="scan-panel-actions">
        <button class="btn btn-lg ${root ? '' : 'btn-primary'}" type="button" data-action="add-folder">
          ${icon('folder-open')} ${root ? '更换文件夹' : '选择文件夹'}
        </button>
        <button class="btn btn-lg ${root ? 'btn-primary' : ''}" type="button" data-action="start-scan" ${disabled ? 'disabled' : ''}>
          ${icon('scan')} 开始扫描
        </button>
      </div>
      <div class="scan-panel-hint">支持 JPG · PNG · WebP · GIF · BMP · TIFF · 全程本地处理</div>
    </div>
  `;
}

function renderHomeSettingsModule() {
  const settings = state.settings || {};
  const similarity = Number(settings.similarity ?? 8);
  return `
    <section class="card home-settings-module">
      <div class="home-module-head">
        <h2>${icon('sliders')} 扫描设置</h2>
        <span class="home-module-meta">自动保存</span>
      </div>
      <div class="home-sim-head"><span>相似度阈值</span><strong id="home-sim-value">${similarity}</strong></div>
      <input class="range" type="range" min="2" max="16" step="1" value="${similarity}" data-home-sim aria-label="相似度阈值" />
      <div class="home-sim-desc" id="home-sim-desc">${similarityPreset(similarity)} · 约 ${similarityPercentOf(similarity)}% 视觉相似才归组</div>
      <label class="switch-row home-setting-row">
        <span class="home-setting-copy"><strong>包含子文件夹</strong><small>扫描所选文件夹下的所有层级</small></span>
        <input type="checkbox" data-setting="includeSubfolders" ${settings.includeSubfolders !== false ? 'checked' : ''} />
        <span class="switch" aria-hidden="true"></span>
      </label>
      <label class="switch-row home-setting-row">
        <span class="home-setting-copy"><strong>查找相似图片</strong><small>除逐字节查重外，再执行感知相似检测</small></span>
        <input type="checkbox" data-setting="findSimilar" ${settings.findSimilar !== false ? 'checked' : ''} />
        <span class="switch" aria-hidden="true"></span>
      </label>
      <div class="home-settings-note">修改会立即保存，并用于下一次扫描。</div>
    </section>
  `;
}

function renderHomeRecentContent() {
  const result = state.result;
  const groups = (state.counts.exactGroups || 0) + (state.counts.similarGroups || 0);
  const reclaim = (state.counts.reclaimExact || 0) + (state.counts.reclaimSimilar || 0);
  if (!result) {
    return `
      <div class="home-side-head"><h3>${icon('zap')} 开始之前</h3></div>
      <ol class="home-tips">
        <li><span>1</span><div><strong>选择文件夹</strong><small>拖入窗口或点击选择，一次扫描一个文件夹。</small></div></li>
        <li><span>2</span><div><strong>调整相似度</strong><small>默认 8，适合大多数照片；数值越大越宽松。</small></div></li>
        <li><span>3</span><div><strong>确认后清理</strong><small>文件先进入隔离区，可恢复或永久删除。</small></div></li>
      </ol>
      <div class="home-recent-placeholder">${icon('clock')} 扫描完成后，这里会保留最近一次结果</div>
      <div class="home-side-note">${icon('shield')} 图片只在本机读取和比对</div>
    `;
  }
  const exact = state.counts.exactGroups || 0;
  const similar = state.counts.similarGroups || 0;
  const exactPct = groups ? Math.round((exact / groups) * 100) : 0;
  const root = result.roots?.[0] || rootLabel();
  return `
    <div class="home-side-head">
      <h3>${icon('clock')} 上次扫描</h3>
      <span class="home-side-meta">${esc(fmtTime(result.finishedAt))}</span>
    </div>
    <div class="home-recent-stats">
      <div><strong>${fmtCount(groups)}</strong><span>组结果</span></div>
      <div><strong>${fmtBytes(reclaim)}</strong><span>可释放</span></div>
    </div>
    ${groups ? `
      <div class="home-recent-bar" title="精确重复 ${fmtCount(exact)} 组，相似 ${fmtCount(similar)} 组">
        <span class="exact" style="width:${exactPct}%"></span>
        <span class="similar" style="width:${100 - exactPct}%"></span>
      </div>
      <div class="home-recent-legend">
        <span><i class="exact"></i>精确重复 ${fmtCount(exact)} 组</span>
        <span><i class="similar"></i>相似 ${fmtCount(similar)} 组</span>
      </div>
    ` : ''}
    <div class="home-recent-detail">
      <span>${fmtCount(state.stats?.filesScanned || 0)} 张图片</span>
      <span>${fmtDuration(result.durationMs || 0)}</span>
      <span>无法读取 ${fmtCount(result.stats?.filesFailed || 0)}</span>
    </div>
    <div class="home-recent-path" title="${esc(root)}">${icon('folder')}<span>${esc(root)}</span></div>
    <div class="home-recent-hint">${icon('info')} 相似图片按视觉相似度归组，每组至少保留一张。</div>
    <button class="btn btn-sm home-extra-action" type="button" data-action="open-results">
      查看上次结果 ${icon('arrow-right')}
    </button>
    <div class="home-side-note">${icon('shield')} 结果已保存在本机</div>
  `;
}

function renderHomeBackProgress() {
  const p = state.status?.progress || {};
  const percent = Math.round(p.percent || 0);
  return `
    <div class="scan-back-inner">
      <div class="scan-back-head">
        <div class="scan-back-title">
          <span class="chip chip-violet" id="scan-phase-chip">${esc(PHASE_LABELS[p.phase] || '准备中')}</span>
          <span class="scan-back-title-text">正在扫描</span>
        </div>
        <span class="scan-back-count" id="scan-found-top">${fmtCount(p.filesFound || 0)} 张</span>
      </div>
      <div class="progress-track scan-back-track"><div class="progress-fill" id="scan-fill" style="width:${percent}%"></div></div>
      <div class="scan-back-main">
        <div class="radar radar-sm">
          <div class="radar-ring r1"></div><div class="radar-ring r2"></div><div class="radar-ring r3"></div>
          <div class="radar-sweep"></div>
          <div class="radar-core">${icon('scan')}</div>
        </div>
        <div class="scan-percent"><span id="scan-percent">${percent}</span><span>%</span></div>
        <div class="scan-message" id="scan-msg">${esc(PHASE_LABELS[p.phase] || '准备扫描')}</div>
        <div class="scan-current" id="scan-current" title="${esc(p.current || '')}">${esc(p.current || '正在准备文件列表…')}</div>
      </div>
      <div class="scan-back-stats">
        <div class="scan-back-stat"><strong id="scan-found">${fmtCount(p.filesFound || 0)}</strong><span>发现图片</span></div>
        <div class="scan-back-stat"><strong id="scan-processed">${fmtCount(p.filesProcessed || 0)}</strong><span>已分析</span></div>
        <div class="scan-back-stat"><strong id="scan-failed">${fmtCount(p.filesFailed || 0)}</strong><span>无法读取</span></div>
        <div class="scan-back-stat"><strong id="scan-elapsed">${fmtDuration(p.elapsedMs || 0)}</strong><span>已用时</span></div>
      </div>
      <div class="stepper stepper-horizontal" id="scan-steps">
        ${['发现图片', '分析视觉指纹', '比对与分组', '完成'].map((label, i) => {
          const st = phaseStepState(p.phase, i);
          return `<div class="step ${st}"><span class="step-dot">${st === 'done' ? icon('check') : st === 'active' ? icon('dot') : ''}</span><span>${label}</span></div>`;
        }).join('')}
      </div>
      <div class="scan-back-foot">
        <div class="scan-tip" id="scan-tip">${scanTip(p.phase)}</div>
        <button class="btn btn-danger" type="button" data-action="cancel-scan">${icon('x')} 取消扫描</button>
      </div>
    </div>
  `;
}

function renderHomeBackComplete() {
  const counts = state.counts || {};
  const groups = (counts.exactGroups || 0) + (counts.similarGroups || 0);
  const reclaim = (counts.reclaimExact || 0) + (counts.reclaimSimilar || 0);
  const p = state.status?.progress || {};
  return `
    <div class="scan-back-inner scan-back-complete">
      <div class="scan-back-head">
        <div class="scan-back-title">
          <span class="chip chip-green">扫描完成</span>
          <span class="scan-back-title-text">结果已保存在本机</span>
        </div>
        <span class="scan-back-count">${fmtCount(p.filesProcessed || 0)} 张已分析</span>
      </div>
      <div class="scan-back-main complete">
        <div class="scan-complete-icon">${icon('check')}</div>
        <div class="scan-panel-eyebrow">扫描完成</div>
        <h2>${groups ? '结果已准备好' : '这次扫描很干净'}</h2>
        <p class="scan-panel-sub">${groups ? `找到 ${fmtCount(groups)} 组重复或相似图片，可释放约 ${fmtBytes(reclaim)}。` : '没有发现重复或相似图片，不需要清理。'}</p>
      </div>
      <div class="scan-back-stats">
        <div class="scan-back-stat"><strong>${fmtCount(p.filesFound || 0)}</strong><span>发现图片</span></div>
        <div class="scan-back-stat"><strong>${fmtCount(p.filesProcessed || 0)}</strong><span>已分析</span></div>
        <div class="scan-back-stat"><strong>${fmtCount(p.filesFailed || 0)}</strong><span>无法读取</span></div>
        <div class="scan-back-stat"><strong>${fmtDuration(p.elapsedMs || 0)}</strong><span>用时</span></div>
      </div>
      <div class="scan-back-actions">
        <button class="btn btn-primary btn-lg" type="button" data-action="view-results">${icon('eye')} 查看结果</button>
        <button class="btn btn-lg" type="button" data-action="rescan">${icon('refresh')} 重新扫描</button>
        <button class="btn btn-lg" type="button" data-action="flip-home">${icon('folder-open')} 更换文件夹</button>
      </div>
    </div>
  `;
}

function setScanFlipped(flipped) {
  const flip = $('#scan-flip');
  if (!flip) return;
  flip.classList.toggle('is-flipped', Boolean(flipped));
  const front = $('.scan-face-front', flip);
  const back = $('.scan-face-back', flip);
  if (front) front.toggleAttribute('inert', Boolean(flipped));
  if (back) back.toggleAttribute('inert', !flipped);
}

function syncHomeFrontButton() {
  const btn = $('.scan-face-front [data-action="start-scan"]');
  if (!btn) return;
  btn.disabled = !currentRoot() || Boolean(state.status?.scanning) || Boolean(state.status?.cleaning);
}

function updateScanBack() {
  const back = $('#scan-back');
  if (!back) return;
  const phase = state.homePhase || 'idle';
  back.classList.toggle('is-scanning', phase === 'scanning');
  back.classList.toggle('is-complete', phase === 'complete');
  back.innerHTML = phase === 'complete' ? renderHomeBackComplete() : renderHomeBackProgress();
}

function refreshHomeRecentModule() {
  const box = $('#home-recent-box');
  if (!box) return;
  box.classList.toggle('has-recent', Boolean(state.result));
  box.classList.toggle('has-tips', !state.result);
  box.innerHTML = renderHomeRecentContent();
}
/* ---------------- scanning view ---------------- */

function phaseStepState(phase, index) {
  const order = { discovering: 0, analyzing: 1, comparing: 2, finalizing: 3, done: 3 };
  const current = order[phase] ?? 0;
  if (phase === 'done') return 'done';
  if (index < current) return 'done';
  if (index === current) return 'active';
  return '';
}

function scanTip(phase) {
  if (phase === 'discovering') return `<strong>正在遍历文件夹。</strong><br/>隐藏目录、系统目录和符号链接会被自动跳过。`;
  if (phase === 'analyzing') return `<strong>正在为每张图片计算指纹。</strong><br/>图片只在本机读取，处理速度取决于硬盘和图片数量。`;
  if (phase === 'comparing') return `<strong>正在把视觉上接近的图片聚在一起。</strong><br/>相似度越高，误报越少；裁剪较多的照片可能需要更宽松的阈值。`;
  if (phase === 'cancelled') return `扫描已取消。<br/>上一次的扫描结果仍然保留。`;
  return `<strong>扫描完成后可以逐组检查。</strong><br/>PicDup 会默认推荐每组保留画质最好的一张。`;
}

function updateScanning(p) {
  if (!p) return;
  const percent = Math.round(p.percent || 0);
  const el = (id) => document.getElementById(id);
  if (el('scan-percent')) el('scan-percent').textContent = String(percent);
  if (el('scan-fill')) el('scan-fill').style.width = `${percent}%`;
  if (el('scan-msg')) el('scan-msg').textContent = PHASE_LABELS[p.phase] || '准备扫描';
  if (el('scan-phase-chip')) el('scan-phase-chip').textContent = PHASE_LABELS[p.phase] || '准备中';
  if (el('scan-current')) {
    const done = p.phase === 'done';
    el('scan-current').textContent = done
      ? '结果已保存在本机，可以查看结果或返回首页'
      : (p.current || (p.phase === 'comparing' ? '正在比对图片指纹…' : '正在准备文件列表…'));
    el('scan-current').title = done ? '' : (p.current || '');
  }
  if (el('scan-found')) el('scan-found').textContent = fmtCount(p.filesFound || 0);
  if (el('scan-found-top')) el('scan-found-top').textContent = `${fmtCount(p.filesFound || 0)} 张`;
  if (el('scan-processed')) el('scan-processed').textContent = fmtCount(p.filesProcessed || 0);
  if (el('scan-failed')) el('scan-failed').textContent = fmtCount(p.filesFailed || 0);
  if (el('scan-elapsed')) el('scan-elapsed').textContent = fmtDuration(p.elapsedMs || 0);
  if (el('scan-tip')) el('scan-tip').innerHTML = scanTip(p.phase);
  const steps = el('scan-steps');
  if (steps) {
    steps.innerHTML = ['发现图片', '分析视觉指纹', '比对与分组', '完成'].map((label, i) => {
      const st = phaseStepState(p.phase, i);
      return `<div class="step ${st}"><span class="step-dot">${st === 'done' ? icon('check') : st === 'active' ? icon('dot') : ''}</span><span>${label}</span></div>`;
    }).join('');
  }
}

/* ---------------- results ---------------- */

async function loadResults(reset, { soft = false } = {}) {
  if (reset) {
    state.offset = 0;
    state.carouselIndex = 0;
    state.carouselWheelAcc = 0;
    if (!soft) {
      state.groups = [];
      state.groupById = new Map();
      state.loadingResults = true;
      if (state.view === 'results') renderResults();
    }
  }
  state.loadingResults = true;
  const seq = ++state.resultsSeq;
  const params = new URLSearchParams({
    kind: state.kind,
    sort: state.sort,
    q: state.query,
    offset: String(state.offset),
    limit: String(state.limit),
  });
  try {
    const data = await request(`/api/results?${params.toString()}`);
    if (seq !== state.resultsSeq) return;
    const groups = Array.isArray(data.groups) ? data.groups : [];
    state.groups = reset ? groups : state.groups.concat(groups);
    state.total = data.total || 0;
    state.counts = data.counts || state.counts;
    state.stats = data.stats || state.stats;
    if (state.result && data.stats) state.result.stats = data.stats;
    state.offset = (reset ? 0 : state.offset) + groups.length;
    state.hasResults = Boolean(data.hasResult);
    state.groupById = new Map(state.groups.map((g) => [g.id, g]));
    for (const g of groups) {
      for (const c of (g.candidates || [])) {
        state.candidateSizes.set(c.path, c.size);
        state.candidateNames.set(c.path, basename(c.path));
      }
    }
    state.loadingResults = false;
    if (state.view === 'results') {
      renderResults();
      updateSelectionUI();
    }
    updateChromeBadges();
  } catch (err) {
    if (seq !== state.resultsSeq) return;
    state.loadingResults = false;
    if (state.view === 'results') renderResults();
    toast('加载结果失败', { type: 'error', msg: err.message });
  }
}

async function loadMore() {
  if (state.loadingResults || state.groups.length >= state.total) return;
  await loadResults(false);
}

function switchKind(kind) {
  if (kind === state.kind) return;
  state.kind = kind;
  state.selection.clear();
  state.selectionBytes = 0;
  state.query = '';
  const wrap = $('#view-wrap');
  if (wrap) wrap.scrollTop = 0;
  loadResults(true);
}

function tabCounts() {
  const c = state.counts || {};
  return { exact: c.exactGroups || 0, similar: c.similarGroups || 0 };
}

function activeReclaim() {
  const s = state.stats || {};
  return state.kind === 'exact' ? (s.reclaimableExact || 0) : (s.reclaimableSimilar || 0);
}

function cardMembers(g) {
  const members = Array.isArray(g.members) ? g.members.slice(0, 4) : [];
  return members;
}

function groupSelectedCount(g) {
  return (g.candidates || []).filter((c) => state.selection.has(c.path)).length;
}

function groupTargetCount(g) {
  return (g.candidates || []).filter((c) => !c.keeper).length;
}

function groupAllTargetsSelected(g) {
  const targets = (g.candidates || []).filter((c) => !c.keeper);
  return targets.length > 0 && targets.every((c) => state.selection.has(c.path));
}

function renderGroupCard(g, index, opts = {}) {
  const members = cardMembers(g);
  const n = Math.min(4, members.length);
  const more = g.count - n;
  const selected = groupSelectedCount(g);
  const allTargets = groupAllTargetsSelected(g);
  const partial = selected > 0 && !allTargets;
  const tiles = members.map((m) => `
    <div class="tile">
      ${m.thumb ? `<img data-thumb="${esc(m.thumb)}" alt="${esc(m.name)}" loading="lazy" decoding="async">` : `<div class="mosaic-empty">${icon('image')}</div>`}
    </div>
  `).join('');
  const kindChip = g.kind === 'exact'
    ? `<span class="badge badge-exact">完全重复</span>`
    : `<span class="badge badge-similar">相似图片</span>`;
  const simChip = g.kind === 'similar' && g.similarity
    ? `<span class="chip chip-cyan" style="padding:1px 5px;font-size:9px">≥${g.similarity.toFixed(1)}%</span>`
    : '';
  const dimensions = g.keeper?.width ? `${g.keeper.width}×${g.keeper.height}` : '';
  const carousel = Boolean(opts.carousel);
  const pos = opts.pos || 'active';
  const classes = `group-card ${carousel ? 'carousel-card' : ''} ${carousel ? `pos-${pos}` : ''} ${opts.active ? 'is-active' : ''} ${carousel && opts.slot !== undefined ? `duo-slot-${opts.slot}` : ''} ${selected ? 'has-selection' : ''}`;
  const action = carousel ? 'carousel-card' : 'open-group';
  const attrs = carousel
    ? `data-index="${index}" tabindex="${pos === 'hidden' ? '-1' : '0'}" aria-hidden="${pos === 'hidden' ? 'true' : 'false'}"`
    : `tabindex="0" role="button" style="--i:${index}"`;
  return `
    <article class="${classes}" data-group-id="${esc(g.id)}" data-action="${action}" ${attrs} ${carousel ? 'role="button"' : ''}>
      <div class="mosaic n-${n || 1}">
        ${tiles || `<div class="mosaic-empty">${icon('image')}</div>`}
        ${more > 0 ? `<span class="mosaic-more">+${more}</span>` : ''}
        <div class="group-badges">${kindChip}</div>
        <button class="card-select ${allTargets ? 'on' : ''} ${partial ? 'partial' : ''}" type="button"
          data-action="toggle-group" data-id="${esc(g.id)}" title="${allTargets ? '取消选择' : '选择其余图片'}" aria-label="选择分组">
          ${allTargets ? icon('check') : partial ? icon('minus') : ''}
        </button>
      </div>
      <div class="group-body">
        <div class="group-title">${icon('star', 'keeper-star')}<span class="group-title-text">${esc(g.keeper?.name || '未命名图片')}</span></div>
        <div class="group-meta">
          <span>${fmtCount(g.count)} 个文件</span><span class="dot"></span><span>${esc(KIND_LABELS[g.kind] || '')}</span>
          ${dimensions ? `<span class="dot"></span><span>${dimensions}</span>` : ''}
          ${simChip}
        </div>
        <div class="group-paths" title="${esc(g.keeper?.dir || '')}">${esc(g.keeper?.dir || '')}</div>
        ${selected ? `<div class="group-selected-label" data-selected-label>已选 ${selected} 张待清理</div>` : `<div class="group-selected-label" data-selected-label hidden></div>`}
        <div class="group-foot">
          <div class="waste"><strong>${fmtBytes(g.wasted)}</strong><span>可释放</span></div>
          <span class="group-open">${icon('eye')} 查看 ${fmtCount(g.count)} 张</span>
        </div>
      </div>
    </article>
  `;
}

function renderResults() {
  const wrap = $('#view-wrap');
  if (!wrap) return;
  const scrollTop = wrap.scrollTop;
  const activeId = document.activeElement?.id;
  const searchValue = state.query;
  const counts = tabCounts();
  const total = counts.exact + counts.similar;
  const selectedCount = state.selection.size;
  const selectedBytes = state.selectionBytes;
  const stats = state.stats || {};
  const reclaim = activeReclaim();
  const activeGroups = state.kind === 'exact' ? counts.exact : counts.similar;
  const note = state.result?.note ? `<div class="note-bar">${icon('alert')}<span>${esc(state.result.note)}</span></div>` : '';
  const issues = state.result?.issueCount
    ? `<div class="issues-bar"><details><summary>${state.result.issueCount} 个文件无法读取，点击查看详情</summary><ul>${(state.result.issues || []).map((it) => `<li>${esc(it.path)} — ${esc(it.message)}</li>`).join('')}</ul></details></div>`
    : '';

  let grid;
  if (state.loadingResults && !state.groups.length) {
    grid = `<div class="result-carousel is-loading"><div class="carousel-stage"><div class="skeleton skeleton-card carousel-skeleton"></div></div></div>`;
  } else if (!state.groups.length) {
    grid = renderResultsEmpty(activeGroups, total);
  } else {
    grid = renderCarousel(state.groups);
  }

  wrap.innerHTML = `
    <section class="view results-view">
      <div class="stat-grid">
        <div class="stat-card">
          <div class="stat-label">${icon('hard-drive')} 可释放空间</div>
          <div class="stat-value accent">${fmtBytes(reclaim)}</div>
          <div class="stat-sub">${esc(KIND_LABELS[state.kind])} · ${fmtCount(activeGroups)} 组</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">${icon('trash')} 待清理</div>
          <div class="stat-value ${selectedCount ? 'danger' : ''}" id="stat-selection-value">${fmtCount(selectedCount)} <span class="unit">张</span></div>
          <div class="stat-sub" id="stat-selection-sub">${selectedBytes ? `约 ${fmtBytes(selectedBytes)}` : '还没有选择文件'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">${icon('layers')} 发现分组</div>
          <div class="stat-value">${fmtCount(total)} <span class="unit">组</span></div>
          <div class="stat-sub">${fmtCount(counts.exact)} 组重复 · ${fmtCount(counts.similar)} 组相似</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">${icon('image')} 已扫描</div>
          <div class="stat-value">${fmtCount(stats.filesScanned || 0)} <span class="unit">张</span></div>
          <div class="stat-sub">${stats.filesFailed ? `${fmtCount(stats.filesFailed)} 张无法读取 · ` : ''}用时 ${fmtDuration(stats.durationMs || 0)}</div>
        </div>
      </div>

      ${note}
      <div class="results-toolbar">
        <div class="tabs" role="tablist">
          <button class="tab ${state.kind === 'exact' ? 'active' : ''}" type="button" role="tab" aria-selected="${state.kind === 'exact'}" data-action="switch-kind" data-kind="exact">
            完全重复 <span class="tab-count">${fmtCount(counts.exact)}</span>
          </button>
          <button class="tab ${state.kind === 'similar' ? 'active' : ''}" type="button" role="tab" aria-selected="${state.kind === 'similar'}" data-action="switch-kind" data-kind="similar">
            相似图片 <span class="tab-count">${fmtCount(counts.similar)}</span>
          </button>
        </div>
        <div class="toolbar-spacer"></div>
        <div class="search-box">
          ${icon('search')}
          <input id="result-search" type="search" placeholder="搜索文件名…" value="${esc(searchValue)}" autocomplete="off" />
        </div>
        <div class="select-wrap">
          <select id="result-sort" aria-label="排序方式">
            ${Object.entries(SORT_LABELS).map(([v, l]) => `<option value="${v}" ${state.sort === v ? 'selected' : ''}>${l}</option>`).join('')}
          </select>
          ${icon('chevron-down')}
        </div>
        <div class="toolbar-actions">
          <button class="btn btn-sm" type="button" data-action="smart-select">${icon('wand')} 智能选择</button>
          <button class="btn btn-sm btn-ghost" type="button" data-action="clear-selection" ${selectedCount ? '' : 'disabled'}>清空</button>
        </div>
      </div>

      <div class="selection-bar" id="selection-bar" ${selectedCount ? '' : 'hidden'}>
        ${icon('alert')}
        <div class="selection-bar-text">已选择 <strong id="sel-count">${fmtCount(selectedCount)}</strong> 张待清理，约 <strong id="sel-bytes">${fmtBytes(selectedBytes)}</strong>。每组至少保留一张。</div>
        <button class="btn btn-sm btn-ghost" type="button" data-action="clear-selection">取消选择</button>
        <button class="btn btn-sm btn-danger" type="button" data-action="cleanup" id="btn-cleanup">${icon('trash')} 清理选中</button>
      </div>

      ${grid}
      ${issues}
    </section>
  `;
  hydrateImages(wrap);
  wrap.scrollTop = scrollTop;
  if (activeId === 'result-search') {
    const input = $('#result-search');
    if (input) {
      input.focus();
      const len = input.value.length;
      input.setSelectionRange(len, len);
    }
  }
}

function renderCarouselCard(g, index, opts = {}) {
  const members = cardMembers(g);
  const n = Math.min(4, members.length);
  const more = g.count - n;
  const selected = groupSelectedCount(g);
  const allTargets = groupAllTargetsSelected(g);
  const partial = selected > 0 && !allTargets;
  const pos = opts.pos || 'active';
  const tiles = members.map((m) => `
    <div class="tile">
      ${m.thumb ? `<img data-thumb="${esc(m.thumb)}" alt="${esc(m.name)}" loading="lazy" decoding="async">` : `<div class="mosaic-empty">${icon('image')}</div>`}
    </div>
  `).join('');
  const kindChip = g.kind === 'exact'
    ? `<span class="badge badge-exact">完全重复</span>`
    : `<span class="badge badge-similar">相似图片</span>`;
  const simChip = g.kind === 'similar' && g.similarity
    ? `<span class="chip chip-cyan carousel-sim">≥${g.similarity.toFixed(1)}%</span>`
    : '';
  const dimensions = g.keeper?.width ? `${g.keeper.width}×${g.keeper.height}` : '';
  const classes = `group-card carousel-card pos-${pos} ${opts.active ? 'is-active' : ''} ${opts.slot !== undefined ? `duo-slot-${opts.slot}` : ''} ${selected ? 'has-selection' : ''}`;
  return `
    <article class="${classes}" data-group-id="${esc(g.id)}" data-index="${index}" data-action="carousel-card" data-side="${opts.side || ''}"
      role="button" tabindex="${pos === 'hidden' ? '-1' : '0'}" aria-hidden="${pos === 'hidden' ? 'true' : 'false'}">
      <div class="carousel-info">
        <div class="carousel-title-row">
          ${kindChip}
          <div class="carousel-title">${icon('star', 'keeper-star')}<span>${esc(g.keeper?.name || '未命名图片')}</span></div>
          <button class="card-select carousel-select ${allTargets ? 'on' : ''} ${partial ? 'partial' : ''}" type="button"
            data-action="toggle-group" data-id="${esc(g.id)}" title="${allTargets ? '取消选择' : '选择其余图片'}" aria-label="选择分组">
            ${allTargets ? icon('check') : partial ? icon('minus') : ''}
          </button>
        </div>
        <div class="carousel-meta">
          <span>${fmtCount(g.count)} 个文件</span><span class="dot"></span><span>${esc(KIND_LABELS[g.kind] || '')}</span>
          ${dimensions ? `<span class="dot"></span><span>${dimensions}</span>` : ''}
          ${simChip}
          <span class="carousel-selected" data-selected-label ${selected ? '' : 'hidden'}>已选 ${selected} 张</span>
        </div>
        <div class="carousel-path" title="${esc(g.keeper?.dir || '')}">${esc(g.keeper?.dir || '')}</div>
      </div>
      <div class="mosaic n-${n || 1}">
        ${tiles || `<div class="mosaic-empty">${icon('image')}</div>`}
        ${more > 0 ? `<span class="mosaic-more">+${more}</span>` : ''}
      </div>
      <div class="carousel-footer">
        <div class="waste"><strong>${fmtBytes(g.wasted)}</strong><span>可释放</span></div>
        <span class="group-open">${icon('eye')} 查看 ${fmtCount(g.count)} 张</span>
      </div>
    </article>
  `;
}

function carouselPositions(count, active) {
  const n = Math.max(1, count);
  const a = Math.max(0, Math.min(active, n - 1));
  const layout = n === 1 ? 'single' : n === 2 ? 'duo' : 'cover';
  const map = new Map();
  for (let i = 0; i < n; i++) {
    let pos = 'hidden';
    if (layout === 'single') pos = 'active';
    else if (layout === 'duo') pos = i === a ? 'active' : 'other';
    else if (i === a) pos = 'active';
    else if (a > 0 && i === a - 1) pos = 'prev';
    else if (a < n - 1 && i === a + 1) pos = 'next';
    map.set(i, pos);
  }
  return { layout, active: a, map };
}

function renderCarousel(groups) {
  const n = groups.length;
  const statePos = carouselPositions(n, state.carouselIndex || 0);
  state.carouselIndex = statePos.active;
  const cards = groups.map((g, i) => {
    const pos = statePos.map.get(i);
    return renderCarouselCard(g, i, {
      pos,
      active: i === statePos.active,
      slot: i,
      side: pos === 'hidden' ? (i < statePos.active ? 'left' : 'right') : '',
    });
  }).join('');
  const dots = groups.map((_, i) => `<span class="carousel-dot ${i === statePos.active ? 'active' : ''}" data-dot="${i}"></span>`).join('');
  const hint = n > 1 ? '滚轮或 ← → 切换 · 点击旁边卡片聚焦' : '仅 1 组结果 · 点击卡片查看';
  return `
    <div class="result-carousel layout-${statePos.layout}" id="result-carousel" data-active="${statePos.active}" tabindex="0" aria-label="扫描结果轮播，使用滚轮或左右方向键切换">
      <div class="carousel-stage">${cards}</div>
      <div class="carousel-foot">
        <span class="carousel-hint">${icon('refresh')} ${hint}</span>
        <div class="carousel-dots" aria-hidden="true">${dots}</div>
        <span class="carousel-counter" id="carousel-counter">${statePos.active + 1} / ${n}</span>
      </div>
    </div>
  `;
}

function applyCarouselPositions() {
  const root = $('#result-carousel');
  if (!root) return;
  const cards = $$('.carousel-card', root);
  if (!cards.length) return;
  const statePos = carouselPositions(cards.length, state.carouselIndex || 0);
  state.carouselIndex = statePos.active;
  cards.forEach((card, i) => {
    const pos = statePos.map.get(i);
    card.classList.remove('pos-active', 'pos-prev', 'pos-next', 'pos-other', 'pos-hidden', 'is-active');
    card.classList.add(`pos-${pos}`);
    card.dataset.side = pos === 'hidden' ? (i < statePos.active ? 'left' : 'right') : '';
    if (i === statePos.active) card.classList.add('is-active');
    card.setAttribute('aria-hidden', pos === 'hidden' ? 'true' : 'false');
    card.tabIndex = pos === 'hidden' ? -1 : 0;
  });
  root.dataset.active = String(statePos.active);
  const counter = $('#carousel-counter');
  if (counter) counter.textContent = `${statePos.active + 1} / ${cards.length}`;
  $$('.carousel-dot', root).forEach((dot) => dot.classList.toggle('active', Number(dot.dataset.dot) === statePos.active));
  hydrateImages(root);
}

function setCarouselIndex(index) {
  const n = state.groups.length;
  if (!n) return;
  const raw = Number(index);
  if (!Number.isFinite(raw)) return;
  const next = Math.max(0, Math.min(n - 1, Math.trunc(raw)));
  if (next === state.carouselIndex) return;
  state.carouselIndex = next;
  applyCarouselPositions();
  if (state.groups.length < state.total && next >= state.groups.length - 3) {
    loadMore();
  }
}

function moveCarousel(delta) {
  const n = state.groups.length;
  if (n < 2) return;
  const current = state.carouselIndex || 0;
  const next = current + delta;
  if (next < 0 || next >= n) return;
  setCarouselIndex(next);
}

function onResultsWheel(event) {
  if (state.view !== 'results' || state.overlayOpen || event.ctrlKey) return;
  const stage = event.target.closest?.('.carousel-stage');
  if (!stage || !state.groups.length) return;
  event.preventDefault();
  const now = performance.now();
  if (now < state.carouselWheelLock) return;
  const delta = Math.abs(event.deltaY) >= Math.abs(event.deltaX) ? event.deltaY : event.deltaX;
  if (!delta) return;
  if (state.carouselWheelAcc && Math.sign(delta) !== Math.sign(state.carouselWheelAcc)) {
    state.carouselWheelAcc = 0;
  }
  state.carouselWheelAcc += delta;
  if (Math.abs(state.carouselWheelAcc) < 36) return;
  const dir = state.carouselWheelAcc > 0 ? 1 : -1;
  state.carouselWheelAcc = 0;
  state.carouselWheelLock = now + 260;
  moveCarousel(dir);
}

function renderResultsSummary() {
  const stats = state.stats || {};
  const counts = tabCounts();
  const total = counts.exact + counts.similar;
  return `
    <div class="group-card group-summary" aria-label="本次扫描摘要">
      <div class="summary-icon">${icon('check')}</div>
      <h4>本次扫描完成</h4>
      <p>${fmtCount(stats.filesScanned || 0)} 张图片 · ${fmtCount(total)} 组结果</p>
      <div class="summary-meta">
        <span class="chip">${fmtCount(counts.exact)} 组重复</span>
        <span class="chip">${fmtCount(counts.similar)} 组相似</span>
        <span class="chip">${fmtDuration(stats.durationMs || 0)}</span>
      </div>
      <div class="summary-note">${icon('shield')} 所有比对均在本机完成</div>
    </div>
  `;
}

function renderResultsEmpty(activeGroups, total) {
  if (!state.result) {
    return `<div class="empty-state"><div class="empty-icon">${icon('image')}</div><h3>还没有扫描结果</h3><p>选择文件夹并开始扫描，结果会出现在这里。</p><button class="btn btn-primary" data-action="adjust-home" type="button">${icon('folder-open')} 选择文件夹</button></div>`;
  }
  if ((state.result.stats?.filesScanned || 0) === 0) {
    return `<div class="empty-state"><div class="empty-icon">${icon('image')}</div><h3>这些文件夹里没有图片</h3><p>没有找到支持格式的图片文件。可以换一个文件夹，或检查是否开启了“包含子文件夹”。</p><button class="btn" data-action="adjust-home" type="button">${icon('folder-open')} 重新选择文件夹</button></div>`;
  }
  if (total === 0) {
    return `<div class="empty-state"><div class="empty-icon" style="color:var(--green);background:var(--green-soft)">${icon('check')}</div><h3>图片很干净，没有发现重复</h3><p>当前文件夹里没有找到完全重复或足够相似的照片。可以调整相似度阈值后重新扫描。</p><button class="btn" data-action="go-settings" type="button">${icon('sliders')} 调整扫描设置</button></div>`;
  }
  if (activeGroups === 0) {
    const other = state.kind === 'exact' ? 'similar' : 'exact';
    return `<div class="empty-state"><div class="empty-icon">${icon('layers')}</div><h3>这个分类下没有结果</h3><p>${state.kind === 'exact' ? '没有找到逐字节完全相同的文件。' : '没有找到符合条件的相似图片。'}可以看看另一个分类。</p><button class="btn" data-action="switch-kind" data-kind="${other}" type="button">${icon('arrow-right')} 查看${KIND_LABELS[other]}</button></div>`;
  }
  return `<div class="empty-state"><div class="empty-icon">${icon('search')}</div><h3>没有匹配的结果</h3><p>试试清空搜索词或换一个排序方式。</p></div>`;
}

function updateSelectionUI() {
  if (!state.selection.size) state.selectionBytes = 0;
  const count = state.selection.size;
  const bytes = state.selectionBytes;
  const statVal = $('#stat-selection-value');
  if (statVal) {
    statVal.innerHTML = `${fmtCount(count)} <span class="unit">张</span>`;
    statVal.classList.toggle('danger', count > 0);
  }
  const statSub = $('#stat-selection-sub');
  if (statSub) statSub.textContent = bytes ? `约 ${fmtBytes(bytes)}` : (count ? '正在估算…' : '还没有选择文件');
  const bar = $('#selection-bar');
  if (bar) bar.hidden = count === 0;
  const selCount = $('#sel-count');
  if (selCount) selCount.textContent = fmtCount(count);
  const selBytes = $('#sel-bytes');
  if (selBytes) selBytes.textContent = fmtBytes(bytes);
  const cleanupBtn = $('#btn-cleanup');
  if (cleanupBtn) cleanupBtn.disabled = count === 0;
  $$('.nav-item [data-action="clear-selection"]').forEach(() => {});
  for (const g of state.groups) {
    const card = document.querySelector(`.group-card[data-group-id="${CSS.escape(g.id)}"]`);
    if (!card) continue;
    const selected = groupSelectedCount(g);
    const allTargets = groupAllTargetsSelected(g);
    const partial = selected > 0 && !allTargets;
    card.classList.toggle('has-selection', selected > 0);
    const btn = card.querySelector('.card-select');
    if (btn) {
      btn.classList.toggle('on', allTargets);
      btn.classList.toggle('partial', partial);
      btn.innerHTML = allTargets ? icon('check') : partial ? icon('minus') : '';
      btn.title = allTargets ? '取消选择' : '选择其余图片';
      btn.setAttribute('aria-label', allTargets ? '取消选择' : '选择其余图片');
    }
    const label = card.querySelector('[data-selected-label]');
    if (label) {
      label.hidden = selected === 0;
      label.textContent = `已选 ${selected} 张待清理`;
    }
  }
  const clearButtons = $$('[data-action="clear-selection"]');
  clearButtons.forEach((b) => { b.disabled = count === 0; });
}

function toggleGroup(groupId) {
  const g = state.groupById.get(groupId);
  if (!g) return;
  const targets = (g.candidates || []).filter((c) => !c.keeper);
  const all = targets.length > 0 && targets.every((c) => state.selection.has(c.path));
  if (all) {
    for (const c of (g.candidates || [])) {
      if (state.selection.has(c.path)) {
        state.selection.delete(c.path);
        state.selectionBytes -= c.size;
      }
    }
  } else {
    for (const c of targets) {
      if (!state.selection.has(c.path)) {
        state.selection.add(c.path);
        state.selectionBytes += c.size;
      }
    }
  }
  if (state.selectionBytes < 0) state.selectionBytes = 0;
  updateSelectionUI();
}

function clearSelection() {
  state.selection.clear();
  state.selectionBytes = 0;
  updateSelectionUI();
}

async function smartSelect({ silent = false } = {}) {
  try {
    const data = await request(`/api/suggest?kind=${encodeURIComponent(state.kind)}`);
    state.selection = new Set(data.paths || []);
    state.selectionBytes = data.bytes || 0;
    updateSelectionUI();
    if (silent) return data;
    if (!data.count) {
      toast('没有可自动选择的图片', { type: 'info', msg: '当前分类下没有可清理的多余文件。' });
    } else {
      toast(`已智能选择 ${fmtCount(data.count)} 张`, {
        type: 'success',
        msg: `约 ${fmtBytes(data.bytes)}，每组会保留推荐的那张。`,
        timeout: 3500,
      });
    }
    return data;
  } catch (err) {
    if (!silent) toast('智能选择失败', { type: 'error', msg: err.message });
  }
}

/* ---------------- cleanup ---------------- */

function requestCleanup() {
  const paths = selectionPaths();
  if (!paths.length) return;
  if (state.settings?.confirmCleanup === false) {
    confirmCleanup();
  } else {
    openCleanupDialog();
  }
}

function openCleanupDialog() {
  const paths = selectionPaths();
  if (!paths.length) return;
  const mode = state.settings?.cleanupMode === 'recycle' && state.boot?.recycle ? 'recycle' : 'trash';
  const names = paths.slice(0, 7).map((p) => lookupName(p));
  const more = paths.length - names.length;
  const modeTitle = mode === 'trash' ? '移入隔离区' : '移入 Windows 回收站';
  const modeDesc = mode === 'trash'
    ? '文件会移入 PicDup 隔离区，之后可以随时恢复或彻底删除。'
    : '文件会进入系统回收站。PicDup 无法撤销这项操作。';
  showOverlay(`
    <div class="modal" role="dialog" aria-modal="true" aria-labelledby="cleanup-title">
      <div class="modal-head">
        <div>
          <h3 id="cleanup-title">清理 ${fmtCount(paths.length)} 张图片？</h3>
          <p>请确认这些文件不再需要。每组至少会保留一张。</p>
        </div>
        <button class="modal-close" data-action="close-modal" aria-label="关闭">${icon('x')}</button>
      </div>
      <div class="modal-body">
        <div class="confirm-summary">
          <div class="confirm-stats">
            <div class="confirm-stat"><strong>${fmtCount(paths.length)}</strong><span>文件数量</span></div>
            <div class="confirm-stat"><strong>${fmtBytes(state.selectionBytes)}</strong><span>预计释放</span></div>
          </div>
          <div class="confirm-note ${mode === 'recycle' ? 'warn' : ''}">
            ${icon(mode === 'trash' ? 'shield' : 'alert')}
            <div><strong style="display:block;font-weight:600">${modeTitle}</strong>${modeDesc}</div>
          </div>
          <ul class="confirm-list">
            ${names.map((n) => `<li><span>${esc(n)}</span><span>待清理</span></li>`).join('')}
            ${more > 0 ? `<li><span>还有 ${more} 个文件…</span><span></span></li>` : ''}
          </ul>
        </div>
      </div>
      <div class="modal-foot">
        <button class="btn" type="button" data-action="close-modal">取消</button>
        <button class="btn btn-danger" type="button" data-action="confirm-cleanup" id="confirm-cleanup-btn">${icon('trash')} 确认清理</button>
      </div>
    </div>
  `);
}

async function confirmCleanup(pathsOverride = null) {
  const paths = Array.isArray(pathsOverride) ? pathsOverride.slice() : selectionPaths();
  if (!paths.length || state.cleanupBusy) return;
  const active = state.activeGroup;
  const cleanedBytes = paths.reduce((sum, p) => {
    const known = state.candidateSizes.get(p);
    if (known != null) return sum + known;
    const member = active?.members?.find((m) => m.path === p);
    return sum + (member?.size || 0);
  }, 0);
  state.cleanupBusy = true;
  const btn = $('#confirm-cleanup-btn');
  const inlineBtn = $('#btn-cleanup');
  const gmBtn = $('#gm-cleanup-btn');
  const gmConfirm = $('#gm-cleanup-confirm');
  const gmCancel = $('#gm-cleanup-cancel');
  const inlineHtml = inlineBtn?.innerHTML;
  const gmHtml = gmBtn?.innerHTML;
  const gmConfirmHtml = gmConfirm?.innerHTML;
  const busyHtml = `<span class="spinner"></span> 正在清理…`;
  if (btn) {
    btn.disabled = true;
    btn.innerHTML = busyHtml;
  }
  if (inlineBtn) {
    inlineBtn.disabled = true;
    inlineBtn.innerHTML = busyHtml;
  }
  if (gmBtn) {
    gmBtn.disabled = true;
    gmBtn.innerHTML = busyHtml;
  }
  if (gmConfirm) {
    gmConfirm.disabled = true;
    gmConfirm.innerHTML = busyHtml;
  }
  if (gmCancel) gmCancel.disabled = true;
  const mode = state.settings?.cleanupMode === 'recycle' && state.boot?.recycle ? 'recycle' : 'trash';
  try {
    const res = await request('/api/cleanup', {
      method: 'POST',
      body: { paths, mode, kind: state.kind },
      timeout: 180000,
    });
    state.cleanupBusy = false;
    closeOverlay();
    const cleanup = res.cleanup || {};
    const failed = cleanup.failed?.length || 0;
    for (const p of paths) state.selection.delete(p);
    state.selectionBytes = Math.max(0, state.selectionBytes - cleanedBytes);
    if (!state.selection.size) state.selectionBytes = 0;
    await refreshBootstrap().catch(() => {});
    renderSidebar();
    if (state.view === 'results') await loadResults(true);
    if (state.view === 'trash') await loadTrash();
    const title = failed ? `已清理 ${fmtCount(cleanup.moved || 0)} 张，${failed} 张失败` : `已清理 ${fmtCount(cleanup.moved || 0)} 张图片`;
    const toastOpts = {
      type: failed ? 'warn' : 'success',
      msg: `释放约 ${fmtBytes(cleanup.bytes || 0)}`,
      timeout: 8000,
    };
    if (mode === 'trash' && cleanup.batchId) {
      toastOpts.action = { label: '撤销', fn: () => undoCleanup(cleanup.batchId) };
    }
    toast(title, toastOpts);
    updateChromeBadges();
    updateSelectionUI();
  } catch (err) {
    state.cleanupBusy = false;
    if (btn) {
      btn.disabled = false;
      btn.innerHTML = `${icon('trash')} 确认清理`;
    }
    if (inlineBtn) {
      inlineBtn.disabled = false;
      inlineBtn.innerHTML = inlineHtml || `${icon('trash')} 清理选中`;
    }
    if (gmBtn) {
      gmBtn.disabled = false;
      gmBtn.innerHTML = gmHtml || `${icon('trash')} 清理选中`;
    }
    if (gmConfirm) {
      gmConfirm.disabled = false;
      gmConfirm.innerHTML = gmConfirmHtml || `${icon('trash')} 确认清理`;
    }
    if (gmCancel) gmCancel.disabled = false;
    if (state.activeGroup) {
      state.groupCleanupPending = false;
      refreshGroupModalSelection(state.activeGroup);
    }
    toast('清理失败', { type: 'error', msg: err.message });
  }
}

async function undoCleanup(batchId) {
  try {
    const res = await request('/api/trash/restore', { method: 'POST', body: { batchId } });
    const restored = res.restore?.restored || res.restored || 0;
    toast('已撤销清理', { type: 'success', msg: `恢复了 ${fmtCount(restored)} 个文件，重新扫描后即可看到它们。` });
    await refreshBootstrap().catch(() => {});
    renderSidebar();
    if (state.view === 'results') await loadResults(true);
    if (state.view === 'trash') await loadTrash();
  } catch (err) {
    toast('撤销失败', { type: 'error', msg: err.message });
  }
}

/* ---------------- group detail ---------------- */

async function openGroup(id) {
  if (!id) return;
  showOverlay(`
    <div class="modal group-modal" role="dialog" aria-modal="true" aria-label="图片分组详情">
      <div class="modal-head">
        <div><h3>正在载入分组…</h3></div>
        <button class="modal-close" data-action="close-modal" aria-label="关闭">${icon('x')}</button>
      </div>
      <div class="modal-body" style="display:flex;align-items:center;justify-content:center;min-height:360px">
        <span class="spinner"></span>
      </div>
    </div>
  `);
  try {
    const g = await request(`/api/groups/${encodeURIComponent(id)}`);
    if (!g || !g.id) throw new Error('分组不存在');
    state.activeGroup = g;
    const members = g.members || [];
    const nonKeeper = members.find((m) => !m.keeper) || members.find((m) => m.path !== g.keeper?.path);
    state.previewPath = g.kind === 'similar' && nonKeeper
      ? nonKeeper.path
      : (g.keeper?.path || members[0]?.path || null);
    state.groupCleanupPending = false;
    renderGroupModal(g);
  } catch (err) {
    closeOverlay();
    toast('无法打开分组', { type: 'error', msg: err.message });
    if (/不存在|更新/.test(err.message)) loadResults(true);
  }
}

function renderGroupModal(g) {
  const members = g.members || [];
  const previewMember = members.find((m) => m.path === state.previewPath) || members[0];
  const simText = g.kind === 'similar' && g.similarity ? ` · 最低相似度 ${g.similarity.toFixed(1)}%` : '';
  const canCompare = g.kind === 'similar' && members.length > 1;
  state.compareMode = canCompare ? 'split' : 'single';
  state.compareZoom = 1;
  state.comparePanX = 0;
  state.comparePanY = 0;
  state.compareDrag = null;
  state.compareBasePath = g.keeper?.path || members[0]?.path || null;
  state.compareTargetPath = null;
  const compareOptions = members.map((m) => `<option value="${esc(m.path)}">${esc(m.name)}${m.keeper ? '（推荐保留）' : ''}</option>`).join('');
  const rows = members.map((m) => {
    const selected = state.selection.has(m.path);
    const sim = g.kind === 'similar' && !m.keeper
      ? `<span class="member-sim">${(m.similarity || 0).toFixed(1)}%</span>`
      : '';
    return `
      <div class="member-row ${m.keeper ? 'is-keeper' : ''} ${selected ? 'is-selected' : ''} ${m.path === previewMember?.path ? 'active' : ''}" data-member-path="${esc(m.path)}" data-action="preview-member" role="button" tabindex="0" title="点击查看，Ctrl+单击标记清理">
        <img class="member-thumb" src="${esc(m.thumb || '')}" alt="${esc(m.name)}" loading="lazy" />
        <div class="member-info">
          <div class="member-name" title="${esc(m.path)}">${esc(m.name)}</div>
          <div class="member-meta">
            <span>${m.width || '?'} × ${m.height || '?'}</span><span>${fmtBytes(m.size)}</span>${sim}
            ${m.keeper ? `<span class="chip chip-green" style="padding:0 4px;font-size:8.5px">推荐保留</span>` : ''}
          </div>
        </div>
        <div class="member-actions">
          <span class="compare-role" hidden></span>
          ${m.keeper ? `<span class="keep-radio" title="推荐保留">${icon('star')}</span>` : ''}
          <button class="delete-toggle ${selected ? 'on' : ''}" type="button" data-action="toggle-member"
            data-path="${esc(m.path)}" data-size="${m.size}" title="${selected ? '取消清理' : '标记清理'}"
            aria-label="${selected ? '取消清理' : '标记清理'}">${icon('trash')}</button>
        </div>
      </div>
    `;
  }).join('');
  showOverlay(`
    <div class="modal group-modal" role="dialog" aria-modal="true" aria-label="图片分组详情">
      <div class="modal-head">
        <div style="min-width:0">
          <div class="badge ${g.kind === 'exact' ? 'badge-exact' : 'badge-similar'}" style="display:inline-flex;margin-bottom:7px">${esc(KIND_LABELS[g.kind])}</div>
          <h3 style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc(g.keeper?.name || '图片分组')}</h3>
          <p>${fmtCount(g.count)} 个文件 · 可释放 ${fmtBytes(g.wasted)}${simText}</p>
        </div>
        <div class="group-head-actions">
          <button class="btn btn-sm" type="button" data-action="ignore-group" title="以后不再提示这一组">${icon('eye-off')} 不是重复</button>
          <button class="modal-close" data-action="close-modal" aria-label="关闭">${icon('x')}</button>
        </div>
      </div>
      <div class="modal-body">
        <div class="preview-pane">
          ${canCompare ? `
          <div class="compare-bar" id="gm-compare-bar">
            <div class="segmented compare-modes">
              <button type="button" data-action="compare-mode" data-mode="single">单图</button>
              <button type="button" class="active" data-action="compare-mode" data-mode="split">并排</button>
            </div>
            <div class="compare-pickers" id="gm-compare-pickers">
              <label class="compare-pick compare-pick-a"><span>A</span><select id="gm-compare-base" data-compare-role="base" aria-label="对比图 A">${compareOptions}</select></label>
              <span class="compare-swap">↔</span>
              <label class="compare-pick compare-pick-b"><span>B</span><select id="gm-compare-target" data-compare-role="target" aria-label="对比图 B">${compareOptions}</select></label>
            </div>
            <div class="compare-tools" id="gm-compare-tools">
              <button class="btn btn-sm" type="button" data-action="compare-zoom" data-step="fit">适应</button>
              <button class="btn btn-sm" type="button" data-action="compare-zoom" data-step="reset" aria-label="重置缩放">${icon('refresh')}</button>
              <button class="btn btn-sm" type="button" data-action="compare-zoom" data-step="-" aria-label="缩小">${icon('minus')}</button>
              <span class="compare-zoom-label" id="gm-zoom-label">100%</span>
              <button class="btn btn-sm" type="button" data-action="compare-zoom" data-step="+" aria-label="放大">${icon('plus')}</button>
            </div>
          </div>` : ''}
          <div class="preview-stage" id="gm-preview-stage" title="Ctrl+单击标记清理">
            <img id="gm-preview-img" alt="" class="loaded" />
            ${canCompare ? `
            <div class="compare-stage" id="gm-compare-stage" hidden>
              <div class="compare-pane">
                <div class="compare-viewport" id="gm-compare-a-view">
                  <img id="gm-compare-a" alt="" />
                  <div class="compare-loading" id="gm-compare-a-loading"><span class="spinner"></span></div>
                </div>
                <div class="compare-caption" id="gm-compare-a-caption"></div>
              </div>
              <div class="compare-pane">
                <div class="compare-viewport" id="gm-compare-b-view">
                  <img id="gm-compare-b" alt="" />
                  <div class="compare-loading" id="gm-compare-b-loading"><span class="spinner"></span></div>
                </div>
                <div class="compare-caption" id="gm-compare-b-caption"></div>
              </div>
            </div>` : ''}
            <div class="preview-loading" id="gm-preview-loading"><span class="spinner"></span> 正在载入预览…</div>
          </div>
          <div class="preview-meta">
            <div class="preview-title"><strong id="gm-title"></strong><span id="gm-sim"></span></div>
            <div class="preview-detail" id="gm-detail"></div>
            <div class="preview-actions">
              <button class="btn btn-sm" type="button" data-action="open-file" data-path="${esc(previewMember?.path || '')}">${icon('external')} 打开图片</button>
              <button class="btn btn-sm" type="button" data-action="reveal-file" data-path="${esc(previewMember?.path || '')}">${icon('folder-open')} 定位文件</button>
            </div>
          </div>
        </div>
        <div class="members-pane">
          <div class="members-head">
            <h4>组内图片</h4>
            <span>Ctrl+单击选择 · Delete 清理</span>
          </div>
          <div class="member-list" id="gm-member-list">${rows}</div>
        </div>
      </div>
      <div class="modal-foot group-foot">
        <div class="selection-count group-foot-count" id="gm-footer-count"></div>
        <div class="group-foot-actions">
          <button class="btn" type="button" data-action="close-modal" id="gm-done-btn">完成</button>
          <button class="btn btn-danger" type="button" data-action="cleanup-group" id="gm-cleanup-btn" disabled>${icon('trash')} 清理选中</button>
          <button class="btn" type="button" data-action="cancel-group-cleanup" id="gm-cleanup-cancel" hidden>取消</button>
          <button class="btn btn-danger" type="button" data-action="confirm-group-cleanup" id="gm-cleanup-confirm" hidden>${icon('trash')} 确认清理</button>
        </div>
      </div>
    </div>
  `);
  updateGroupPreview(g, previewMember);
  refreshGroupModalSelection(g);
}

function canCompareGroup(g) {
  return Boolean(g && g.kind === 'similar' && (g.members || []).length > 1);
}

function currentPreviewMember(g) {
  if (!g) return null;
  const members = g.members || [];
  return members.find((m) => m.path === state.previewPath) || members[0] || null;
}

function compareBaseMember(g) {
  const members = g?.members || [];
  return members.find((m) => m.keeper) || members[0] || null;
}

function updateGroupPreview(g, member) {
  if (!member) return;
  state.previewPath = member.path;
  const split = canCompareGroup(g) && state.compareMode === 'split';
  const stage = $('#gm-preview-stage');
  stage?.classList.toggle('compare-active', split);
  const img = $('#gm-preview-img');
  const loading = $('#gm-preview-loading');
  if (split) {
    if (img) img.classList.remove('loaded');
    if (loading) loading.style.display = 'none';
    const compare = $('#gm-compare-stage');
    if (compare) compare.hidden = false;
    updateCompareTarget(g, member);
    updateCompareToolbar(g);
  } else {
    const compare = $('#gm-compare-stage');
    if (compare) compare.hidden = true;
    if (img) {
      img.classList.remove('loaded');
      if (loading) {
        loading.style.display = 'flex';
        loading.innerHTML = `<span class="spinner"></span> 正在载入预览…`;
      }
      img.onload = () => { img.classList.add('loaded'); if (loading) loading.style.display = 'none'; };
      img.onerror = () => {
        if (loading) {
          loading.style.display = 'flex';
          loading.innerHTML = `${icon('alert')} 预览载入失败`;
        }
      };
      img.src = `/api/preview?path=${encodeURIComponent(member.path)}&v=${member.modTime || 0}`;
      img.alt = member.name || '';
    }
    updateCompareToolbar(g);
  }
  const title = $('#gm-title');
  if (title) title.textContent = member.name || '';
  const sim = $('#gm-sim');
  if (sim) {
    if (g.kind === 'similar' && !member.keeper) sim.innerHTML = `<span class="chip chip-cyan" style="margin-left:6px;font-size:9px">相似度 ${(member.similarity || 0).toFixed(1)}%</span>`;
    else if (member.keeper) sim.innerHTML = `<span class="chip chip-green" style="margin-left:6px;font-size:9px">推荐保留</span>`;
    else sim.innerHTML = '';
  }
  const detail = $('#gm-detail');
  if (detail) {
    detail.innerHTML = `
      <span>${icon('image')} ${member.width || '?'} × ${member.height || '?'}</span>
      <span>${icon('hard-drive')} ${fmtBytes(member.size)}</span>
      <span>${icon('file')} ${esc((member.format || '').toUpperCase())}</span>
      <span title="${esc(member.path)}">${icon('folder')} ${esc(dirname(member.path))}</span>
    `;
  }
  $$('.member-row', $('#gm-member-list') || document).forEach((row) => {
    row.classList.toggle('active', row.dataset.memberPath === member.path);
  });
  const openBtn = $('[data-action="open-file"]');
  const revealBtn = $('[data-action="reveal-file"]');
  if (openBtn) openBtn.dataset.path = member.path;
  if (revealBtn) revealBtn.dataset.path = member.path;
}

function compareCaption(member, role) {
  if (!member) return '';
  return `<strong>${role} · ${esc(member.name || '')}</strong><span>${member.width || '?'} × ${member.height || '?'} · ${fmtBytes(member.size)}</span>`;
}

function loadCompareImage(img, loading, member) {
  if (!img || !member) return;
  const token = member.path + '|' + (member.modTime || 0);
  img.dataset.compareToken = token;
  img.classList.remove('loaded');
  if (loading) loading.style.display = 'flex';
  img.onload = () => {
    if (img.dataset.compareToken !== token) return;
    img.classList.add('loaded');
    if (loading) loading.style.display = 'none';
  };
  img.onerror = () => {
    if (img.dataset.compareToken !== token) return;
    img.classList.remove('loaded');
    if (loading) {
      loading.style.display = 'flex';
      loading.innerHTML = `${icon('alert')}`;
    }
  };
  img.src = `/api/preview?path=${encodeURIComponent(member.path)}&v=${member.modTime || 0}`;
  img.alt = member.name || '';
}

function updateCompareTarget(g, member) {
  if (!canCompareGroup(g)) return;
  const members = g.members || [];
  const base = members.find((m) => m.path === state.compareBasePath) || compareBaseMember(g);
  const target = member && base && member.path !== base.path
    ? member
    : members.find((m) => m.path !== base?.path) || member || base;
  if (!base || !target) return;
  state.compareBasePath = base.path;
  state.compareTargetPath = target.path;
  const a = $('#gm-compare-a');
  const b = $('#gm-compare-b');
  loadCompareImage(a, $('#gm-compare-a-loading'), base);
  loadCompareImage(b, $('#gm-compare-b-loading'), target);
  const capA = $('#gm-compare-a-caption');
  const capB = $('#gm-compare-b-caption');
  if (capA) capA.innerHTML = compareCaption(base, base.keeper ? 'A · 推荐保留' : 'A · 对比图');
  if (capB) capB.innerHTML = compareCaption(target, 'B · 对比图');
  updateComparePickers(base, target);
  updateCompareRoles(base, target);
  applyCompareTransform();
}

function updateComparePickers(base, target) {
  const a = $('#gm-compare-base');
  const b = $('#gm-compare-target');
  if (a && base) a.value = base.path;
  if (b && target) b.value = target.path;
}

function updateCompareRoles(base, target) {
  $$('.member-row', $('#gm-member-list') || document).forEach((row) => {
    const chip = row.querySelector('.compare-role');
    if (!chip) return;
    const path = row.dataset.memberPath;
    const role = state.compareMode === 'split'
      ? (path === base?.path ? 'A' : (path === target?.path ? 'B' : ''))
      : '';
    chip.hidden = !role;
    chip.textContent = role;
    chip.dataset.role = role;
    chip.title = role === 'A' ? '对比图 A' : role === 'B' ? '对比图 B' : '';
  });
}

function setComparePair(g, basePath, targetPath) {
  if (!canCompareGroup(g)) return;
  const members = g.members || [];
  const base = members.find((m) => m.path === basePath) || compareBaseMember(g);
  let target = members.find((m) => m.path === targetPath);
  if (!base) return;
  if (!target || target.path === base.path) {
    target = members.find((m) => m.path !== base.path);
  }
  if (!target) return;
  state.compareBasePath = base.path;
  state.compareTargetPath = target.path;
  state.previewPath = target.path;
  updateGroupPreview(g, target);
}

function focusMember(g, member) {
  if (!g || !member) return;
  if (state.compareMode === 'split' && canCompareGroup(g) && state.compareBasePath === member.path) {
    const current = currentPreviewMember(g);
    if (current && current.path !== member.path) {
      setComparePair(g, current.path, member.path);
      return;
    }
  }
  updateGroupPreview(g, member);
}

function handleCompareRoleChange(role, value) {
  const g = state.activeGroup;
  if (!canCompareGroup(g) || !value) return;
  const members = g.members || [];
  const currentBase = state.compareBasePath || compareBaseMember(g)?.path;
  const currentTarget = currentPreviewMember(g)?.path;
  if (role === 'base') {
    if (value === currentTarget && currentBase && currentBase !== value) {
      setComparePair(g, value, currentBase);
    } else {
      setComparePair(g, value, currentTarget);
    }
  } else {
    if (value === currentBase && currentTarget && currentTarget !== value) {
      setComparePair(g, currentTarget, value);
    } else {
      setComparePair(g, currentBase, value);
    }
  }
}

function applyCompareTransform() {
  const scale = state.compareZoom || 1;
  const transform = `translate3d(${state.comparePanX || 0}px, ${state.comparePanY || 0}px, 0) scale(${scale})`;
  $$('#gm-compare-a, #gm-compare-b').forEach((img) => {
    img.style.transform = transform;
  });
  const label = $('#gm-zoom-label');
  if (label) label.textContent = `${Math.round(scale * 100)}%`;
}

function resetCompareView() {
  state.compareZoom = 1;
  state.comparePanX = 0;
  state.comparePanY = 0;
  state.compareDrag = null;
  applyCompareTransform();
}

function updateCompareToolbar(g) {
  const bar = $('#gm-compare-bar');
  if (!bar) return;
  const split = state.compareMode === 'split';
  $$('[data-action="compare-mode"]', bar).forEach((btn) => {
    btn.classList.toggle('active', btn.dataset.mode === state.compareMode);
  });
  const tools = $('#gm-compare-tools');
  if (tools) tools.hidden = !split;
  const pickers = $('#gm-compare-pickers');
  if (pickers) pickers.hidden = !split;
  if (!split) updateCompareRoles(null, null);
  applyCompareTransform();
}

function setCompareMode(mode) {
  const g = state.activeGroup;
  if (!canCompareGroup(g)) return;
  state.compareMode = mode === 'split' ? 'split' : 'single';
  resetCompareView();
  let member = currentPreviewMember(g);
  if (state.compareMode === 'split') {
    const base = g.members?.find((m) => m.path === state.compareBasePath) || compareBaseMember(g);
    if (!member || (base && member.path === base.path)) {
      member = (g.members || []).find((m) => m.path !== base?.path) || member;
    }
  }
  updateGroupPreview(g, member);
}

function changeCompareZoom(step) {
  if (!canCompareGroup(state.activeGroup) || state.compareMode !== 'split') return;
  if (step === 'fit' || step === 'reset') {
    resetCompareView();
    return;
  }
  const delta = step === '+' ? 0.25 : -0.25;
  state.compareZoom = Math.min(4, Math.max(1, (state.compareZoom || 1) + delta));
  if (state.compareZoom === 1) {
    state.comparePanX = 0;
    state.comparePanY = 0;
  }
  applyCompareTransform();
}

function onCompareWheel(event) {
  if (!state.overlayOpen || !canCompareGroup(state.activeGroup) || state.compareMode !== 'split') return;
  if (!event.target.closest?.('#gm-compare-stage')) return;
  event.preventDefault();
  changeCompareZoom(event.deltaY > 0 ? '-' : '+');
}

function onComparePointerDown(event) {
  if (!state.overlayOpen || state.compareMode !== 'split' || !canCompareGroup(state.activeGroup)) return;
  if (event.button !== 0 || (state.compareZoom || 1) <= 1 || !event.target.closest?.('#gm-compare-stage') || event.target.closest('button')) return;
  event.preventDefault();
  state.compareDrag = {
    id: event.pointerId,
    x: event.clientX,
    y: event.clientY,
    panX: state.comparePanX,
    panY: state.comparePanY,
  };
  $('#gm-compare-stage')?.classList.add('dragging');
}

function onComparePointerMove(event) {
  const drag = state.compareDrag;
  if (!drag || event.pointerId !== drag.id) return;
  event.preventDefault();
  state.comparePanX = drag.panX + event.clientX - drag.x;
  state.comparePanY = drag.panY + event.clientY - drag.y;
  applyCompareTransform();
}

function onComparePointerUp(event) {
  const drag = state.compareDrag;
  if (!drag || (event.pointerId !== undefined && event.pointerId !== drag.id)) return;
  state.compareDrag = null;
  $('#gm-compare-stage')?.classList.remove('dragging');
}

function groupSelectedMembers(g = state.activeGroup) {
  if (!g) return [];
  return (g.members || []).filter((m) => state.selection.has(m.path));
}

function cleanupTargetName() {
  return state.settings?.cleanupMode === 'recycle' && state.boot?.recycle ? 'Windows 回收站' : 'PicDup 隔离区';
}

function refreshGroupModalSelection(g) {
  if (!g) return;
  const members = g.members || [];
  const unselected = members.filter((m) => !state.selection.has(m.path));
  const canSelectAnother = unselected.length > 1;
  $$('.member-row', $('#gm-member-list') || document).forEach((row) => {
    const path = row.dataset.memberPath;
    const selected = state.selection.has(path);
    row.classList.toggle('is-selected', selected);
    const btn = row.querySelector('.delete-toggle');
    if (!btn) return;
    btn.classList.toggle('on', selected);
    btn.disabled = !selected && !canSelectAnother;
    btn.title = selected ? '取消清理' : (canSelectAnother ? '标记清理' : '至少保留一张');
    btn.innerHTML = icon('trash');
  });
  const selected = groupSelectedMembers(g);
  const bytes = selected.reduce((sum, m) => sum + (m.size || 0), 0);
  const pending = state.groupCleanupPending && selected.length > 0;
  const footer = $('#gm-footer-count');
  if (footer) {
    footer.innerHTML = pending
      ? `确认清理 <strong>${selected.length}</strong> 张？将移入 ${cleanupTargetName()}`
      : selected.length
        ? `组内已选 <strong>${selected.length}</strong> 张 · ${fmtBytes(bytes)}`
        : '组内还没有选择待清理的文件 · Ctrl+单击标记';
  }
  const cleanupBtn = $('#gm-cleanup-btn');
  if (cleanupBtn) {
    cleanupBtn.hidden = pending;
    cleanupBtn.disabled = selected.length === 0 || state.cleanupBusy;
  }
  const doneBtn = $('#gm-done-btn');
  if (doneBtn) doneBtn.hidden = pending;
  const cancelBtn = $('#gm-cleanup-cancel');
  if (cancelBtn) {
    cancelBtn.hidden = !pending;
    cancelBtn.disabled = state.cleanupBusy;
  }
  const confirmBtn = $('#gm-cleanup-confirm');
  if (confirmBtn) {
    confirmBtn.hidden = !pending;
    confirmBtn.disabled = state.cleanupBusy;
  }
}

function requestGroupCleanup() {
  if (!state.activeGroup || state.cleanupBusy) return;
  const selected = groupSelectedMembers();
  if (!selected.length) {
    toast('先用 Ctrl+单击或垃圾桶标记要清理的图片', { type: 'info' });
    return;
  }
  if (state.settings?.confirmCleanup === false) {
    confirmCleanup(selected.map((m) => m.path));
    return;
  }
  state.groupCleanupPending = true;
  refreshGroupModalSelection(state.activeGroup);
  setTimeout(() => $('#gm-cleanup-confirm')?.focus(), 0);
}

function cancelGroupCleanup() {
  if (!state.activeGroup || state.cleanupBusy) return;
  state.groupCleanupPending = false;
  refreshGroupModalSelection(state.activeGroup);
  setTimeout(() => $('#gm-cleanup-btn')?.focus(), 0);
}

function confirmGroupCleanup() {
  if (!state.activeGroup || state.cleanupBusy) return;
  const selected = groupSelectedMembers();
  state.groupCleanupPending = false;
  if (!selected.length) {
    refreshGroupModalSelection(state.activeGroup);
    return;
  }
  confirmCleanup(selected.map((m) => m.path));
}

async function ignoreActiveGroup() {
  const g = state.activeGroup;
  if (!g) return;
  try {
    const data = await request('/api/ignore/group', { method: 'POST', body: { groupId: g.id } });
    const rule = data.rule;
    for (const m of (g.members || [])) {
      if (state.selection.delete(m.path)) {
        state.selectionBytes = Math.max(0, state.selectionBytes - (Number(m.size) || 0));
      }
    }
    closeOverlay();
    await refreshBootstrap().catch(() => {});
    renderSidebar();
    if (state.view === 'results') await loadResults(true);
    updateSelectionUI();
    const opts = {
      type: 'success',
      msg: '以后扫描不会再提示这一组。',
    };
    if (rule?.id) {
      opts.action = { label: '撤销', fn: () => undoIgnoreRule(rule.id) };
    }
    toast('已标记为不是重复', opts);
  } catch (err) {
    toast('无法标记忽略', { type: 'error', msg: err.message });
  }
}

async function undoIgnoreRule(id) {
  try {
    await request(`/api/ignore/${encodeURIComponent(id)}`, { method: 'DELETE', body: {} });
    await refreshBootstrap().catch(() => {});
    renderSidebar();
    if (state.view === 'results') await loadResults(true);
    updateSelectionUI();
    toast('已恢复忽略分组', { type: 'success', msg: '如果当前结果里还有这些文件，它们会重新参与分组。' });
  } catch (err) {
    toast('恢复失败', { type: 'error', msg: err.message });
  }
}

async function openIgnoreRules() {
  try {
    const data = await request('/api/ignore');
    const rules = data.rules || [];
    const list = rules.length ? rules.map((rule) => `
      <div class="ignore-row">
        <div class="ignore-main">
          <div class="ignore-title">${icon('eye-off')} ${esc(rule.label || '已忽略分组')}</div>
          <div class="ignore-meta">${esc(KIND_LABELS[rule.kind] || rule.kind)} · ${fmtCount(rule.count || 0)} 个文件</div>
        </div>
        <button class="btn btn-sm" type="button" data-action="restore-ignore" data-id="${esc(rule.id)}">${icon('undo')} 恢复</button>
      </div>
    `).join('') : `<div class="ignore-empty">${icon('eye-off')} 还没有标记“不是重复”的分组。</div>`;
    showOverlay(`
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="ignore-title">
        <div class="modal-head">
          <div>
            <h3 id="ignore-title">忽略记录</h3>
            <p>标记为“不是重复”的分组不会在以后的扫描结果中出现。</p>
          </div>
          <button class="modal-close" data-action="close-modal" aria-label="关闭">${icon('x')}</button>
        </div>
        <div class="modal-body">
          <div class="ignore-list">${list}</div>
        </div>
        <div class="modal-foot"><button class="btn" type="button" data-action="close-modal">完成</button></div>
      </div>
    `);
  } catch (err) {
    toast('无法读取忽略记录', { type: 'error', msg: err.message });
  }
}

async function restoreIgnoreRule(id) {
  if (!id) return;
  try {
    await request(`/api/ignore/${encodeURIComponent(id)}`, { method: 'DELETE', body: {} });
    await refreshBootstrap().catch(() => {});
    renderSidebar();
    if (state.view === 'results') await loadResults(true);
    updateSelectionUI();
    await openIgnoreRules();
    toast('已恢复忽略分组', { type: 'success' });
  } catch (err) {
    toast('恢复失败', { type: 'error', msg: err.message });
  }
}

function toggleMember(path, size) {
  const g = state.activeGroup;
  if (!g) return;
  if (state.groupCleanupPending) state.groupCleanupPending = false;
  const members = g.members || [];
  const unselected = members.filter((m) => !state.selection.has(m.path));
  if (!state.selection.has(path)) {
    if (unselected.length <= 1) {
      toast('每组至少保留一张图片', { type: 'warn' });
      return;
    }
    state.selection.add(path);
    state.selectionBytes += Number(size) || 0;
  } else {
    state.selection.delete(path);
    state.selectionBytes = Math.max(0, state.selectionBytes - (Number(size) || 0));
  }
  refreshGroupModalSelection(g);
  updateSelectionUI();
}

/* ---------------- trash ---------------- */

async function loadTrash() {
  if (state.view !== 'trash') return;
  state.trashLoading = true;
  renderView();
  try {
    const data = await request('/api/trash');
    state.trash = data.batches || [];
    state.trashSummary = data.summary || state.trashSummary;
    state.trashLoading = false;
    if (state.view === 'trash') renderView();
    updateChromeBadges();
  } catch (err) {
    state.trashLoading = false;
    if (state.view === 'trash') renderView();
    toast('加载隔离区失败', { type: 'error', msg: err.message });
  }
}

function renderTrash() {
  const batches = state.trash || [];
  if (state.trashLoading && !state.trash) {
    return `<section class="view trash-view"><div class="spinner" style="margin:80px auto"></div></section>`;
  }
  const list = batches.length ? batches.map((b) => {
    const items = (b.items || []).slice(0, 4);
    return `
      <div class="card batch-card" data-batch-id="${esc(b.id)}">
        <div class="batch-main">
          <div class="batch-title">${icon('clock')} ${fmtTime(b.createdAt, true)} <span class="chip">${fmtCount(b.count)} 个文件</span></div>
          <div class="batch-meta">隔离批次 ${esc(b.id)}</div>
          <div class="batch-items">
            ${items.map((it) => `<span class="batch-item" title="${esc(it.originalPath)}">${esc(it.name)}</span>`).join('')}
            ${b.count > items.length ? `<span class="batch-item">+${b.count - items.length}</span>` : ''}
          </div>
        </div>
        <div class="batch-size"><strong>${fmtBytes(b.totalSize)}</strong><span>已占用</span></div>
        <div class="batch-actions">
          <button class="btn btn-sm" type="button" data-action="restore-batch" data-id="${esc(b.id)}">${icon('rotate-ccw')} 恢复</button>
          <button class="btn btn-sm btn-danger" type="button" data-action="delete-batch" data-id="${esc(b.id)}">${icon('trash')} 永久删除</button>
        </div>
      </div>
    `;
  }).join('') : `
    <div class="empty-state">
      <div class="empty-icon" style="color:var(--green);background:var(--green-soft)">${icon('shield')}</div>
      <h3>隔离区是空的</h3>
      <p>清理的文件会先暂存在这里，确认无误后再永久删除。</p>
      <button class="btn" type="button" data-action="adjust-home">${icon('folder-open')} 去扫描图片</button>
    </div>
  `;
  return `
    <section class="view trash-view">
      <div class="info-banner">${icon('shield')} <span>清理操作默认进入隔离区，不会立即删除磁盘上的文件。你可以恢复任意批次；确认不再需要后，再永久删除以释放空间。</span></div>
      <div class="trash-list">${list}</div>
    </section>
  `;
}

async function restoreBatch(batchId) {
  try {
    const data = await request('/api/trash/restore', { method: 'POST', body: { batchId } });
    const restored = data.restore?.restored || 0;
    const failed = data.restore?.failed?.length || 0;
    toast(failed ? `恢复了 ${restored} 个，${failed} 个失败` : `已恢复 ${fmtCount(restored)} 个文件`, {
      type: failed ? 'warn' : 'success',
      msg: failed ? '失败的条目仍保留在隔离区。' : '文件已回到原来的位置。',
    });
    state.trashSummary = data.summary || state.trashSummary;
    await loadTrash();
    await refreshBootstrap().catch(() => {});
    renderSidebar();
  } catch (err) {
    toast('恢复失败', { type: 'error', msg: err.message });
  }
}

function deleteBatch(batchId) {
  const batch = (state.trash || []).find((b) => b.id === batchId);
  if (!batch) return;
  showConfirm({
    title: '永久删除这批文件？',
    message: `批次中的 ${fmtCount(batch.count)} 个文件将被永久删除，无法恢复。`,
    confirmLabel: '永久删除',
    danger: true,
    onConfirm: async () => {
      const data = await request('/api/trash/delete', { method: 'POST', body: { batchId } });
      state.trashSummary = data.summary || state.trashSummary;
      closeOverlay();
      toast('已永久删除', { type: 'success', msg: `释放 ${fmtBytes(batch.totalSize)}` });
      await loadTrash();
      await refreshBootstrap().catch(() => {});
      renderSidebar();
    },
  });
}

function emptyTrash() {
  const summary = state.trashSummary || { files: 0, bytes: 0 };
  showConfirm({
    title: '清空整个隔离区？',
    message: `将永久删除 ${fmtCount(summary.files)} 个文件，释放约 ${fmtBytes(summary.bytes)}。此操作无法恢复。`,
    confirmLabel: '清空并永久删除',
    danger: true,
    onConfirm: async () => {
      const data = await request('/api/trash/empty', { method: 'POST', body: {} });
      closeOverlay();
      toast('隔离区已清空', { type: 'success', msg: `释放 ${fmtBytes(data.bytes || 0)}` });
      state.trashSummary = data.summary || { batches: 0, files: 0, bytes: 0 };
      await loadTrash();
      await refreshBootstrap().catch(() => {});
      renderSidebar();
    },
  });
}

/* ---------------- settings ---------------- */

function renderSettings() {
  const s = state.settings || {};
  const cache = state.cache || { files: 0, bytes: 0, fingerprintFiles: 0, fingerprintBytes: 0 };
  const cachePct = Math.min(100, (cache.bytes / (2 * 1024 * 1024 * 1024)) * 100);
  const theme = s.theme || 'dark';
  const mode = s.cleanupMode === 'recycle' && state.boot?.recycle ? 'recycle' : 'trash';
  return `
    <section class="view settings-view">
      <div class="settings-grid">
        <div class="settings-col">
          <div class="card settings-card">
            <h3>${icon('sliders')} 扫描偏好</h3>
            <p>这些设置会成为下一次扫描的默认值。</p>
            <div class="setting-row">
              <div class="setting-row-info">
                <div class="setting-row-label">相似度阈值</div>
                <div class="setting-row-desc">${similarityPreset(s.similarity || 8)} · 约 ${similarityPercentOf(s.similarity || 8)}% 视觉相似才归组</div>
              </div>
              <div class="setting-range">
                <input class="range" id="setting-sim" type="range" min="2" max="16" step="1" value="${Number(s.similarity || 8)}" data-setting="similarity" aria-label="相似度阈值" />
                <span class="setting-range-value" id="setting-sim-value">${Number(s.similarity || 8)}</span>
              </div>
            </div>
            <div class="setting-row">
              <div class="setting-row-info"><div class="setting-row-label">包含子文件夹</div><div class="setting-row-desc">递归扫描所选文件夹下的所有层级</div></div>
              <label class="switch-row" style="padding:0"><input type="checkbox" data-setting="includeSubfolders" ${s.includeSubfolders !== false ? 'checked' : ''}/><span class="switch"></span></label>
            </div>
            <div class="setting-row">
              <div class="setting-row-info"><div class="setting-row-label">查找相似图片</div><div class="setting-row-desc">除逐字节查重外，再执行感知相似检测</div></div>
              <label class="switch-row" style="padding:0"><input type="checkbox" data-setting="findSimilar" ${s.findSimilar !== false ? 'checked' : ''}/><span class="switch"></span></label>
            </div>
            <div class="setting-row">
              <div class="setting-row-info"><div class="setting-row-label">包含隐藏文件/文件夹</div><div class="setting-row-desc">默认跳过 . 开头的隐藏项目</div></div>
              <label class="switch-row" style="padding:0"><input type="checkbox" data-setting="includeHidden" ${s.includeHidden ? 'checked' : ''}/><span class="switch"></span></label>
            </div>
            <div class="setting-row">
              <div class="setting-row-info"><div class="setting-row-label">最小文件大小</div><div class="setting-row-desc">小于该大小的图片会被跳过，0 表示不过滤</div></div>
              <div style="display:flex;align-items:center;gap:6px">
                <input class="btn" style="width:86px;text-align:right;user-select:text" type="number" min="0" step="10" value="${Number(s.minFileSizeKB) || 0}" data-setting="minFileSizeKB" aria-label="最小文件大小 KB" />
                <span style="font-size:10px;color:var(--dim)">KB</span>
              </div>
            </div>
            <div class="setting-row">
              <div class="setting-row-info"><div class="setting-row-label">推荐保留</div><div class="setting-row-desc">下次扫描和智能选择时生效</div></div>
              <div class="select-wrap keeper-inline">
                <select data-setting="keeperMode" aria-label="保留策略">
                  <option value="resolution" ${(s.keeperMode || 'resolution') === 'resolution' ? 'selected' : ''}>最高分辨率</option>
                  <option value="largest" ${s.keeperMode === 'largest' ? 'selected' : ''}>文件最大</option>
                  <option value="newest" ${s.keeperMode === 'newest' ? 'selected' : ''}>修改时间最新</option>
                  <option value="oldest" ${s.keeperMode === 'oldest' ? 'selected' : ''}>修改时间最早</option>
                  <option value="shortest" ${s.keeperMode === 'shortest' ? 'selected' : ''}>路径最短</option>
                </select>
                ${icon('chevron-down')}
              </div>
            </div>
            <div class="setting-row">
              <div class="setting-row-info"><div class="setting-row-label">扫描后自动智能选择</div><div class="setting-row-desc">扫描完成后默认勾选每组中多余的文件</div></div>
              <label class="switch-row" style="padding:0"><input type="checkbox" data-setting="autoSelect" ${s.autoSelect !== false ? 'checked' : ''}/><span class="switch"></span></label>
            </div>
          </div>

          <div class="card settings-card">
            <h3>${icon('trash')} 清理方式</h3>
            <p>决定“确认清理”后文件去哪里。</p>
            <div class="cleanup-options">
              <button class="cleanup-opt ${mode === 'trash' ? 'active' : ''}" type="button" data-action="cleanup-mode" data-mode="trash">
                ${icon('shield')}<div><strong>PicDup 隔离区 · 可恢复</strong><span>文件移动到本地隔离区，可随时恢复或永久删除，最安全。</span></div>
              </button>
              <button class="cleanup-opt ${mode === 'recycle' ? 'active' : ''}" type="button" data-action="cleanup-mode" data-mode="recycle" ${state.boot?.recycle ? '' : 'disabled style="opacity:.5"'}>
                ${icon('trash')}<div><strong>Windows 回收站 · 不可撤销</strong><span>使用系统回收站，PicDup 无法恢复。${state.boot?.recycle ? '' : (state.boot?.recycleNote || '')}</span></div>
              </button>
            </div>
            <div class="setting-row">
              <div class="setting-row-info">
                <div class="setting-row-label">清理前二次确认</div>
                <div class="setting-row-desc">关闭后点击“清理选中”会直接执行，不再弹出确认窗口</div>
              </div>
              <label class="switch-row" style="padding:0"><input type="checkbox" data-setting="confirmCleanup" ${s.confirmCleanup !== false ? 'checked' : ''}/><span class="switch"></span></label>
            </div>
            <div class="pref-note">无论选择哪种方式，每个分组都至少会保留一张图片。</div>
          </div>
        </div>

        <div class="settings-col">
          <div class="card settings-card">
            <h3>${icon('sun')} 外观</h3>
            <p>选择界面主题，切换后立即生效。</p>
            <div class="theme-options">
              <button class="theme-opt ${theme === 'dark' ? 'active' : ''}" type="button" data-action="theme-choice" data-theme="dark">${icon('moon')}<span>深色</span></button>
              <button class="theme-opt ${theme === 'light' ? 'active' : ''}" type="button" data-action="theme-choice" data-theme="light">${icon('sun')}<span>浅色</span></button>
              <button class="theme-opt ${theme === 'system' ? 'active' : ''}" type="button" data-action="theme-choice" data-theme="system">${icon('monitor')}<span>跟随系统</span></button>
            </div>
          </div>

          <div class="card settings-card">
            <h3>${icon('hard-drive')} 缓存与存储</h3>
            <p>缩略图缓存只用于加速界面显示，不会改动原图。</p>
            <div class="setting-row">
              <div class="setting-row-info"><div class="setting-row-label">缩略图缓存</div><div class="setting-row-desc">${fmtCount(cache.files)} 个文件 · ${fmtBytes(cache.bytes)}</div></div>
              <button class="btn btn-sm" type="button" data-action="clear-cache">${icon('broom')} 清除缓存</button>
            </div>
            <div class="cache-bar"><span style="width:${cache.bytes ? Math.max(1, cachePct) : 0}%"></span></div>
            <div class="setting-row">
              <div class="setting-row-info"><div class="setting-row-label">指纹缓存</div><div class="setting-row-desc">${fmtCount(cache.fingerprintFiles || 0)} 项 · ${fmtBytes(cache.fingerprintBytes || 0)} · 删除后下次扫描会重新计算</div></div>
              <button class="btn btn-sm" type="button" data-action="clear-fingerprint-cache">${icon('broom')} 清除</button>
            </div>
            <div class="setting-row">
              <div class="setting-row-info"><div class="setting-row-label">隔离区</div><div class="setting-row-desc">${fmtCount(state.trashSummary?.files || 0)} 个文件 · ${fmtBytes(state.trashSummary?.bytes || 0)}</div></div>
              <button class="btn btn-sm" type="button" data-action="go-trash">去管理</button>
            </div>
            <div class="setting-row">
              <div class="setting-row-info"><div class="setting-row-label">忽略记录</div><div class="setting-row-desc">${fmtCount(state.boot?.ignoreCount || 0)} 组已标记为“不是重复”</div></div>
              <button class="btn btn-sm" type="button" data-action="manage-ignore">管理</button>
            </div>
          </div>

          <div class="card settings-card">
            <h3>${icon('info')} 关于 PicDup</h3>
            <p>一个完全本地的图片去重与清理工具。</p>
            <div class="about-list">
              <div><span>版本</span><span>v${esc(state.boot?.version || '1.0.0')}</span></div>
              <div><span>运行平台</span><span>${esc(state.boot?.platform || 'windows')} · ${state.desktop ? '桌面窗口' : '本地服务模式'}</span></div>
              <div class="about-dir-row">
                <span>数据目录</span>
                <span class="about-dir-value" title="${esc(state.boot?.appDataDir || '')}">${esc(state.boot?.appDataDir || '')}</span>
                <button class="btn btn-sm about-dir-open" type="button" data-action="open-data-dir">${icon('folder-open')} 打开</button>
              </div>
              <div><span>存储模式</span><span>${state.boot?.dataPortable ? '便携 · 软件目录' : '系统应用数据目录'}</span></div>
              <div><span>检测方式</span><span>SHA-256 · pHash · 颜色直方图</span></div>
              <div><span>支持格式</span><span>${esc((state.boot?.formats || []).join(' / '))}</span></div>
            </div>
            <div class="about-actions">
              <button class="btn btn-sm" type="button" data-action="help">${icon('sparkles')} 快捷键</button>
              <button class="btn btn-sm" type="button" data-action="reset-settings">${icon('refresh')} 恢复默认设置</button>
            </div>
            <div class="pref-note">PicDup 不会联网上传任何图片。HEIC/AVIF/RAW 不参与扫描；超过 30,000 张唯一图片时，相似比对会自动跳过。${(state.boot?.dataWarnings || []).length ? `<br><span class="text-warn">${esc(state.boot.dataWarnings.join(' '))}</span>` : ''}</div>
          </div>
        </div>
      </div>
    </section>
  `;
}
function updateSettingsLocal(patch) {
  state.settings = { ...(state.settings || {}), ...patch };
  renderSidebar();
  if (state.view === 'settings') {
    // Keep focus on inputs instead of re-rendering the whole pane.
    if (patch.similarity !== undefined) {
      const value = Number(patch.similarity);
      $$('.range[data-setting="similarity"]').forEach((r) => {
        if (document.activeElement !== r) r.value = String(value);
        updateRangeVisual(r, value);
      });
      const label = $('#setting-sim-value');
      if (label) label.textContent = String(value);
      const desc = $('#setting-sim')?.closest('.setting-row')?.querySelector('.setting-row-desc');
      if (desc) desc.textContent = `${similarityPreset(value)} · 约 ${similarityPercentOf(value)}% 视觉相似才归组`;
    }
    if (patch.theme !== undefined) {
      $$('.theme-opt').forEach((b) => b.classList.toggle('active', b.dataset.theme === patch.theme));
    }
    if (patch.cleanupMode !== undefined) {
      $$('.cleanup-opt').forEach((b) => b.classList.toggle('active', b.dataset.mode === patch.cleanupMode));
    }
  }
  scheduleSettingsSave();
}

function scheduleSettingsSave() {
  clearTimeout(state.settingsSaveTimer);
  state.settingsSaveTimer = setTimeout(async () => {
    try {
      const saved = await request('/api/settings', { method: 'POST', body: state.settings });
      state.settings = saved;
      try { localStorage.setItem('picdup.theme', saved.theme); } catch { /* ignore */ }
    } catch (err) {
      toast('设置保存失败', { type: 'error', msg: err.message });
    }
  }, 450);
}

/* ---------------- overlay / modal / toast ---------------- */

function showOverlay(html) {
  const root = $('#overlay-root');
  if (!root) return;
  if (!state.overlayOpen) state.lastFocus = document.activeElement;
  state.overlayOpen = true;
  const wide = /class="[^"]*modal group-modal/.test(html);
  root.innerHTML = `<div class="overlay${wide ? ' overlay-wide' : ''}" data-overlay>${html}</div>`;
  setTimeout(() => {
    const target = root.querySelector('.modal button:not(.modal-close), .modal input');
    if (target) target.focus();
  }, 40);
}

function closeOverlay() {
  const root = $('#overlay-root');
  if (!root) return;
  root.innerHTML = '';
  state.overlayOpen = false;
  state.activeGroup = null;
  state.previewPath = null;
  state.groupCleanupPending = false;
  if (state.lastFocus && document.body.contains(state.lastFocus)) {
    try { state.lastFocus.focus(); } catch { /* ignore */ }
  }
  state.lastFocus = null;
}

function showConfirm({ title, message, confirmLabel = '确认', danger = false, onConfirm }) {
  showOverlay(`
    <div class="modal" role="dialog" aria-modal="true" aria-labelledby="confirm-title">
      <div class="modal-head">
        <div><h3 id="confirm-title">${esc(title)}</h3><p>${esc(message)}</p></div>
        <button class="modal-close" data-action="close-modal" aria-label="关闭">${icon('x')}</button>
      </div>
      <div class="modal-foot">
        <button class="btn" type="button" data-action="close-modal">取消</button>
        <button class="btn ${danger ? 'btn-danger' : 'btn-primary'}" type="button" id="confirm-action-btn">${esc(confirmLabel)}</button>
      </div>
    </div>
  `);
  const btn = $('#confirm-action-btn');
  if (btn) {
    btn.addEventListener('click', async () => {
      if (btn.disabled) return;
      btn.disabled = true;
      const original = btn.innerHTML;
      btn.innerHTML = `<span class="spinner"></span> 正在处理…`;
      try {
        await onConfirm();
      } catch (err) {
        btn.disabled = false;
        btn.innerHTML = original;
        toast('操作失败', { type: 'error', msg: err.message });
      }
    });
  }
}

function toast(title, { type = 'info', msg = '', timeout = 4500, action = null } = {}) {
  const root = $('#toast-root');
  if (!root) return;
  const node = document.createElement('div');
  node.className = `toast ${type}`;
  const iconName = type === 'success' ? 'check' : type === 'error' ? 'alert' : type === 'warn' ? 'alert' : 'info';
  node.innerHTML = `
    <div class="toast-icon">${icon(iconName)}</div>
    <div class="toast-body">
      <div class="toast-title">${esc(title)}</div>
      ${msg ? `<div class="toast-msg">${esc(msg)}</div>` : ''}
    </div>
    ${action ? `<button class="toast-action" type="button">${esc(action.label)}</button>` : ''}
    <button class="toast-close" type="button" aria-label="关闭">${icon('x')}</button>
  `;
  const remove = () => {
    if (!node.isConnected) return;
    node.classList.add('leaving');
    setTimeout(() => node.remove(), 220);
  };
  node.querySelector('.toast-close')?.addEventListener('click', remove);
  if (action) {
    node.querySelector('.toast-action')?.addEventListener('click', () => {
      remove();
      try { action.fn(); } catch (err) { toast('操作失败', { type: 'error', msg: err.message }); }
    });
  }
  root.appendChild(node);
  if (timeout > 0) setTimeout(remove, timeout);
  while (root.children.length > 4) root.firstElementChild.remove();
}

/* ---------------- images ---------------- */

let imageObserver = null;

function setupImageObserver() {
  if (!('IntersectionObserver' in window)) return;
  imageObserver = new IntersectionObserver((entries) => {
    for (const entry of entries) {
      if (!entry.isIntersecting) continue;
      const img = entry.target;
      imageObserver.unobserve(img);
      loadThumb(img);
    }
  }, { rootMargin: '240px 0px' });
}

function loadThumb(img) {
  const src = img.dataset.thumb;
  if (!src) return;
  img.loading = 'eager';
  img.addEventListener('load', () => img.classList.add('loaded'), { once: true });
  img.addEventListener('error', () => {
    img.dataset.failed = '1';
    const tile = img.closest('.tile');
    if (tile && !tile.querySelector('.mosaic-empty')) {
      const placeholder = document.createElement('div');
      placeholder.className = 'mosaic-empty';
      placeholder.innerHTML = icon('image');
      tile.appendChild(placeholder);
    }
  }, { once: true });
  img.src = src;
}

function hydrateImages(root = document) {
  const images = $$('img[data-thumb]', root).filter((img) => !img.closest('.carousel-card.pos-hidden'));
  if (!imageObserver) {
    images.forEach(loadThumb);
    return;
  }
  images.forEach((img) => {
    if (img.dataset.observed === '1') return;
    img.dataset.observed = '1';
    imageObserver.observe(img);
  });
}

/* ---------------- help ---------------- */

function showHelp() {
  showOverlay(`
    <div class="modal" role="dialog" aria-modal="true" aria-labelledby="help-title">
      <div class="modal-head">
        <div><h3 id="help-title">PicDup 使用说明</h3><p>本地图片去重与相似检测的几点须知。</p></div>
        <button class="modal-close" data-action="close-modal" aria-label="关闭">${icon('x')}</button>
      </div>
      <div class="modal-body">
        <div class="help-section">
          <h4>两种检测方式</h4>
          <p><strong>完全重复</strong>按 SHA-256 校验文件内容，只有逐字节相同才会归为一组。<br/><strong>相似图片</strong>使用感知哈希（pHash）、差异哈希与颜色直方图，能识别缩放、压缩、裁剪和轻微调色后的变体。</p>
        </div>
        <div class="help-section">
          <h4>相似度阈值</h4>
          <p>数值越小越严格。默认 8 适合大多数照片；如果希望找出裁剪较多的图片，可以尝试 10–12。阈值越大，误报的可能性也越高。</p>
        </div>
        <div class="help-section">
          <h4>清理与恢复</h4>
          <p>清理默认移入隔离区，可在“隔离区”页面恢复或永久删除；也可以在设置里改为使用 Windows 回收站。每个分组至少会保留一张图片。</p>
        </div>
        <div class="help-section">
          <h4>快捷键</h4>
          <div class="shortcut-grid">
            <span class="kbd">Ctrl</span><span>+</span><span class="kbd">O</span><span>选择文件夹</span>
            <span class="kbd">Enter</span><span></span><span></span><span>开始扫描（在开始页）</span>
            <span class="kbd">/</span><span></span><span></span><span>聚焦搜索框（在结果页）</span>
            <span class="kbd">Esc</span><span></span><span></span><span>关闭弹窗</span>
          </div>
        </div>
        <div class="help-section">
          <h4>已知限制</h4>
          <p>暂不支持 HEIC/AVIF 解码；超过 30,000 张唯一图片时会跳过相似比对以保证性能。所有数据与缩略图都保存在本机。</p>
        </div>
      </div>
      <div class="modal-foot"><button class="btn btn-primary" data-action="close-modal">知道了</button></div>
    </div>
  `);
}

/* ---------------- dropped files ---------------- */

async function startDroppedPolling() {
  if (state.droppedTimer) return;
  const tick = async () => {
    try {
      const data = await request('/api/dropped', { timeout: 8000 });
      if (data?.paths?.length && !state.status?.scanning && !state.status?.cleaning) {
        const path = data.paths.find(Boolean);
        if (path && addRoot(path)) {
          if (state.view !== 'home') setView('home');
          toast('已选择文件夹', { type: 'success', msg: basename(path) });
        }
      }
    } catch {
      /* ignore dropped polling errors */
    }
    state.droppedTimer = setTimeout(tick, 1500);
  };
  state.droppedTimer = setTimeout(tick, 800);
}

/* ---------------- events ---------------- */

function bindEvents() {
  if (bindEvents.bound) return;
  bindEvents.bound = true;
  setupImageObserver();
  document.addEventListener('click', onDocumentClick);
  document.addEventListener('change', onDocumentChange);
  document.addEventListener('input', onDocumentInput);
  document.addEventListener('keydown', onKeyDown);
  document.addEventListener('wheel', onResultsWheel, { passive: false });
  document.addEventListener('wheel', onCompareWheel, { passive: false });
  document.addEventListener('pointerdown', onComparePointerDown);
  document.addEventListener('pointermove', onComparePointerMove);
  document.addEventListener('pointerup', onComparePointerUp);
  document.addEventListener('pointercancel', onComparePointerUp);
  window.addEventListener('dragenter', onDragEnter);
  window.addEventListener('dragover', onDragOver);
  window.addEventListener('dragleave', onDragLeave);
  window.addEventListener('drop', onDrop);
  window.addEventListener('dragend', hideDropOverlay);
  window.addEventListener('blur', hideDropOverlay);
  const media = window.matchMedia('(prefers-color-scheme: light)');
  media.addEventListener?.('change', () => {
    if (state.settings?.theme === 'system') applyTheme('system', false);
  });
}

function onDocumentClick(event) {
  const nav = event.target.closest('[data-nav]');
  if (nav && nav.classList.contains('nav-item')) {
    event.preventDefault();
    setView(nav.dataset.nav);
    if (nav.dataset.nav === 'results' && state.result && !state.groups.length && !state.loadingResults) {
      loadResults(true);
    }
    return;
  }
  const overlay = event.target.closest('[data-overlay]');
  if (overlay && event.target === overlay) {
    closeOverlay();
    return;
  }
  const previewStage = event.target.closest('#gm-preview-stage');
  if (previewStage && (event.ctrlKey || event.metaKey) && !event.target.closest('button')) {
    event.preventDefault();
    const member = state.activeGroup?.members?.find((m) => m.path === state.previewPath);
    if (member) {
      toggleMember(member.path, member.size);
      updateGroupPreview(state.activeGroup, member);
    }
    return;
  }
  const el = event.target.closest('[data-action]');
  if (!el) return;
  if (el.disabled) return;
  const action = el.dataset.action;
  if (!action) return;
  switch (action) {
    case 'retry-init':
      window.location.reload();
      break;
    case 'add-folder':
      event.preventDefault();
      addFolderFlow();
      break;
    case 'start-scan':
      startScan();
      break;
    case 'cancel-scan':
      cancelScan();
      break;
    case 'rescan':
      startScan();
      break;
    case 'home':
      setView('home');
      break;
    case 'adjust-home':
      if (!state.status?.scanning) state.homePhase = 'idle';
      setView('home');
      break;
    case 'flip-home':
      if (state.status?.scanning) break;
      state.homePhase = 'idle';
      updateChrome();
      syncHomeFrontButton();
      setScanFlipped(false);
      break;
    case 'go-settings':
      setView('settings');
      break;
    case 'show-scan':
      setView('home');
      break;
    case 'view-results':
      setView('results');
      if (!state.groups.length) loadResults(true);
      break;
    case 'open-results':
      if (state.result) {
        setView('results');
        if (!state.groups.length) loadResults(true);
      } else {
        setView('home');
      }
      break;
    case 'switch-kind':
      switchKind(el.dataset.kind);
      break;
    case 'smart-select':
      smartSelect();
      break;
    case 'clear-selection':
      clearSelection();
      break;
    case 'cleanup':
      requestCleanup();
      break;
    case 'confirm-cleanup':
      confirmCleanup();
      break;
    case 'cleanup-group':
      requestGroupCleanup();
      break;
    case 'ignore-group':
      ignoreActiveGroup();
      break;
    case 'manage-ignore':
      openIgnoreRules();
      break;
    case 'compare-mode':
      setCompareMode(el.dataset.mode);
      break;
    case 'compare-zoom':
      changeCompareZoom(el.dataset.step);
      break;
    case 'restore-ignore':
      restoreIgnoreRule(el.dataset.id);
      break;
    case 'cancel-group-cleanup':
      cancelGroupCleanup();
      break;
    case 'confirm-group-cleanup':
      confirmGroupCleanup();
      break;
    case 'toggle-group':
      event.preventDefault();
      event.stopPropagation();
      toggleGroup(el.dataset.id);
      break;
    case 'open-group':
      openGroup(el.dataset.groupId || el.dataset.id);
      break;
    case 'carousel-card': {
      const idx = Number(el.dataset.index);
      if (!Number.isFinite(idx)) break;
      if (idx === state.carouselIndex) openGroup(el.dataset.groupId || el.dataset.id);
      else setCarouselIndex(idx);
      break;
    }
    case 'load-more':
      loadMore();
      break;
    case 'close-modal':
      closeOverlay();
      break;
    case 'preview-member': {
      event.preventDefault();
      const row = el.closest('.member-row');
      const path = el.dataset.memberPath || row?.dataset.memberPath;
      const member = state.activeGroup?.members?.find((m) => m.path === path);
      if (!member) break;
      if (event.ctrlKey || event.metaKey) {
        toggleMember(path, member.size);
        updateGroupPreview(state.activeGroup, member);
        break;
      }
      focusMember(state.activeGroup, member);
      break;
    }
    case 'toggle-member': {
      event.preventDefault();
      event.stopPropagation();
      toggleMember(el.dataset.path, Number(el.dataset.size) || 0);
      break;
    }
    case 'open-file':
    case 'reveal-file':
      event.preventDefault();
      callOpen(el.dataset.path, action === 'reveal-file');
      break;
    case 'restore-batch':
      restoreBatch(el.dataset.id);
      break;
    case 'delete-batch':
      deleteBatch(el.dataset.id);
      break;
    case 'empty-trash':
      emptyTrash();
      break;
    case 'go-trash':
      setView('trash');
      break;
    case 'clear-cache':
      clearCache();
      break;
    case 'clear-fingerprint-cache':
      clearFingerprintCache();
      break;
    case 'open-data-dir':
      openDataDir();
      break;
    case 'reset-settings':
      resetSettings();
      break;
    case 'theme-choice':
      chooseTheme(el.dataset.theme);
      break;
    case 'cleanup-mode':
      if (el.dataset.mode === 'recycle' && !state.boot?.recycle) {
        toast('当前平台不支持回收站', { type: 'warn', msg: state.boot?.recycleNote || '' });
        return;
      }
      updateSettingsLocal({ cleanupMode: el.dataset.mode });
      break;
    case 'help':
      showHelp();
      break;
    case 'theme-toggle': {
      const current = resolvedTheme(state.settings?.theme || 'dark');
      chooseTheme(current === 'dark' ? 'light' : 'dark');
      break;
    }
    default:
      break;
  }
}

function onDocumentChange(event) {
  const el = event.target;
  if (!el) return;
  if (el.dataset?.compareRole) {
    handleCompareRoleChange(el.dataset.compareRole, el.value);
    return;
  }
  if (el.id === 'result-sort') {
    state.sort = el.value;
    loadResults(true);
    return;
  }
  const setting = el.dataset?.setting;
  if (setting) {
    if (el.type === 'checkbox') {
      updateSettingsLocal({ [setting]: el.checked });
    } else if (el.type === 'number' || el.type === 'range') {
      updateSettingsLocal({ [setting]: Number(el.value) });
    } else if (el.tagName === 'SELECT') {
      updateSettingsLocal({ [setting]: el.value });
    }
    return;
  }
}

function onDocumentInput(event) {
  const el = event.target;
  if (!el) return;
  if (el.id === 'result-search') {
    state.query = el.value;
    clearTimeout(state.searchTimer);
    state.searchTimer = setTimeout(() => loadResults(true, { soft: true }), 280);
    return;
  }
  if (el.dataset?.homeSim !== undefined) {
    const value = Number(el.value);
    updateRangeVisual(el, value);
    updateSettingsLocal({ similarity: value });
    const label = $('#home-sim-value');
    if (label) label.textContent = String(value);
    const desc = $('#home-sim-desc');
    if (desc) desc.textContent = `${similarityPreset(value)} · 约 ${similarityPercentOf(value)}% 视觉相似才归组`;
    return;
  }
  if (el.dataset?.setting === 'similarity') {
    const value = Number(el.value);
    updateRangeVisual(el, value);
    const label = $('#setting-sim-value');
    if (label) label.textContent = String(value);
    state.settings = { ...(state.settings || {}), similarity: value };
  }
}

function isEditableTarget(target) {
  const el = target instanceof Element ? target : null;
  if (!el) return false;
  return Boolean(el.closest('input, textarea, select, [contenteditable="true"], [contenteditable=""]'));
}

function onKeyDown(event) {
  if (event.key === 'Escape') {
    if (state.groupCleanupPending) {
      cancelGroupCleanup();
      event.preventDefault();
      return;
    }
    if (state.overlayOpen) {
      closeOverlay();
      event.preventDefault();
    }
    return;
  }
  if (event.key === 'Delete' && state.overlayOpen && state.activeGroup && !isEditableTarget(event.target)) {
    event.preventDefault();
    if (state.groupCleanupPending) confirmGroupCleanup();
    else requestGroupCleanup();
    return;
  }
  if (!state.overlayOpen && state.view === 'results' && (event.key === 'ArrowLeft' || event.key === 'ArrowRight') && !isEditableTarget(event.target)) {
    event.preventDefault();
    moveCarousel(event.key === 'ArrowRight' ? 1 : -1);
    return;
  }
  const mod = event.ctrlKey || event.metaKey;
  if (mod && event.key.toLowerCase() === 'o') {
    event.preventDefault();
    addFolderFlow();
    return;
  }
  if (mod && event.key.toLowerCase() === 'f' && state.view === 'results') {
    event.preventDefault();
    $('#result-search')?.focus();
    return;
  }
  if (event.key === '/' && state.view === 'results') {
    const tag = document.activeElement?.tagName;
    if (!['INPUT', 'TEXTAREA', 'SELECT'].includes(tag)) {
      event.preventDefault();
      $('#result-search')?.focus();
    }
    return;
  }
  if ((event.key === 'Enter' || event.key === ' ') && !state.overlayOpen) {
    const card = document.activeElement?.closest?.('[role="button"][data-action]');
    if (card) {
      event.preventDefault();
      card.click();
      return;
    }
  }
  if (event.key === 'Enter' && state.view === 'home' && !state.overlayOpen) {
    const tag = document.activeElement?.tagName;
    if (!['INPUT', 'TEXTAREA', 'SELECT', 'BUTTON', 'A'].includes(tag)) {
      event.preventDefault();
      startScan();
    }
  }
}

let dragCounter = 0;
function hasFiles(event) {
  return Array.from(event.dataTransfer?.types || []).includes('Files');
}
function onDragEnter(event) {
  if (!hasFiles(event)) return;
  event.preventDefault();
  dragCounter++;
  $('#dropzone')?.classList.add('drag-over');
}
function onDragOver(event) {
  if (!hasFiles(event)) return;
  event.preventDefault();
  event.dataTransfer.dropEffect = 'copy';
  $('#dropzone')?.classList.add('drag-over');
}
function onDragLeave() {
  dragCounter = Math.max(0, dragCounter - 1);
  if (dragCounter === 0) hideDropOverlay();
}
function hideDropOverlay() {
  dragCounter = 0;
  $('#dropzone')?.classList.remove('drag-over');
}
function onDrop(event) {
  event.preventDefault();
  hideDropOverlay();
  // In the desktop app Wails reports real paths through /api/dropped.
  if (!state.desktop) {
    toast('浏览器模式无法读取文件夹路径', { type: 'info', msg: '请在 PicDup 桌面窗口中使用拖放。' });
  }
}

async function callOpen(path, reveal) {
  if (!path) return;
  try {
    await request('/api/open', { method: 'POST', body: { path, reveal } });
  } catch (err) {
    toast('无法打开文件', { type: 'error', msg: err.message });
  }
}

function chooseTheme(theme) {
  if (!['dark', 'light', 'system'].includes(theme)) return;
  updateSettingsLocal({ theme });
  applyTheme(theme);
  try { localStorage.setItem('picdup.theme', theme); } catch { /* ignore */ }
  if (state.view === 'settings') renderView();
}

async function clearCache() {
  try {
    const data = await request('/api/cache/clear', { method: 'POST', body: {} });
    state.cache = data;
    toast('缓存已清除', { type: 'success', msg: '缩略图会在下次浏览时按需重建。' });
    if (state.view === 'settings') renderView();
  } catch (err) {
    toast('清除缓存失败', { type: 'error', msg: err.message });
  }
}

async function clearFingerprintCache() {
  try {
    const data = await request('/api/cache/clear-fingerprints', { method: 'POST', body: {} });
    state.cache = data;
    toast('指纹缓存已清除', { type: 'success', msg: '下次扫描会重新计算图片指纹。' });
    if (state.view === 'settings') renderView();
  } catch (err) {
    toast('清除指纹缓存失败', { type: 'error', msg: err.message });
  }
}

async function openDataDir() {
  const dir = state.boot?.appDataDir;
  if (!dir) return;
  try {
    await request('/api/open', { method: 'POST', body: { path: dir } });
  } catch (err) {
    toast('无法打开数据目录', { type: 'error', msg: err.message });
  }
}

function resetSettings() {
  showConfirm({
    title: '恢复默认设置？',
    message: '扫描偏好、清理方式、主题和保留策略会恢复默认；忽略记录、隔离区和数据不会删除。',
    confirmLabel: '恢复默认',
    onConfirm: async () => {
      const saved = await request('/api/settings/reset', { method: 'POST', body: {} });
      state.settings = saved;
      applyTheme(saved.theme || 'dark');
      closeOverlay();
      if (state.view === 'settings') renderView();
      toast('已恢复默认设置', { type: 'success' });
    },
  });
}

/* ---------------- start ---------------- */

init();
