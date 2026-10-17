#!/usr/bin/env node
/**
 * scripts/dev.js: hot-reload dev runner (see start-dev.sh, start-ide-dev.sh).
 *
 *   node scripts/dev.js [--electron] [px0 args...]
 *
 * - web/src/** changes rebuild web/app.js; the page reloads itself because
 *   px0 serves web/ from disk when PX0_DEV_WEB is set.
 * - *.go changes rebuild ./px0 and restart the server (or the whole Electron app).
 * - electron/*.js changes restart Electron.
 * Zero dependencies.
 */
import fs from 'node:fs';
import path from 'node:path';
import { spawn, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
let args = process.argv.slice(2);
const electron = args[0] === '--electron';
if (electron) args = args.slice(1);

const bin = path.join(root, process.platform === 'win32' ? 'px0.exe' : 'px0');
const tag = (m) => console.log(`\x1b[36m[dev]\x1b[0m ${m}`);

function run(cmd, cmdArgs) {
  return spawnSync(cmd, cmdArgs, { cwd: root, stdio: 'inherit' }).status === 0;
}
const buildWeb = () => run('node', ['scripts/build-web.js']);
const buildGo = () => run('go', ['build', '-o', bin, '.']);

function electronBin() {
  const p = path.join(root, 'electron', 'node_modules', '.bin', process.platform === 'win32' ? 'electron.cmd' : 'electron');
  if (!fs.existsSync(p)) {
    console.error('Electron is not installed. Run: (cd electron && npm install)');
    process.exit(1);
  }
  return p;
}

let child = null;
let first = true;

function start() {
  const env = { ...process.env, PX0_DEV_WEB: root, PX0_BIN: bin };
  if (electron) {
    child = spawn(electronBin(), [path.join(root, 'electron'), '--no-update', '--no-telemetry', ...args], { cwd: root, env, stdio: 'inherit' });
  } else {
    // Only the first start opens a browser; later restarts reuse the open tab.
    const open = first ? [] : ['--no-open'];
    child = spawn(bin, [...open, '--no-update', '--no-telemetry', ...args], { cwd: root, env, stdio: 'inherit' });
  }
  first = false;
  const me = child;
  child.on('exit', (code) => {
    if (child === me) {
      child = null;
      tag(electron ? 'Electron closed, stopping.' : `px0 exited (${code}); waiting for changes...`);
      if (electron) process.exit(0);
    }
  });
}

function stop() {
  return new Promise((resolve) => {
    if (!child) return resolve();
    const c = child;
    child = null;
    c.once('exit', resolve);
    c.kill('SIGTERM');
    setTimeout(() => c.kill('SIGKILL'), 3000).unref();
  });
}

// Coalesce bursts of events; run tasks one at a time.
function debounced(fn, ms = 150) {
  let t = null, running = false, again = false;
  const go = async () => {
    if (running) { again = true; return; }
    running = true;
    try { await fn(); } finally { running = false; if (again) { again = false; go(); } }
  };
  return () => { clearTimeout(t); t = setTimeout(go, ms); };
}

const onWeb = debounced(() => {
  tag('web source changed, rebuilding web/app.js');
  buildWeb();
});

const restart = debounced(async () => {
  await stop();
  start();
});

const onGo = debounced(async () => {
  tag('Go source changed, rebuilding');
  if (!buildGo()) { tag('build failed; keeping the old binary running'); return; }
  await stop();
  start();
});

function watchTree(dir, filter, cb, recursive = true) {
  // fs.watch recursive works on macOS/Windows and on Linux with Node >= 20.
  try {
    fs.watch(dir, { recursive }, (_ev, name) => { if (name && filter(name)) cb(name); }).on('error', () => {});
  } catch (e) {
    console.error(`cannot watch ${dir}: ${e.message}`);
  }
}

tag('building web assets and px0');
if (!buildWeb() || !buildGo()) process.exit(1);

watchTree(path.join(root, 'web', 'src'), (n) => n.endsWith('.js'), onWeb);
watchTree(root, (n) => /^[^/\\]+\.go$/.test(n) && !n.endsWith('_test.go'), onGo, false);
if (electron) watchTree(path.join(root, 'electron'), (n) => /^[^/\\]+\.js$/.test(n), () => { tag('electron source changed, restarting'); restart(); }, false);

for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, async () => { await stop(); process.exit(0); });

tag(electron ? 'starting Electron (hot reload on)' : 'starting px0 (hot reload on)');
start();
