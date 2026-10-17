// web/src/commitgrok.js
// CommitGrok: the sidebar footer's commit button opens the local git history in
// the left pane (a branch picker, ten commits a page, the merge graph) and, for
// the commit you click, its explained diff in the center. The explained-diff view
// (Explain Hunks, Explain visually, Narrate) is the one PRGrok uses: grokview.js.
// Server side: commitgrok.go, over the same engine as prgrok.go.
import { api, esc } from './state.js';
import { layout, render } from './renderer.js';
import { closeVisualizer } from './viz.js';
import { closeOtherGroks, createGrokView, registerGrok } from './grokview.js';

const cgS = {
  open: false,
  info: null,
  branch: '',
  list: { items: [], page: 1, hasMore: false, cols: 1 },
  sha: '',           // the commit on screen
  listSeq: 0,
};

const cgEl = id => document.getElementById(id);

const cgView = createGrokView({
  prefix: 'cg',
  name: 'CommitGrok',
  allLabel: 'Entire commit',
  bodyLabel: 'Message',
  endpoints: { result: '/api/commitgrok/result', run: '/api/commitgrok/run', job: '/api/commitgrok/job' },
  params: subject => ({ ref: subject.key }),
  onClose: () => closeCommitGrok(),
});

// ---------------------------------------------------------------- the merge graph

const CG_COLORS = ['#4c9be8', '#e8a23c', '#4caf7a', '#c778dd', '#e05656', '#2bb7b3', '#d4b13a', '#8d8fe8'];
const CG_ROW_H = 44;
const CG_COL_W = 14;
const CG_PAD = 9;

const cgX = col => CG_PAD + col * CG_COL_W;
const cgColor = i => CG_COLORS[i % CG_COLORS.length];

/** One row of the graph: the lines through it and the commit's dot. Rows are a fixed height so they join. */
function cgGraphSVG(c, cols) {
  const w = CG_PAD * 2 + Math.max(0, cols - 1) * CG_COL_W;
  const mid = CG_ROW_H / 2;
  const parts = [];
  const link = (x1, y1, x2, y2, color) => {
    const d = x1 === x2 ? `M${x1} ${y1}V${y2}` : `M${x1} ${y1}C${x1} ${(y1 + y2) / 2} ${x2} ${(y1 + y2) / 2} ${x2} ${y2}`;
    parts.push(`<path d="${d}" stroke="${cgColor(color)}" fill="none" stroke-width="2" stroke-linecap="round"/>`);
  };
  for (const l of c.top) {
    if (l.toNode) link(cgX(l.col), 0, cgX(c.node), mid, l.color);
    else link(cgX(l.col), 0, cgX(l.col), CG_ROW_H, l.color);
  }
  for (const l of c.bottom) link(cgX(c.node), mid, cgX(l.col), CG_ROW_H, l.color);
  const fill = c.merge ? 'var(--bg)' : cgColor(c.color);
  parts.push(`<circle cx="${cgX(c.node)}" cy="${mid}" r="${c.merge ? 4.5 : 4}" fill="${fill}" stroke="${cgColor(c.color)}" stroke-width="2"/>`);
  return `<svg class="cg-graph" width="${w}" height="${CG_ROW_H}" viewBox="0 0 ${w} ${CG_ROW_H}" aria-hidden="true">${parts.join('')}</svg>`;
}

function cgAgo(iso) {
  const t = Date.parse(iso);
  if (!t) return '';
  const s = Math.max(0, (Date.now() - t) / 1000);
  for (const [n, unit] of [[31536000, 'y'], [2592000, 'mo'], [604800, 'w'], [86400, 'd'], [3600, 'h'], [60, 'm']]) {
    if (s >= n) return `${Math.floor(s / n)}${unit} ago`;
  }
  return 'just now';
}

// ---------------------------------------------------------------- list (left pane)

function cgFillBranches() {
  const { branches = [], current } = cgS.info;
  const opt = b => `<option value="${esc(b.name)}">${esc(b.name)}</option>`;
  const local = branches.filter(b => !b.remote);
  const remote = branches.filter(b => b.remote);
  const detached = !local.some(b => b.name === current);
  cgEl('cg-branch').innerHTML =
    (detached ? `<option value="${esc(current)}">${esc(current)} (detached)</option>` : '') +
    (local.length ? `<optgroup label="Local">${local.map(opt).join('')}</optgroup>` : '') +
    (remote.length ? `<optgroup label="Remote">${remote.map(opt).join('')}</optgroup>` : '');
  cgS.branch = cgS.branch && [...cgEl('cg-branch').options].some(o => o.value === cgS.branch) ? cgS.branch : current;
  cgEl('cg-branch').value = cgS.branch;
}

async function cgLoadList(page = 1) {
  const seq = ++cgS.listSeq;
  const list = cgEl('cg-list');
  list.innerHTML = '<div class="prg-empty">Loading commits...</div>';
  try {
    const r = await api('/api/commitgrok/log', { branch: cgS.branch, page });
    if (seq !== cgS.listSeq) return;
    cgS.list = r;
    cgDrawList();
  } catch (e) {
    if (seq !== cgS.listSeq) return;
    list.innerHTML = `<div class="prg-empty prg-error">${esc(e.message)}</div>`;
    cgEl('cg-prev').disabled = cgEl('cg-next').disabled = true;
    cgEl('cg-page').textContent = '';
  }
}

function cgRefBadges(refs = []) {
  return refs.map(r => `<span class="cg-ref cg-ref-${r.kind}">${esc(r.name)}</span>`).join('');
}

function cgDrawList() {
  const { items, page, hasMore, cols } = cgS.list;
  const list = cgEl('cg-list');
  if (!items.length) list.innerHTML = '<div class="prg-empty">No commits.</div>';
  else {
    list.innerHTML = items.map(c => `
      <button class="cg-commit${cgS.sha === c.sha ? ' active' : ''}" data-sha="${c.sha}" style="height:${CG_ROW_H}px"
        title="${esc(c.subject)}&#10;${esc(c.sha)}">
        ${cgGraphSVG(c, cols)}
        <span class="cg-body">
          <span class="cg-subject">${cgRefBadges(c.refs)}${esc(c.subject)}</span>
          <span class="cg-sub"><span class="cg-sha">${esc(c.short)}</span>${c.merge ? ' <span class="cg-merge">merge</span>' : ''} &middot; ${esc(c.author)} &middot; ${esc(cgAgo(c.date))}</span>
        </span>
      </button>`).join('');
  }
  cgEl('cg-prev').disabled = page <= 1;
  cgEl('cg-next').disabled = !hasMore;
  cgEl('cg-page').textContent = `Page ${page}`;
}

// ---------------------------------------------------------------- opening a commit

async function cgOpenCommit(sha) {
  cgS.sha = sha;
  const seq = cgView.begin(`Commit ${sha.slice(0, 7)}`);
  cgDrawList();
  try {
    const r = await api('/api/commitgrok/commit', { sha });
    const m = r.meta;
    const date = m.date ? new Date(m.date).toLocaleString() : '';
    let info = `${m.author}${date ? ' · ' + date : ''} · ${m.short}`;
    if (m.parents.length > 1) info += ` · merge commit, shown against its first parent ${m.parents[0].slice(0, 7)}`;
    cgView.load(seq, {
      key: m.sha, body: m.body, info, files: r.files, additions: r.additions, deletions: r.deletions,
      titleHtml: `<code>${esc(m.short)}</code> ${esc(m.title)}`,
    });
  } catch (e) {
    cgView.fail(seq, e.message);
  }
}

// ---------------------------------------------------------------- open / close

async function cgOpen() {
  if (cgS.open) return;
  closeVisualizer();
  closeOtherGroks('cg');
  cgS.open = true;
  cgView.setOpen(true);
  document.body.classList.add('commitgrok-active');
  cgEl('btn-commitgrok')?.classList.add('prg-on');
  cgEl('commitgrok-panel').hidden = false;
  cgEl('commitgrokview').hidden = false;
  try {
    cgS.info = await api('/api/commitgrok/info');
  } catch (e) {
    cgEl('cg-list').innerHTML = `<div class="prg-empty prg-error">${esc(e.message)}</div>`;
    cgView.empty('CommitGrok needs a project inside a git repository.', true);
    return;
  }
  cgEl('cg-repo').textContent = cgS.info.repo || 'CommitGrok';
  cgFillBranches();
  if (!cgS.sha) cgView.empty('Pick a commit on the left.');
  cgLoadList(cgS.list.page || 1);
}

export function closeCommitGrok() {
  if (!cgS.open) return;
  cgS.open = false;
  cgView.setOpen(false);
  document.body.classList.remove('commitgrok-active');
  cgEl('btn-commitgrok')?.classList.remove('prg-on');
  cgEl('commitgrok-panel').hidden = true;
  cgEl('commitgrokview').hidden = true;
  layout();
  render();
}

export function initCommitGrok() {
  cgView.mount(cgEl('commitgrokview'));
  registerGrok('cg', closeCommitGrok);
  cgEl('btn-commitgrok')?.addEventListener('click', () => (cgS.open ? closeCommitGrok() : cgOpen()));
  cgEl('cg-refresh').onclick = async () => {
    try { cgS.info = await api('/api/commitgrok/info'); cgFillBranches(); } catch { /* keep the old list */ }
    cgLoadList(cgS.list.page);
  };
  cgEl('cg-branch').onchange = e => { cgS.branch = e.target.value; cgLoadList(1); };
  cgEl('cg-prev').onclick = () => cgLoadList(Math.max(1, cgS.list.page - 1));
  cgEl('cg-next').onclick = () => cgLoadList(cgS.list.page + 1);
  cgEl('cg-list').addEventListener('click', e => {
    const b = e.target.closest('.cg-commit');
    if (b) cgOpenCommit(b.dataset.sha);
  });
  new MutationObserver(() => { if (document.body.classList.contains('viz-active')) closeCommitGrok(); })
    .observe(document.body, { attributes: true, attributeFilter: ['class'] });
}
