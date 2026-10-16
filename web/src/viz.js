// web/src/viz.js
// Visualizations: alternative views of the checked-out workspace that take over
// the center pane. The server lists the available visualizers (/api/viz/list)
// and serves one model per visualizer for the current branch (/api/viz); each
// visualizer here registers a renderer under the same id.
//
// Adding a visualizer: implement a Go provider in viz.go, then write a module
// that calls registerVisualizer({ id, mount }) and import it below. mount()
// receives the stage element and returns an instance:
//   render(result)  draw the server model ({ data, branch, head, stale })
//   menu()          optional extra Controls-menu items, see menuItem below
//   resize()        optional, the stage changed size
//   destroy()       release everything (GPU, listeners, timers)
// A menu item is { label, run, checked?, disabled? }, or { header } / { sep: true }.

import { $, api } from './state.js';
import { layout, render } from './renderer.js';
import { showToast } from './ui.js';
import { registerCodeCity } from './codecity.js';

/** @type {Map<string, {id: string, mount: (stage: HTMLElement, env: any) => any}>} */
const renderers = new Map();

export function registerVisualizer(def) {
  renderers.set(def.id, def);
}

let available = [];       // from /api/viz/list
let activeId = null;
let instance = null;
let lastResult = null;
let loading = false;
let loadSeq = 0;          // drops results of a load superseded by a newer one

const menuBtn = () => $('#btn-viz');
const pickMenu = () => $('#viz-menu');
const ctlMenu = () => $('#viz-controls-menu');

export const activeVisualizer = () => activeId;

function setStatus(text) {
  const el = $('#viz-status');
  if (el) el.textContent = text || '';
}

function menuItemEl(item, onDone) {
  if (item.sep) {
    const hr = document.createElement('div');
    hr.className = 'viz-menu-sep';
    return hr;
  }
  if (item.header) {
    const h = document.createElement('div');
    h.className = 'viz-menu-header';
    h.textContent = item.header;
    return h;
  }
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'viz-menu-item' + (item.checked ? ' active' : '');
  b.setAttribute('role', 'menuitem');
  b.disabled = !!item.disabled;
  const check = document.createElement('span');
  check.className = 'viz-check';
  check.textContent = '✓';
  const body = document.createElement('span');
  const title = document.createElement('span');
  title.className = 'viz-menu-title';
  title.textContent = item.label;
  body.appendChild(title);
  if (item.desc) {
    const d = document.createElement('span');
    d.className = 'viz-menu-desc';
    d.textContent = item.desc;
    body.appendChild(d);
  }
  b.append(check, body);
  b.addEventListener('click', () => { onDone(); item.run?.(); });
  return b;
}

function fillMenu(menu, items, onDone) {
  menu.replaceChildren(...items.map(it => menuItemEl(it, onDone)));
}

function closeMenus() {
  const pm = pickMenu(), cm = ctlMenu();
  if (pm) pm.hidden = true;
  if (cm) cm.hidden = true;
  menuBtn()?.setAttribute('aria-expanded', 'false');
  $('#viz-controls-btn')?.setAttribute('aria-expanded', 'false');
}

function togglePickMenu() {
  const menu = pickMenu(), btn = menuBtn();
  if (!menu || !btn) return;
  if (!menu.hidden) { closeMenus(); return; }
  closeMenus();
  const items = [{ header: 'Visualizations' }];
  if (!available.length) items.push({ label: 'No visualizations available', disabled: true });
  for (const v of available) {
    items.push({
      label: v.name, desc: v.description, checked: v.id === activeId,
      disabled: !renderers.has(v.id),
      run: () => (v.id === activeId ? closeVisualizer() : openVisualizer(v.id)),
    });
  }
  if (activeId) items.push({ sep: true }, { label: 'Back to editor', run: closeVisualizer });
  fillMenu(menu, items, closeMenus);
  menu.hidden = false;
  // Anchor above the button: the button lives in the sidebar footer.
  const r = btn.getBoundingClientRect();
  menu.style.left = Math.max(8, r.left) + 'px';
  menu.style.bottom = Math.max(8, innerHeight - r.top + 6) + 'px';
  menu.style.top = 'auto';
  btn.setAttribute('aria-expanded', 'true');
}

function toggleControlsMenu() {
  const menu = ctlMenu(), btn = $('#viz-controls-btn');
  if (!menu || !btn) return;
  if (!menu.hidden) { closeMenus(); return; }
  closeMenus();
  const items = [
    { label: loading ? 'Refreshing…' : 'Refresh', desc: 'Rebuild from the code as it is now', disabled: loading, run: () => loadVisualizer(true) },
    { sep: true },
    ...(instance?.menu?.() || []),
    { sep: true },
    { label: 'Back to editor', run: closeVisualizer },
  ];
  fillMenu(menu, items, closeMenus);
  menu.hidden = false;
  btn.setAttribute('aria-expanded', 'true');
}

function showResult(result) {
  lastResult = result;
  const branch = $('#viz-branch');
  if (branch) {
    branch.hidden = !result.branch;
    branch.textContent = result.branch || '';
    branch.title = result.head ? `Built from ${result.branch} @ ${result.head}` : '';
  }
  const stale = $('#viz-stale');
  if (stale) stale.hidden = !result.stale;
  instance?.render(result);
}

async function loadVisualizer(refresh) {
  const id = activeId;
  if (!id || !instance) return;
  const seq = ++loadSeq;
  loading = true;
  setStatus(refresh ? 'Refreshing…' : 'Building…');
  try {
    const result = await api('/api/viz', refresh ? { id, refresh: '1' } : { id });
    if (seq !== loadSeq || id !== activeId) return;
    showResult(result);
    setStatus('');
    if (refresh) showToast('✓', 'Visualization refreshed');
  } catch (e) {
    if (seq !== loadSeq) return;
    setStatus('');
    showToast('!', `Visualization failed: ${e.message}`, 4000);
    instance?.fail?.(e.message);
  } finally {
    if (seq === loadSeq) loading = false;
  }
}

export async function openVisualizer(id) {
  const def = renderers.get(id);
  if (!def) return;
  if (activeId) teardown();
  activeId = id;
  document.body.classList.add('viz-active');
  $('#vizview').hidden = false;
  const meta = available.find(v => v.id === id);
  $('#viz-title').textContent = meta?.name || id;
  const stage = $('#viz-stage');
  stage.replaceChildren();
  lastResult = null;
  instance = def.mount(stage, { refresh: () => loadVisualizer(true), close: closeVisualizer, setStatus });
  stage.focus({ preventScroll: true });
  await loadVisualizer(false);
}

function teardown() {
  loadSeq++;
  loading = false;
  try { instance?.destroy?.(); } catch (e) { console.error('visualizer destroy failed', e); }
  instance = null;
  activeId = null;
  lastResult = null;
  $('#viz-stage')?.replaceChildren();
  setStatus('');
}

export function closeVisualizer() {
  if (!activeId) return;
  teardown();
  closeMenus();
  document.body.classList.remove('viz-active');
  $('#vizview').hidden = true;
  // The editor was display:none, so its measurements are stale.
  layout();
  render();
}

export function initViz() {
  registerCodeCity(registerVisualizer);

  menuBtn()?.addEventListener('click', e => { e.stopPropagation(); togglePickMenu(); });
  $('#viz-controls-btn')?.addEventListener('click', e => { e.stopPropagation(); toggleControlsMenu(); });
  document.addEventListener('click', e => {
    if (!e.target.closest?.('#viz-menu, #viz-controls-menu')) closeMenus();
  });
  addEventListener('resize', closeMenus);

  api('/api/viz/list').then(j => { available = j.visualizers || []; }).catch(() => {});

  const ro = new ResizeObserver(() => instance?.resize?.());
  const stage = $('#viz-stage');
  if (stage) ro.observe(stage);
  // Keep the visualizer's keys (WASD, Esc ...) from reaching the editor shortcuts.
  stage?.addEventListener('keydown', e => e.stopPropagation());

  // A checkout or commit happens in a terminal; check on returning to the window.
  // The server answers from its per-branch cache (building a branch it has not
  // seen), so only a changed branch or staleness flag costs a re-render.
  addEventListener('focus', syncVisualizer);
}

async function syncVisualizer() {
  const id = activeId;
  if (!id || !instance || loading || !lastResult) return;
  try {
    const result = await api('/api/viz', { id });
    if (id !== activeId || loading) return;
    const prev = lastResult;
    if (result.branch !== prev.branch || (result.head !== prev.head && !result.stale)) showResult(result);
    else if (result.stale !== prev.stale) { prev.stale = result.stale; $('#viz-stale').hidden = !result.stale; }
  } catch { /* the next explicit load reports errors */ }
}
