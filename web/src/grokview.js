// web/src/grokview.js
// The explained-diff view shared by PRGrok, CommitGrok and (later) CodeGrok: a
// toolbar (view scope, layout, Diff / Explain Hunks / Explain visually / Narrate,
// harness picker, voice), the diff with the agent's notes attached to files, hunks
// and elements, Mermaid diagrams, and narration.
//
// A Grok module owns what differs (its left-hand list, how it loads a subject, which
// endpoints serve it) and hands the rest to createGrokView():
//
//   const view = createGrokView({ prefix: 'cg', ... });
//   view.mount(sectionEl);                 // once
//   const seq = view.begin('Loading...');  // a subject was picked
//   view.load(seq, { key, titleHtml, body, info, files, additions, deletions });
//
// The server side is the shared engine in prgrok.go (grokRun / grokResult).
import { api, apiPostJson, esc } from './state.js';
import { applyAgentMeta, registerAgentPicker, syncAgentSelection } from './agent.js';
import { openThread } from './thread.js';
import { renderMermaidBlocks } from './mermaid.js';
import { PRG_VOICES, prgIsVoice, prgTtsLoad, prgTtsReady, prgTtsTry, prgCreatePlayer } from './prgrok-tts.js';

// The Grok modules take over the same left and center panes, so opening one closes the others.
const groks = new Map();
export const registerGrok = (name, close) => groks.set(name, close);
export function closeOtherGroks(except) {
  for (const [name, close] of groks) if (name !== except) close();
}

const VOICE_KEY = 'px0.prgrok.voice';
const LAYOUT_KEY = 'px0.prgrok.layout';

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


/** The toolbar markup. Ids are `<prefix>-<name>`; classes are the shared prg-* ones. */
function grokBarHTML(p, cfg) {
  return `
  <div class="prg-bar">
    <div class="prg-row">
      <span id="${p}-title" class="prg-title">${esc(cfg.name)}</span>
      <span id="${p}-status" class="prg-status"></span>
      <button id="${p}-thread" class="prg-btn" hidden title="Watch the agent work in the Agent bar">Show thread</button>
      <button id="${p}-cancel" class="prg-btn" hidden>Cancel</button>
      <button id="${p}-close" class="prg-btn" title="Back to the editor">Back to editor</button>
    </div>
    <div class="prg-row" id="${p}-controls" hidden>
      <label class="prg-field">View
        <select id="${p}-scope" class="prg-select" title="Show every file, or one file at a time">
          <option value="all">${esc(cfg.allLabel)}</option>
          <option value="file">One file at a time</option>
        </select>
      </label>
      <select id="${p}-file" class="prg-select prg-file-select" title="File" hidden></select>
      <select id="${p}-layout" class="prg-select" title="Diff layout">
        <option value="split">Side by side</option>
        <option value="unified">Unified</option>
      </select>
      <span class="prg-sep"></span>
      <button id="${p}-diff" class="prg-btn prg-mode" data-mode="diff" title="Plain diff">Diff</button>
      <button id="${p}-explain" class="prg-btn prg-mode" data-mode="explain" title="Explain every hunk: what changed and why">Explain Hunks</button>
      <button id="${p}-visual" class="prg-btn prg-mode" data-mode="visual" title="Explain with diagrams">Explain visually</button>
      <button id="${p}-narrate" class="prg-btn" title="Turn the explanation into spoken talking points">Narrate</button>
      <button id="${p}-regen" class="prg-btn" title="Run the agent again for this view" disabled>&#8635; Regenerate</button>
      <span id="${p}-agent" class="prg-agent" title="Coding harness and model used for Explain and Narrate (the same selection as the Agent bar)">
        <select id="${p}-harness" class="agent-harness-select" title="Coding harness"></select>
        <select id="${p}-model" class="agent-model-select" title="Model"></select>
      </span>
    </div>
    <div class="prg-row prg-voice" id="${p}-voice" hidden>
      <label class="prg-field">Minutes <input id="${p}-minutes" class="prg-num" type="number" min="1" max="30" value="3"></label>
      <select id="${p}-locale" class="prg-select" title="Accent"></select>
      <select id="${p}-gender" class="prg-select" title="Voice gender"></select>
      <select id="${p}-voice-id" class="prg-select" title="Kokoro voice"></select>
      <label class="prg-field">Speed <input id="${p}-rate" class="prg-num" type="number" min="0.5" max="2" step="0.1" value="1"></label>
      <button id="${p}-dl" class="prg-btn" title="Download the Kokoro voice model (about 80 MB, cached by the browser)">Download</button>
      <button id="${p}-try" class="prg-btn" title="Play a sample" disabled>Try</button>
      <span class="prg-sep"></span>
      <button id="${p}-prevs" class="prg-btn" title="Previous talking point" disabled>&#9198;</button>
      <button id="${p}-play" class="prg-btn" title="Play" disabled>&#9654;</button>
      <button id="${p}-pause" class="prg-btn" title="Pause" disabled>&#10074;&#10074;</button>
      <button id="${p}-nexts" class="prg-btn" title="Next talking point" disabled>&#9197;</button>
      <button id="${p}-stops" class="prg-btn" title="Stop" disabled>&#9209;</button>
      <span id="${p}-voice-status" class="prg-status"></span>
    </div>
  </div>
  <div id="${p}-stage" class="prg-stage" tabindex="-1"></div>`;
}

/**
 * @param {object} cfg
 * @param {string} cfg.prefix     element id prefix, e.g. 'prg'
 * @param {string} cfg.name       'PRGrok': titles and logs
 * @param {string} cfg.allLabel   the "all files" view, e.g. 'Entire PR'
 * @param {string} cfg.bodyLabel  heading of the collapsible message, e.g. 'Description'
 * @param {{result:string, run:string, job:string}} cfg.endpoints
 * @param {(subject:object)=>object} cfg.params  what identifies the subject in a request, e.g. { number }
 * @param {()=>void} cfg.onClose  the toolbar's "Back to the editor"
 */
export function createGrokView(cfg) {
  const $ = id => document.getElementById(`${cfg.prefix}-${id}`);
  let root = null;

  const S = {
    open: false,
    subject: null,     // { key, titleHtml, body, info, files, additions, deletions }
    mode: 'diff',      // diff | explain | visual
    scope: 'all',      // view only: all (every file) | file (one at a time). Never changes what the agent explains.
    layout: 'split',   // split | unified
    file: '',          // selected file id for scope=file
    results: {},       // cache of server results by prgKey
    narr: null,        // active narration { source, sections }
    run: null,         // { thread, action } while the agent works
    seq: 0,            // bumped when the subject changes, so stale replies are dropped
    active: null,      // section id being spoken
    voiceShown: false,
    voice: { voice: '', rate: 1, minutes: 3, locale: 'en-US', gender: 'female' },
    player: null,
  };

  // Results are for the whole subject, so the key never depends on the view.
  const prgKey = (action, source) => (action === 'narrate' ? `narrate|${source}` : action);
  const prgFiles = () => S.subject?.files || [];
  const prgShownFiles = () => S.scope === 'file' ? prgFiles().filter(f => f.id === S.file) : prgFiles();
  const prgResult = action => S.results[prgKey(action)] || null;

  const prgSubjectParams = () => cfg.params(S.subject);

  function prgSetStatus(msg, isError) {
    const el = $('status');
    el.textContent = msg || '';
    el.classList.toggle('prg-error', !!isError);
  }

  function prgSetBusy(busy) {
    for (const id of ['diff', 'explain', 'visual', 'narrate', 'regen']) {
      const b = $(id);
      if (b) b.disabled = busy || (id === 'regen' && (S.mode === 'diff' || !S.subject));
    }
    $('cancel').hidden = !busy;
    $('thread').hidden = !busy;
    // The shared picker disables itself while its element is busy; no change of harness mid-run.
    $('agent').classList.toggle('busy', busy);
    applyAgentMeta();
  }

  // ---------------------------------------------------------------- opening a subject

  function prgFillFileSelect() {
    const sel = $('file');
    sel.innerHTML = prgFiles().map(f => `<option value="${f.id}">${esc(f.path)} (+${f.additions} -${f.deletions})</option>`).join('');
    sel.value = S.file;
  }

  /** Load results already saved on disk so reopening a PR, or changing the view, costs no agent run. */
  async function prgPeekAll() {
    const seq = S.seq;
    for (const action of ['explain', 'visual']) {
      if (S.results[prgKey(action)]) continue;
      try {
        const r = await api(cfg.endpoints.result, { ...prgSubjectParams(), action });
        if (seq !== S.seq) return;
        if (r.cached) S.results[prgKey(action)] = r.result;
      } catch { /* a missing result is not an error */ }
    }
    if (seq === S.seq) { prgNarrationForView(); prgDraw(); }
  }

  // ---------------------------------------------------------------- running the agent

  async function prgRunAction(action, { refresh = false, source = '' } = {}) {
    if (S.run || !S.subject) return null;
    const seq = S.seq;
    const key = prgKey(action, source);
    const minutes = Math.max(1, Math.min(30, Number($('minutes').value) || 3));
    S.run = { thread: '', action };
    prgSetBusy(true);
    prgSetStatus('Starting...');
    try {
      // Run on the harness and model the dropdowns show, like every composer.
      if (!await syncAgentSelection($('harness').value, $('model').value)) throw new Error('could not select that harness');
      const r = await apiPostJson(cfg.endpoints.run, { ...prgSubjectParams(), action, source, minutes, refresh });
      let result = r.cached ? r.result : null;
      if (!result) {
        S.run.thread = r.thread;
        prgShowThread();
        prgSetStatus('Working in the Agent bar. The explanation appears here when it finishes.');
        result = await prgPoll(r.thread, seq);
      }
      if (seq !== S.seq || !result) return null;
      S.results[key] = result;
      prgSetStatus(result.harness ? `Done (${result.harness}${result.model ? ' / ' + result.model : ''}). Saved for next time.` : '');
      return result;
    } catch (e) {
      if (seq === S.seq) prgShowError(e.message, e.body?.raw);
      return null;
    } finally {
      S.run = null;
      if (seq === S.seq) prgSetBusy(false);
    }
  }

  function prgShowThread() {
    if (S.run?.thread) openThread(S.run.thread);
  }

  function prgPoll(thread, seq) {
    return new Promise((resolve, reject) => {
      const tick = async () => {
        if (seq !== S.seq || !S.run || S.run.thread !== thread) return resolve(null);
        try {
          const j = await api(cfg.endpoints.job, { id: thread });
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
    if (raw) console.warn(`${cfg.name} agent output:`, raw);
  }

  async function prgCancel() {
    const id = S.run?.thread;
    if (id) { try { await apiPostJson('/api/threads/cancel', { id }); } catch { /* already finished */ } }
    S.run = null;
    prgSetBusy(false);
    prgSetStatus('Cancelled.');
  }

  // ---------------------------------------------------------------- actions

  async function prgSetMode(mode) {
    if (!S.subject || S.run) return;
    S.mode = mode;
    prgSyncControls();
    if (mode !== 'diff' && !prgResult(mode)) {
      try {
        const r = await api(cfg.endpoints.result, { ...prgSubjectParams(), action: mode });
        if (r.cached) S.results[prgKey(mode)] = r.result;
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
    if (!S.subject || S.run) return;
    prgShowVoice();
    if (S.mode === 'diff' || !prgResult(S.mode)) {
      prgSetStatus('Run Explain Hunks or Explain visually first: there is nothing to narrate yet.', true);
      return;
    }
    const source = S.mode;
    let res = S.results[prgKey('narrate', source)];
    if (!res) {
      res = await prgRunAction('narrate', { source });
      if (!res) return;
    }
    prgApplyNarration(res, source);
    prgDraw();
  }

  function prgApplyNarration(res, source) {
    S.narr = { source, sections: res.sections || [] };
    S.player.setSections(prgViewSections());
    prgSyncTransport();
  }

  /** Narration is for the whole PR; the player speaks only what the view shows. */
  function prgViewSections() {
    const all = S.narr?.sections || [];
    if (S.scope !== 'file' || !S.file) return all;
    const mine = new RegExp(`^${S.file}(h|$)`);
    return all.filter(x => mine.test(x.id));
  }

  async function prgRegenerate() {
    if (!S.subject || S.run || S.mode === 'diff') return;
    const res = await prgRunAction(S.mode, { refresh: true });
    if (res) {
      // A regenerated explanation invalidates narration built from the old one.
      S.narr = null;
      delete S.results[prgKey('narrate', 'explain')];
      delete S.results[prgKey('narrate', 'visual')];
      S.player.setSections([]);
      prgSyncTransport();
    }
    prgDraw();
  }

  function prgViewChanged() {
    S.scope = $('scope').value;
    S.file = $('file').value || S.file;
    $('file').hidden = S.scope !== 'file';
    prgNarrationForView();
    prgDraw();
  }

  /** Follow the view: the saved narration for the shown mode, limited to what is on screen. */
  function prgNarrationForView() {
    prgStopAll();
    const src = S.mode === 'diff' ? '' : S.mode;
    const res = src ? S.results[prgKey('narrate', src)] : null;
    if (res) prgApplyNarration(res, src);
    else { S.narr = null; S.player.setSections([]); prgSyncTransport(); }
  }

  function prgSyncControls() {
    for (const b of root.querySelectorAll('.prg-mode')) b.classList.toggle('active', b.dataset.mode === S.mode);
    $('scope').value = S.scope;
    $('file').hidden = S.scope !== 'file';
    prgSetBusy(!!S.run);
  }

  // ---------------------------------------------------------------- rendering

  async function prgDraw() {
    if (!S.open) return;
    const stage = $('stage');
    const keepTop = stage.scrollTop;
    stage.replaceChildren();
    if (!S.subject) return;
    const frag = document.createDocumentFragment();
    const res = S.mode === 'diff' ? null : prgResult(S.mode);

    const body = S.subject.body?.trim();
    if (S.mode === 'diff' && body) {
      const d = document.createElement('details');
      d.className = 'prg-desc';
      d.innerHTML = `<summary>${esc(cfg.bodyLabel)}</summary><div class="prg-md">${prgMd(body)}</div>`;
      frag.append(d);
    }
    if (S.subject.info) frag.append(Object.assign(document.createElement('div'), { className: 'prg-info', textContent: S.subject.info }));
    frag.append(prgStats());
    if (S.mode !== 'diff' && !res && !S.run) {
      frag.append(prgNotice('No result yet. Use Regenerate to run the agent.'));
    }
    if (res && S.scope === 'all') {
      // The overall note and diagrams are about the whole PR, so they belong to the whole-subject view.
      if (res.overall) frag.append(prgNote(res.overall, 'overall', 'Overall'));
      if (S.mode === 'visual' && res.diagrams?.length) frag.append(prgDiagrams(res.diagrams));
    }
    const files = prgShownFiles();
    if (!files.length) frag.append(prgNotice('No changes in this diff.'));
    for (const f of files) frag.append(prgFileBlock(f, res));
    stage.append(frag);
    stage.scrollTop = keepTop;
    await renderMermaidBlocks(stage, () => S.open);
  }

  /** Files changed, lines added and removed: always for the whole PR, whichever view is showing. */
  function prgStats() {
    const n = prgFiles().length;
    const el = document.createElement('div');
    el.className = 'prg-stats';
    el.innerHTML = `<span><strong>${n}</strong> file${n === 1 ? '' : 's'} changed</span>` +
      `<span class="prg-plus">+${S.subject.additions ?? 0} added</span>` +
      `<span class="prg-minus">&minus;${S.subject.deletions ?? 0} removed</span>`;
    return el;
  }

  const prgNotice = text => Object.assign(document.createElement('div'), { className: 'prg-empty', textContent: text });

  function prgNote(text, id, label, cls = '') {
    const box = document.createElement('div');
    box.className = `prg-note ${cls}`;
    box.dataset.sid = id;
    if (S.active === id) box.classList.add('speaking');
    const head = document.createElement('div');
    head.className = 'prg-note-head';
    head.innerHTML = `<span class="prg-note-label">${esc(label || '')}</span>`;
    if (S.narr?.sections.some(s => s.id === id)) {
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
      if (S.active === d.id) card.classList.add('speaking');
      const title = document.createElement('div');
      title.className = 'prg-diagram-title';
      title.textContent = `${d.title || d.type} (${d.type})`;
      if (S.narr?.sections.some(s => s.id === d.id)) {
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
    table.className = `prg-lines prg-${S.layout}`;
    const pending = [...elements].sort((a, b) => a.line - b.line);
    // An element note sits just above the first row at or after the line it anchors to.
    const flushNotes = n => {
      while (pending.length && n && n >= pending[0].line) {
        const e = pending.shift();
        table.append(prgNote(e.summary, e.id, `${e.kind || 'element'}${e.name ? ' ' + e.name : ''}`, 'prg-note-element'));
      }
    };
    if (S.layout === 'unified') {
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
    try { Object.assign(S.voice, JSON.parse(localStorage.getItem(VOICE_KEY) || '{}')); } catch { /* defaults */ }
  }
  const prgSaveVoiceSettings = () => { try { localStorage.setItem(VOICE_KEY, JSON.stringify(S.voice)); } catch { /* private mode */ } };

  function prgFillVoiceSelects() {
    const v = S.voice;
    const locales = [...new Set(PRG_VOICES.map(x => x.locale))];
    $('locale').innerHTML = locales.map(l => `<option>${l}</option>`).join('');
    $('gender').innerHTML = '<option value="female">Female</option><option value="male">Male</option>';
    $('locale').value = v.locale;
    $('gender').value = v.gender;
    prgFillVoiceList();
    $('rate').value = v.rate;
    $('minutes').value = v.minutes;
  }

  function prgFillVoiceList() {
    const v = S.voice;
    const opts = PRG_VOICES.filter(x => x.locale === v.locale && x.gender === v.gender);
    const sel = $('voice-id');
    sel.innerHTML = '<option value="">Choose a voice...</option>' + opts.map(x => `<option value="${x.id}">${x.name}</option>`).join('');
    sel.value = opts.some(x => x.id === v.voice) ? v.voice : '';
    if (!sel.value) v.voice = '';
    prgSyncVoiceButtons();
  }

  function prgSyncVoiceButtons() {
    const has = prgIsVoice(S.voice.voice);
    $('dl').disabled = !has || prgTtsReady();
    $('dl').textContent = prgTtsReady() ? 'Downloaded' : 'Download';
    $('try').disabled = !has;
  }

  function prgShowVoice() {
    S.voiceShown = true;
    $('voice').hidden = false;
  }

  function prgSyncTransport() {
    const p = S.player;
    const has = p.hasSections();
    $('play').disabled = !has || p.isPlaying();
    $('pause').disabled = !has || !p.isPlaying();
    $('prevs').disabled = !has || !p.canPrev();
    $('nexts').disabled = !has || !p.canNext();
    $('stops').disabled = !has;
    if (has) prgShowVoice();
  }

  function prgActivate(section) {
    S.active = section?.id || null;
    for (const el of $('stage').querySelectorAll('.speaking')) el.classList.remove('speaking');
    if (!section) return;
    const el = [...$('stage').querySelectorAll('[data-sid]')].find(n => n.dataset.sid === section.id && !n.classList.contains('prg-pill'));
    if (el) {
      el.classList.add('speaking');
      el.scrollIntoView({ block: 'center', behavior: 'smooth' });
    }
  }

  function prgStopAll() { S.player?.stop(); }

  function prgInitVoiceUI() {
    prgLoadVoiceSettings();
    prgFillVoiceSelects();
    S.player = prgCreatePlayer({
      getVoice: () => S.voice,
      setStatus: m => { $('voice-status').textContent = m || ''; },
      onActivate: prgActivate,
      onState: prgSyncTransport,
    });
    const status = m => { $('voice-status').textContent = m || ''; };
    $('locale').onchange = e => { S.voice.locale = e.target.value; prgFillVoiceList(); prgSaveVoiceSettings(); };
    $('gender').onchange = e => { S.voice.gender = e.target.value; prgFillVoiceList(); prgSaveVoiceSettings(); };
    $('voice-id').onchange = e => { S.voice.voice = e.target.value; S.player.voiceChanged(); prgSyncVoiceButtons(); prgSaveVoiceSettings(); };
    $('rate').onchange = e => { S.voice.rate = Math.min(2, Math.max(0.5, Number(e.target.value) || 1)); S.player.voiceChanged(); prgSaveVoiceSettings(); };
    $('minutes').onchange = e => { S.voice.minutes = Math.min(30, Math.max(1, Number(e.target.value) || 3)); prgSaveVoiceSettings(); };
    $('dl').onclick = async () => {
      $('dl').disabled = true;
      try { await prgTtsLoad(status); } catch (e) { status(`Could not load the voice model: ${e.message}`); }
      prgSyncVoiceButtons();
    };
    $('try').onclick = async () => {
      try { await prgTtsTry(S.voice.voice, S.voice.rate, status); } catch (e) { status(e.message); }
      prgSyncVoiceButtons();
    };
    $('play').onclick = () => S.player.play();
    $('pause').onclick = () => S.player.pause();
    $('prevs').onclick = () => S.player.prev();
    $('nexts').onclick = () => S.player.next();
    $('stops').onclick = () => S.player.stop();
    prgSyncTransport();
  }


  // ---------------------------------------------------------------- public API

  /** Build the toolbar and stage inside `section` and wire them. Call once. */
  function mount(section) {
    root = section;
    root.innerHTML = grokBarHTML(cfg.prefix, cfg);
    try { S.layout = localStorage.getItem(LAYOUT_KEY) === 'unified' ? 'unified' : 'split'; } catch { /* default */ }
    prgInitVoiceUI();
    $('close').onclick = cfg.onClose;
    $('scope').onchange = prgViewChanged;
    $('file').onchange = prgViewChanged;
    $('layout').value = S.layout;
    $('layout').onchange = e => { S.layout = e.target.value; try { localStorage.setItem(LAYOUT_KEY, S.layout); } catch { /* private mode */ } prgDraw(); };
    $('thread').onclick = prgShowThread;
    registerAgentPicker({ el: $('agent'), harnessSelect: $('harness'), modelSelect: $('model') });
    for (const b of root.querySelectorAll('.prg-mode')) b.onclick = () => prgSetMode(b.dataset.mode);
    $('narrate').onclick = prgNarrate;
    $('regen').onclick = prgRegenerate;
    $('cancel').onclick = prgCancel;
    $('stage').addEventListener('click', e => {
      const pill = e.target.closest('.prg-pill');
      if (pill) S.player.playAt(pill.dataset.sid);
    });
    // Keep editor shortcuts out of the stage.
    $('stage').addEventListener('keydown', e => e.stopPropagation());
  }

  /** A subject was picked and is loading: reset the view. Returns the token to pass to load/fail. */
  function begin(title) {
    prgStopAll();
    const seq = ++S.seq;
    S.subject = null; S.results = {}; S.narr = null; S.mode = 'diff'; S.file = '';
    $('controls').hidden = true;
    $('title').textContent = title;
    $('stage').innerHTML = '<div class="prg-empty">Loading diff...</div>';
    prgSetStatus('');
    return seq;
  }

  async function load(seq, subject) {
    if (seq !== S.seq) return;
    S.subject = subject;
    S.file = subject.files[0]?.id || '';
    $('title').innerHTML = subject.titleHtml;
    $('controls').hidden = false;
    prgFillFileSelect();
    prgSyncControls();
    await prgDraw();
    prgPeekAll();
  }

  function fail(seq, message) {
    if (seq !== S.seq) return;
    $('stage').innerHTML = `<div class="prg-empty prg-error">${esc(message)}</div>`;
  }

  /** Show a message in the stage with nothing loaded (no subject picked yet, or none possible). */
  function empty(message, isError) {
    S.seq++;
    S.subject = null;
    $('controls').hidden = true;
    $('stage').innerHTML = `<div class="prg-empty${isError ? ' prg-error' : ''}">${esc(message)}</div>`;
  }

  function setOpen(open) {
    S.open = open;
    if (!open) prgStopAll();
  }

  return { mount, begin, load, fail, empty, setOpen, subjectKey: () => S.subject?.key ?? null, isRunning: () => !!S.run };
}
