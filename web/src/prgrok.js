// web/src/prgrok.js
// PRGrok: the sidebar footer's PR button opens a pull request browser in the
// left pane and an explained diff in the center. Standalone: it talks only to
// /api/prgrok/*. The explained-diff view (Explain Hunks, Explain visually,
// Narrate) is shared with CommitGrok: see grokview.js. Server side: prgrok.go;
// prompts: prgrok/prompts/.
import { api, esc } from './state.js';
import { layout, render } from './renderer.js';
import { closeVisualizer } from './viz.js';
import { closeOtherGroks, createGrokView, registerGrok } from './grokview.js';

const prgS = {
  open: false,
  info: null,
  list: { items: [], page: 1, hasMore: false, state: 'open' },
  number: 0,         // the pull request on screen
  listSeq: 0,
};

const prgEl = id => document.getElementById(id);

const prgView = createGrokView({
  prefix: 'prg',
  name: 'PRGrok',
  allLabel: 'Entire PR',
  bodyLabel: 'Description',
  endpoints: { result: '/api/prgrok/result', run: '/api/prgrok/run', job: '/api/prgrok/job' },
  params: subject => ({ number: subject.key }),
  onClose: () => closePRGrok(),
});

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
      <button class="prg-pr${prgS.number === p.number ? ' active' : ''}" data-n="${p.number}" title="${esc(p.title)}">
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
  prgS.number = number;
  const seq = prgView.begin(`PR #${number}`);
  prgDrawList();
  try {
    const r = await api('/api/prgrok/pr', { number });
    prgView.load(seq, {
      key: r.meta.number, body: r.meta.body, files: r.files, additions: r.additions, deletions: r.deletions,
      titleHtml: `<a href="${esc(r.meta.url)}" target="_blank" rel="noopener">#${r.meta.number}</a> ${esc(r.meta.title)}`,
    });
  } catch (e) {
    prgView.fail(seq, e.message);
  }
}

// ---------------------------------------------------------------- open / close

async function prgOpen() {
  if (prgS.open) return;
  closeVisualizer();
  closeOtherGroks('prg');
  prgS.open = true;
  prgView.setOpen(true);
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
    prgView.empty('PRGrok needs a project with a GitHub or Bitbucket remote.');
    return;
  }
  if (info.tool && !info.toolOk) {
    prgEl('prg-list').innerHTML = `<div class="prg-empty prg-error">The ${esc(info.tool)} CLI is not installed or not on PATH.</div>`;
    return;
  }
  if (!prgS.number) prgView.empty('Pick a pull request on the left.');
  prgLoadList(prgS.list.page || 1);
}

export function closePRGrok() {
  if (!prgS.open) return;
  prgS.open = false;
  prgView.setOpen(false);
  document.body.classList.remove('prgrok-active');
  prgEl('btn-prgrok')?.classList.remove('prg-on');
  prgEl('prgrok-panel').hidden = true;
  prgEl('prgrokview').hidden = true;
  layout();
  render();
}

export function initPRGrok() {
  prgView.mount(prgEl('prgrokview'));
  registerGrok('prg', closePRGrok);
  prgEl('btn-prgrok')?.addEventListener('click', () => (prgS.open ? closePRGrok() : prgOpen()));
  prgEl('prg-refresh').onclick = () => prgLoadList(prgS.list.page);
  prgEl('prg-state').onchange = () => prgLoadList(1);
  prgEl('prg-prev').onclick = () => prgLoadList(Math.max(1, prgS.list.page - 1));
  prgEl('prg-next').onclick = () => prgLoadList(prgS.list.page + 1);
  prgEl('prg-list').addEventListener('click', e => {
    const b = e.target.closest('.prg-pr');
    if (b) prgOpenPR(Number(b.dataset.n));
  });
  // A visualization takes over the center pane too: yield to it.
  new MutationObserver(() => { if (document.body.classList.contains('viz-active')) closePRGrok(); })
    .observe(document.body, { attributes: true, attributeFilter: ['class'] });
}
