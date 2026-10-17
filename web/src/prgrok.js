// web/src/prgrok.js
// PRGrok: the sidebar footer's PR button opens a pull request browser in the
// left pane and an explained diff in the center. Standalone: it talks only to
// /api/prgrok/*. Each AI action (Explain Hunks, Explain visually, Narrate) is a
// single agent run on the harness picked in the Agent bar. Server side:
// prgrok.go; prompts: prgrok/prompts/.
import { api, apiPostJson, esc } from './state.js';
import { applyAgentMeta, registerAgentPicker, syncAgentSelection } from './agent.js';
import { openThread } from './thread.js';
import { layout, render } from './renderer.js';
import { closeVisualizer } from './viz.js';
import { renderMermaidBlocks } from './mermaid.js';
import { PRG_VOICES, prgIsVoice, prgTtsLoad, prgTtsReady, prgTtsTry, prgCreatePlayer } from './prgrok-tts.js';

const PRG_VOICE_KEY = 'px0.prgrok.voice';

const prgS = {
  open: false,
  info: null,
  list: { items: [], page: 1, hasMore: false, state: 'open' },
  pr: null,          // { meta, files }
  mode: 'diff',      // diff | explain | visual
  scope: 'pr',       // view only: pr (all files) | file (one at a time). Never changes what the agent explains.
  layout: 'split',   // split | unified
  file: '',          // selected file id for scope=file
  results: {},       // cache of server results by prgKey
  narr: null,        // active narration { source, sections }
  run: null,         // { thread, action } while the agent works
  seq: 0,            // bumped when the PR changes, so stale replies are dropped
  active: null,      // section id being spoken
  voiceShown: false,
  voice: { voice: '', rate: 1, minutes: 3, locale: 'en-US', gender: 'female' },
  player: null,
  listSeq: 0,
};

const prgEl = id => document.getElementById(id);

// ---------------------------------------------------------------- helpers

/** Tiny Markdown for agent text: paragraphs, lists, `code`, **bold**. */
function prgMd(src) {
  const inline = t => esc(t)
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  const out = [];
  let list = null;
  const flush = () => { if (list) { out.push(`<${list.tag}>${list.items.join('')}</${list.tag}>`); list = null; } };
  for (const block of String(src || '').replace(/\r/g, '').split(/\n{2,}/)) {
    const lines = block.split('\n').filter(l => l.trim());
    if (lines.length && lines.every(l => /^\s*(\d+[.)]|[-*])\s+/.test(l))) {
      flush();
      const tag = /^\s*\d/.test(lines[0]) ? 'ol' : 'ul';
      out.push(`<${tag}>${lines.map(l => `<li>${inline(l.replace(/^\s*(\d+[.)]|[-*])\s+/, ''))}</li>`).join('')}</${tag}>`);
    } else if (lines.length) {
      flush();
      out.push(`<p>${lines.map(inline).join('<br>')}</p>`);
    }
  }
  flush();
  return out.join('');
}

// Results are for the whole PR, so the key never depends on the view.
const prgKey = (action, source) => (action === 'narrate' ? `narrate|${source}` : action);
const prgFiles = () => prgS.pr?.files || [];
const prgShownFiles = () => prgS.scope === 'file' ? prgFiles().filter(f => f.id === prgS.file) : prgFiles();
const prgResult = action => prgS.results[prgKey(action)] || null;

function prgSetStatus(msg, isError) {
  const el = prgEl('prg-status');
  el.textContent = msg || '';
  el.classList.toggle('prg-error', !!isError);
}

function prgSetBusy(busy) {
  for (const id of ['prg-diff', 'prg-explain', 'prg-visual', 'prg-narrate', 'prg-regen']) {
    const b = prgEl(id);
    if (b) b.disabled = busy || (id === 'prg-regen' && (prgS.mode === 'diff' || !prgS.pr));
  }
  prgEl('prg-cancel').hidden = !busy;
  prgEl('prg-thread').hidden = !busy;
  // The shared picker disables itself while its element is busy; no change of harness mid-run.
  prgEl('prg-agent').classList.toggle('busy', busy);
  applyAgentMeta();
}

// ---------------------------------------------------------------- list (left pane)

async function prgLoadList(page = 1) {
  const seq = ++prgS.listSeq;
  const list = prgEl('prg-list');
  list.innerHTML = '<div class="prg-empty">Loading pull requests...</div>';
  try {
    const r = await api('/api/prgrok/prs', { page, state: prgEl('prg-state').value || 'open' });
    if (seq !== prgS.listSeq) return;
    prgS.list = r;
    prgDrawList();
  } catch (e) {
    if (seq !== prgS.listSeq) return;
    list.innerHTML = `<div class="prg-empty prg-error">${esc(e.message)}</div>`;
    prgEl('prg-prev').disabled = prgEl('prg-next').disabled = true;
    prgEl('prg-page').textContent = '';
  }
}

function prgDrawList() {
  const { items, page, hasMore } = prgS.list;
  const list = prgEl('prg-list');
  if (!items.length) list.innerHTML = '<div class="prg-empty">No pull requests.</div>';
  else {
    list.innerHTML = items.map(p => `
      <button class="prg-pr${prgS.pr?.meta.number === p.number ? ' active' : ''}" data-n="${p.number}" title="${esc(p.title)}">
        <span class="prg-pr-top"><span class="prg-pr-n">#${p.number}</span>
          <span class="prg-pr-state prg-st-${esc(p.state)}">${esc(p.draft ? 'draft' : p.state)}</span></span>
        <span class="prg-pr-title">${esc(p.title)}</span>
        <span class="prg-pr-sub">${esc(p.author)}${p.source ? ' &middot; ' + esc(p.source) + ' &rarr; ' + esc(p.dest) : ''}</span>
      </button>`).join('');
  }
  prgEl('prg-prev').disabled = page <= 1;
  prgEl('prg-next').disabled = !hasMore;
  prgEl('prg-page').textContent = `Page ${page}`;
}

// ---------------------------------------------------------------- opening a PR

async function prgOpenPR(number) {
  prgStopAll();
  const seq = ++prgS.seq;
  prgS.pr = null; prgS.results = {}; prgS.narr = null; prgS.mode = 'diff'; prgS.file = '';
  prgEl('prg-controls').hidden = true;
  prgEl('prg-title').textContent = `PR #${number}`;
  prgEl('prg-stage').innerHTML = '<div class="prg-empty">Loading diff...</div>';
  prgDrawList();
  prgSetStatus('');
  try {
    const r = await api('/api/prgrok/pr', { number });
    if (seq !== prgS.seq) return;
    prgS.pr = r;
    prgS.file = r.files[0]?.id || '';
    prgEl('prg-title').innerHTML = `<a href="${esc(r.meta.url)}" target="_blank" rel="noopener">#${r.meta.number}</a> ${esc(r.meta.title)}`;
    prgEl('prg-controls').hidden = false;
    prgFillFileSelect();
    prgSyncControls();
    prgDrawList();
    await prgDraw();
    prgPeekAll();
  } catch (e) {
    if (seq !== prgS.seq) return;
    prgEl('prg-stage').innerHTML = `<div class="prg-empty prg-error">${esc(e.message)}</div>`;
  }
}

function prgFillFileSelect() {
  const sel = prgEl('prg-file');
  sel.innerHTML = prgFiles().map(f => `<option value="${f.id}">${esc(f.path)} (+${f.additions} -${f.deletions})</option>`).join('');
  sel.value = prgS.file;
}

/** Load results already saved on disk so reopening a PR, or changing the view, costs no agent run. */
async function prgPeekAll() {
  const seq = prgS.seq;
  const number = prgS.pr.meta.number;
  for (const action of ['explain', 'visual']) {
    if (prgS.results[prgKey(action)]) continue;
    try {
      const r = await api('/api/prgrok/result', { number, action });
      if (seq !== prgS.seq) return;
      if (r.cached) prgS.results[prgKey(action)] = r.result;
    } catch { /* a missing result is not an error */ }
  }
  if (seq === prgS.seq) { prgNarrationForView(); prgDraw(); }
}

// ---------------------------------------------------------------- running the agent

async function prgRunAction(action, { refresh = false, source = '' } = {}) {
  if (prgS.run || !prgS.pr) return null;
  const seq = prgS.seq;
  const key = prgKey(action, source);
  const minutes = Math.max(1, Math.min(30, Number(prgEl('prg-minutes').value) || 3));
  prgS.run = { thread: '', action };
  prgSetBusy(true);
  prgSetStatus('Starting...');
  try {
    // Run on the harness and model the dropdowns show, like every composer.
    if (!await syncAgentSelection(prgEl('prg-harness').value, prgEl('prg-model').value)) throw new Error('could not select that harness');
    const r = await apiPostJson('/api/prgrok/run', { number: prgS.pr.meta.number, action, source, minutes, refresh });
    let result = r.cached ? r.result : null;
    if (!result) {
      prgS.run.thread = r.thread;
      prgShowThread();
      prgSetStatus('Working in the Agent bar. The explanation appears here when it finishes.');
      result = await prgPoll(r.thread, seq);
    }
    if (seq !== prgS.seq || !result) return null;
    prgS.results[key] = result;
    prgSetStatus(result.harness ? `Done (${result.harness}${result.model ? ' / ' + result.model : ''}). Saved for next time.` : '');
    return result;
  } catch (e) {
    if (seq === prgS.seq) prgShowError(e.message, e.body?.raw);
    return null;
  } finally {
    prgS.run = null;
    if (seq === prgS.seq) prgSetBusy(false);
  }
}

function prgShowThread() {
  if (prgS.run?.thread) openThread(prgS.run.thread);
}

function prgPoll(thread, seq) {
  return new Promise((resolve, reject) => {
    const tick = async () => {
      if (seq !== prgS.seq || !prgS.run || prgS.run.thread !== thread) return resolve(null);
      try {
        const j = await api('/api/prgrok/job', { id: thread });
        if (j.running) return setTimeout(tick, 1000);
        if (j.error) return reject(Object.assign(new Error(j.error), { body: j }));
        resolve(j.result);
      } catch (e) { reject(e); }
    };
    tick();
  });
}

function prgShowError(msg, raw) {
  prgSetStatus(msg, true);
  if (raw) console.warn('PRGrok agent output:', raw);
}

async function prgCancel() {
  const id = prgS.run?.thread;
  if (id) { try { await apiPostJson('/api/threads/cancel', { id }); } catch { /* already finished */ } }
  prgS.run = null;
  prgSetBusy(false);
  prgSetStatus('Cancelled.');
}

// ---------------------------------------------------------------- actions

async function prgSetMode(mode) {
  if (!prgS.pr || prgS.run) return;
  prgS.mode = mode;
  prgSyncControls();
  if (mode !== 'diff' && !prgResult(mode)) {
    try {
      const r = await api('/api/prgrok/result', { number: prgS.pr.meta.number, action: mode });
      if (r.cached) prgS.results[prgKey(mode)] = r.result;
    } catch { /* no saved result */ }
    if (!prgResult(mode)) {
      prgDraw();
      await prgRunAction(mode);
    }
  }
  prgNarrationForView();
  prgDraw();
}

async function prgNarrate() {
  if (!prgS.pr || prgS.run) return;
  prgShowVoice();
  if (prgS.mode === 'diff' || !prgResult(prgS.mode)) {
    prgSetStatus('Run Explain Hunks or Explain visually first: there is nothing to narrate yet.', true);
    return;
  }
  const source = prgS.mode;
  let res = prgS.results[prgKey('narrate', source)];
  if (!res) {
    res = await prgRunAction('narrate', { source });
    if (!res) return;
  }
  prgApplyNarration(res, source);
  prgDraw();
}

function prgApplyNarration(res, source) {
  prgS.narr = { source, sections: res.sections || [] };
  prgS.player.setSections(prgViewSections());
  prgSyncTransport();
}

/** Narration is for the whole PR; the player speaks only what the view shows. */
function prgViewSections() {
  const all = prgS.narr?.sections || [];
  if (prgS.scope !== 'file' || !prgS.file) return all;
  const mine = new RegExp(`^${prgS.file}(h|$)`);
  return all.filter(x => mine.test(x.id));
}

async function prgRegenerate() {
  if (!prgS.pr || prgS.run || prgS.mode === 'diff') return;
  const res = await prgRunAction(prgS.mode, { refresh: true });
  if (res) {
    // A regenerated explanation invalidates narration built from the old one.
    prgS.narr = null;
    delete prgS.results[prgKey('narrate', 'explain')];
    delete prgS.results[prgKey('narrate', 'visual')];
    prgS.player.setSections([]);
    prgSyncTransport();
  }
  prgDraw();
}

function prgViewChanged() {
  prgS.scope = prgEl('prg-scope').value;
  prgS.file = prgEl('prg-file').value || prgS.file;
  prgEl('prg-file').hidden = prgS.scope !== 'file';
  prgNarrationForView();
  prgDraw();
}

/** Follow the view: the saved narration for the shown mode, limited to what is on screen. */
function prgNarrationForView() {
  prgStopAll();
  const src = prgS.mode === 'diff' ? '' : prgS.mode;
  const res = src ? prgS.results[prgKey('narrate', src)] : null;
  if (res) prgApplyNarration(res, src);
  else { prgS.narr = null; prgS.player.setSections([]); prgSyncTransport(); }
}

function prgSyncControls() {
  for (const b of document.querySelectorAll('.prg-mode')) b.classList.toggle('active', b.dataset.mode === prgS.mode);
  prgEl('prg-scope').value = prgS.scope;
  prgEl('prg-file').hidden = prgS.scope !== 'file';
  prgSetBusy(!!prgS.run);
}

// ---------------------------------------------------------------- rendering

async function prgDraw() {
  if (!prgS.open) return;
  const stage = prgEl('prg-stage');
  const keepTop = stage.scrollTop;
  stage.replaceChildren();
  if (!prgS.pr) return;
  const frag = document.createDocumentFragment();
  const res = prgS.mode === 'diff' ? null : prgResult(prgS.mode);

  const body = prgS.pr.meta.body?.trim();
  if (prgS.mode === 'diff' && body) {
    const d = document.createElement('details');
    d.className = 'prg-desc';
    d.innerHTML = `<summary>Description</summary><div class="prg-md">${prgMd(body)}</div>`;
    frag.append(d);
  }
  frag.append(prgStats());
  if (prgS.mode !== 'diff' && !res && !prgS.run) {
    frag.append(prgNotice('No result yet. Use Regenerate to run the agent.'));
  }
  if (res && prgS.scope === 'pr') {
    // The overall note and diagrams are about the whole PR, so they belong to the whole-PR view.
    if (res.overall) frag.append(prgNote(res.overall, 'overall', 'Overall'));
    if (prgS.mode === 'visual' && res.diagrams?.length) frag.append(prgDiagrams(res.diagrams));
  }
  const files = prgShownFiles();
  if (!files.length) frag.append(prgNotice('No changes in this diff.'));
  for (const f of files) frag.append(prgFileBlock(f, res));
  stage.append(frag);
  stage.scrollTop = keepTop;
  await renderMermaidBlocks(stage, () => prgS.open);
}

/** Files changed, lines added and removed: always for the whole PR, whichever view is showing. */
function prgStats() {
  const n = prgFiles().length;
  const el = document.createElement('div');
  el.className = 'prg-stats';
  el.innerHTML = `<span><strong>${n}</strong> file${n === 1 ? '' : 's'} changed</span>` +
    `<span class="prg-plus">+${prgS.pr.additions ?? 0} added</span>` +
    `<span class="prg-minus">&minus;${prgS.pr.deletions ?? 0} removed</span>`;
  return el;
}

const prgNotice = text => Object.assign(document.createElement('div'), { className: 'prg-empty', textContent: text });

function prgNote(text, id, label, cls = '') {
  const box = document.createElement('div');
  box.className = `prg-note ${cls}`;
  box.dataset.sid = id;
  if (prgS.active === id) box.classList.add('speaking');
  const head = document.createElement('div');
  head.className = 'prg-note-head';
  head.innerHTML = `<span class="prg-note-label">${esc(label || '')}</span>`;
  if (prgS.narr?.sections.some(s => s.id === id)) {
    const pill = document.createElement('button');
    pill.className = 'prg-pill';
    pill.title = 'Speak this note';
    pill.dataset.sid = id;
    pill.innerHTML = '&#128266; speak';
    head.append(pill);
  }
  const md = document.createElement('div');
  md.className = 'prg-md';
  md.innerHTML = prgMd(text);
  box.append(head, md);
  return box;
}

function prgDiagrams(diagrams) {
  const wrap = document.createElement('div');
  wrap.className = 'prg-diagrams';
  const mermaid = src => {
    const m = document.createElement('div');
    m.className = 'md-pre';
    m.dataset.lang = 'mermaid';
    const pre = document.createElement('pre');
    const code = document.createElement('code');
    code.textContent = src;
    pre.append(code);
    m.append(pre);
    return m;
  };
  for (const d of diagrams) {
    const card = document.createElement('div');
    card.className = 'prg-diagram';
    card.dataset.sid = d.id;
    if (prgS.active === d.id) card.classList.add('speaking');
    const title = document.createElement('div');
    title.className = 'prg-diagram-title';
    title.textContent = `${d.title || d.type} (${d.type})`;
    if (prgS.narr?.sections.some(s => s.id === d.id)) {
      const pill = document.createElement('button');
      pill.className = 'prg-pill';
      pill.dataset.sid = d.id;
      pill.innerHTML = '&#128266; speak';
      title.append(pill);
    }
    card.append(title);
    if (d.before) {
      const lbl = document.createElement('div');
      lbl.className = 'prg-diagram-sub';
      lbl.textContent = 'Before';
      card.append(lbl, mermaid(d.before));
      const lbl2 = document.createElement('div');
      lbl2.className = 'prg-diagram-sub';
      lbl2.textContent = 'After';
      card.append(lbl2);
    }
    card.append(mermaid(d.mermaid));
    const ex = document.createElement('div');
    ex.className = 'prg-md';
    ex.innerHTML = prgMd(d.explanation);
    card.append(ex);
    wrap.append(card);
  }
  return wrap;
}

function prgFileBlock(f, res) {
  const block = document.createElement('section');
  block.className = 'prg-file';
  block.innerHTML = `<div class="prg-file-head"><span class="prg-fs prg-fs-${f.status}">${f.status[0].toUpperCase()}</span>
    <span class="prg-file-path">${esc(f.path)}</span>
    <span class="prg-counts"><span class="prg-plus">+${f.additions}</span> <span class="prg-minus">-${f.deletions}</span></span></div>`;
  const fileNote = res?.files?.find(x => x.id === f.id);
  if (fileNote?.summary) block.append(prgNote(fileNote.summary, f.id, 'File', 'prg-note-file'));
  if (f.binary) {
    block.append(prgNotice('Binary file'));
    return block;
  }
  for (const h of f.hunks) {
    const hunkNote = fileNote?.hunks?.find(x => x.id === h.id);
    const hh = document.createElement('div');
    hh.className = 'prg-hunk-head';
    hh.textContent = h.header;
    block.append(hh);
    if (hunkNote?.summary) block.append(prgNote(hunkNote.summary, h.id, 'Hunk', 'prg-note-hunk'));
    block.append(prgHunkTable(h, hunkNote?.elements || []));
  }
  return block;
}

/** Pair deleted lines with the added lines that replace them, the way GitHub's split view does. */
function prgPairRows(lines) {
  const rows = [];
  for (let i = 0; i < lines.length;) {
    const l = lines[i];
    if (l.t === 'c') { rows.push({ left: l, right: l, ctx: true }); i++; continue; }
    if (l.t === 'm') { rows.push({ meta: l }); i++; continue; }
    const dels = [], adds = [];
    while (i < lines.length && lines[i].t === 'd') dels.push(lines[i++]);
    while (i < lines.length && lines[i].t === 'a') adds.push(lines[i++]);
    if (!dels.length && !adds.length) { i++; continue; }
    for (let k = 0; k < Math.max(dels.length, adds.length); k++) rows.push({ left: dels[k] || null, right: adds[k] || null });
  }
  return rows;
}

const prgCell = (cls, no, text, mark) => {
  const no_ = document.createElement('span');
  no_.className = 'prg-no';
  no_.textContent = no || '';
  const code = document.createElement('span');
  code.className = `prg-code ${cls}`;
  code.textContent = (mark || '') + (text ?? '');
  return [no_, code];
};

function prgHunkTable(h, elements) {
  const table = document.createElement('div');
  table.className = `prg-lines prg-${prgS.layout}`;
  const pending = [...elements].sort((a, b) => a.line - b.line);
  // An element note sits just above the first row at or after the line it anchors to.
  const flushNotes = n => {
    while (pending.length && n && n >= pending[0].line) {
      const e = pending.shift();
      table.append(prgNote(e.summary, e.id, `${e.kind || 'element'}${e.name ? ' ' + e.name : ''}`, 'prg-note-element'));
    }
  };
  if (prgS.layout === 'unified') {
    for (const l of h.lines) {
      flushNotes(l.n);
      const row = document.createElement('div');
      row.className = `prg-row-u prg-l-${l.t}`;
      row.append(...prgCell('', l.o, l.x, l.t === 'a' ? '+' : l.t === 'd' ? '-' : ' ').slice(0, 1),
        ...prgCell('', l.n, l.x, l.t === 'a' ? '+' : l.t === 'd' ? '-' : ' '));
      table.append(row);
    }
  } else {
    for (const r of prgPairRows(h.lines)) {
      if (r.meta) {
        const m = document.createElement('div');
        m.className = 'prg-row-meta';
        m.textContent = r.meta.x;
        table.append(m);
        continue;
      }
      flushNotes(r.right?.n);
      const row = document.createElement('div');
      row.className = 'prg-row-s';
      const left = r.left ? prgCell(r.ctx ? 'ctx' : 'del', r.left.o, r.left.x) : prgCell('empty', '', '');
      const right = r.right ? prgCell(r.ctx ? 'ctx' : 'add', r.right.n, r.right.x) : prgCell('empty', '', '');
      row.append(...left, ...right);
      table.append(row);
    }
  }
  for (const e of pending) table.append(prgNote(e.summary, e.id, e.name || 'element', 'prg-note-element'));
  return table;
}

// ---------------------------------------------------------------- narration and voice

function prgLoadVoiceSettings() {
  try { Object.assign(prgS.voice, JSON.parse(localStorage.getItem(PRG_VOICE_KEY) || '{}')); } catch { /* defaults */ }
}
const prgSaveVoiceSettings = () => { try { localStorage.setItem(PRG_VOICE_KEY, JSON.stringify(prgS.voice)); } catch { /* private mode */ } };

function prgFillVoiceSelects() {
  const v = prgS.voice;
  const locales = [...new Set(PRG_VOICES.map(x => x.locale))];
  prgEl('prg-locale').innerHTML = locales.map(l => `<option>${l}</option>`).join('');
  prgEl('prg-gender').innerHTML = '<option value="female">Female</option><option value="male">Male</option>';
  prgEl('prg-locale').value = v.locale;
  prgEl('prg-gender').value = v.gender;
  prgFillVoiceList();
  prgEl('prg-rate').value = v.rate;
  prgEl('prg-minutes').value = v.minutes;
}

function prgFillVoiceList() {
  const v = prgS.voice;
  const opts = PRG_VOICES.filter(x => x.locale === v.locale && x.gender === v.gender);
  const sel = prgEl('prg-voice-id');
  sel.innerHTML = '<option value="">Choose a voice...</option>' + opts.map(x => `<option value="${x.id}">${x.name}</option>`).join('');
  sel.value = opts.some(x => x.id === v.voice) ? v.voice : '';
  if (!sel.value) v.voice = '';
  prgSyncVoiceButtons();
}

function prgSyncVoiceButtons() {
  const has = prgIsVoice(prgS.voice.voice);
  prgEl('prg-dl').disabled = !has || prgTtsReady();
  prgEl('prg-dl').textContent = prgTtsReady() ? 'Downloaded' : 'Download';
  prgEl('prg-try').disabled = !has;
}

function prgShowVoice() {
  prgS.voiceShown = true;
  prgEl('prg-voice').hidden = false;
}

function prgSyncTransport() {
  const p = prgS.player;
  const has = p.hasSections();
  prgEl('prg-play').disabled = !has || p.isPlaying();
  prgEl('prg-pause').disabled = !has || !p.isPlaying();
  prgEl('prg-prevs').disabled = !has || !p.canPrev();
  prgEl('prg-nexts').disabled = !has || !p.canNext();
  prgEl('prg-stops').disabled = !has;
  if (has) prgShowVoice();
}

function prgActivate(section) {
  prgS.active = section?.id || null;
  for (const el of document.querySelectorAll('#prg-stage .speaking')) el.classList.remove('speaking');
  if (!section) return;
  const el = [...document.querySelectorAll('#prg-stage [data-sid]')].find(n => n.dataset.sid === section.id && !n.classList.contains('prg-pill'));
  if (el) {
    el.classList.add('speaking');
    el.scrollIntoView({ block: 'center', behavior: 'smooth' });
  }
}

function prgStopAll() { prgS.player?.stop(); }

function prgInitVoiceUI() {
  prgLoadVoiceSettings();
  prgFillVoiceSelects();
  prgS.player = prgCreatePlayer({
    getVoice: () => prgS.voice,
    setStatus: m => { prgEl('prg-voice-status').textContent = m || ''; },
    onActivate: prgActivate,
    onState: prgSyncTransport,
  });
  const status = m => { prgEl('prg-voice-status').textContent = m || ''; };
  prgEl('prg-locale').onchange = e => { prgS.voice.locale = e.target.value; prgFillVoiceList(); prgSaveVoiceSettings(); };
  prgEl('prg-gender').onchange = e => { prgS.voice.gender = e.target.value; prgFillVoiceList(); prgSaveVoiceSettings(); };
  prgEl('prg-voice-id').onchange = e => { prgS.voice.voice = e.target.value; prgS.player.voiceChanged(); prgSyncVoiceButtons(); prgSaveVoiceSettings(); };
  prgEl('prg-rate').onchange = e => { prgS.voice.rate = Math.min(2, Math.max(0.5, Number(e.target.value) || 1)); prgS.player.voiceChanged(); prgSaveVoiceSettings(); };
  prgEl('prg-minutes').onchange = e => { prgS.voice.minutes = Math.min(30, Math.max(1, Number(e.target.value) || 3)); prgSaveVoiceSettings(); };
  prgEl('prg-dl').onclick = async () => {
    prgEl('prg-dl').disabled = true;
    try { await prgTtsLoad(status); } catch (e) { status(`Could not load the voice model: ${e.message}`); }
    prgSyncVoiceButtons();
  };
  prgEl('prg-try').onclick = async () => {
    try { await prgTtsTry(prgS.voice.voice, prgS.voice.rate, status); } catch (e) { status(e.message); }
    prgSyncVoiceButtons();
  };
  prgEl('prg-play').onclick = () => prgS.player.play();
  prgEl('prg-pause').onclick = () => prgS.player.pause();
  prgEl('prg-prevs').onclick = () => prgS.player.prev();
  prgEl('prg-nexts').onclick = () => prgS.player.next();
  prgEl('prg-stops').onclick = () => prgS.player.stop();
  prgSyncTransport();
}

// ---------------------------------------------------------------- open / close

async function prgOpen() {
  if (prgS.open) return;
  closeVisualizer();
  prgS.open = true;
  document.body.classList.add('prgrok-active');
  prgEl('btn-prgrok')?.classList.add('prg-on');
  prgEl('prgrok-panel').hidden = false;
  prgEl('prgrokview').hidden = false;
  try {
    prgS.info = await api('/api/prgrok/info');
  } catch (e) {
    prgS.info = { reason: e.message, states: ['open'] };
  }
  const info = prgS.info;
  prgEl('prg-repo').textContent = info.repo ? `${info.provider === 'bitbucket' ? 'Bitbucket' : 'GitHub'} ${info.repo}` : 'PRGrok';
  prgEl('prg-state').innerHTML = (info.states || ['open']).map(s => `<option value="${s}">${s[0].toUpperCase() + s.slice(1)}</option>`).join('');
  if (!info.provider) {
    prgEl('prg-list').innerHTML = `<div class="prg-empty prg-error">${esc(info.reason || 'No GitHub or Bitbucket remote found.')}</div>`;
    prgEl('prg-stage').innerHTML = '<div class="prg-empty">PRGrok needs a project with a GitHub or Bitbucket remote.</div>';
    return;
  }
  if (info.tool && !info.toolOk) {
    prgEl('prg-list').innerHTML = `<div class="prg-empty prg-error">The ${esc(info.tool)} CLI is not installed or not on PATH.</div>`;
    return;
  }
  if (!prgS.pr) prgEl('prg-stage').innerHTML = '<div class="prg-empty">Pick a pull request on the left.</div>';
  prgLoadList(prgS.list.page || 1);
}

export function closePRGrok() {
  if (!prgS.open) return;
  prgS.open = false;
  prgStopAll();
  document.body.classList.remove('prgrok-active');
  prgEl('btn-prgrok')?.classList.remove('prg-on');
  prgEl('prgrok-panel').hidden = true;
  prgEl('prgrokview').hidden = true;
  layout();
  render();
}

export function initPRGrok() {
  try { prgS.layout = localStorage.getItem('px0.prgrok.layout') === 'unified' ? 'unified' : 'split'; } catch { /* default */ }
  prgInitVoiceUI();
  prgEl('btn-prgrok')?.addEventListener('click', () => (prgS.open ? closePRGrok() : prgOpen()));
  prgEl('prg-close').onclick = closePRGrok;
  prgEl('prg-refresh').onclick = () => prgLoadList(prgS.list.page);
  prgEl('prg-state').onchange = () => prgLoadList(1);
  prgEl('prg-prev').onclick = () => prgLoadList(Math.max(1, prgS.list.page - 1));
  prgEl('prg-next').onclick = () => prgLoadList(prgS.list.page + 1);
  prgEl('prg-list').addEventListener('click', e => {
    const b = e.target.closest('.prg-pr');
    if (b) prgOpenPR(Number(b.dataset.n));
  });
  prgEl('prg-scope').onchange = prgViewChanged;
  prgEl('prg-file').onchange = prgViewChanged;
  prgEl('prg-layout').value = prgS.layout;
  prgEl('prg-layout').onchange = e => { prgS.layout = e.target.value; try { localStorage.setItem('px0.prgrok.layout', prgS.layout); } catch { /* private mode */ } prgDraw(); };
  prgEl('prg-thread').onclick = prgShowThread;
  registerAgentPicker({ el: prgEl('prg-agent'), harnessSelect: prgEl('prg-harness'), modelSelect: prgEl('prg-model') });
  for (const b of document.querySelectorAll('.prg-mode')) b.onclick = () => prgSetMode(b.dataset.mode);
  prgEl('prg-narrate').onclick = prgNarrate;
  prgEl('prg-regen').onclick = prgRegenerate;
  prgEl('prg-cancel').onclick = prgCancel;
  prgEl('prg-stage').addEventListener('click', e => {
    const pill = e.target.closest('.prg-pill');
    if (pill) prgS.player.playAt(pill.dataset.sid);
  });
  // Keep editor shortcuts out of the PRGrok stage.
  prgEl('prg-stage').addEventListener('keydown', e => e.stopPropagation());
  // A visualization takes over the center pane too: yield to it.
  new MutationObserver(() => { if (document.body.classList.contains('viz-active')) closePRGrok(); })
    .observe(document.body, { attributes: true, attributeFilter: ['class'] });
}

