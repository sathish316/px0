// web/src/codecity.js
// CodeCity renderer for the visualization host (viz.js). The server sends a
// laid-out city (districts and buildings with world coordinates, see
// internal/codecity); this draws it with three.js, which is fetched and cached
// on first use like Mermaid. Districts and buildings are each one InstancedMesh
// so thousands of files stay a couple of draw calls.

import { openFile } from './tabs.js';

const THREE_ASSET = 'static/vendor/three-0.170.0.module.min.js';
let threePromise = null;

function loadThree() {
  if (!threePromise) {
    const url = new URL(THREE_ASSET, document.baseURI).href;
    threePromise = import(/* @vite-ignore */ url).catch(e => { threePromise = null; throw e; });
  }
  return threePromise;
}

const DISHARMONY_COLORS = {
  'god-class': '#4d7fe8', 'brain-class': '#e6c84a', 'data-class': '#4caf7a', antenna: '#d0894a',
};
const HEALTHY = '#5c6570';

function disharmonyColor(node) {
  const tags = node.disharmonies || [];
  if (tags.includes('god-class') && tags.includes('brain-class')) return '#e05656';
  for (const t of ['god-class', 'brain-class', 'data-class', 'antenna']) if (tags.includes(t)) return DISHARMONY_COLORS[t];
  return HEALTHY;
}

const esc = s => String(s ?? '').replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));

function cityMount(stage, env) {
  const host = document.createElement('div');
  host.className = 'cc-host';
  host.innerHTML = `
    <canvas class="cc-canvas"></canvas>
    <div class="cc-labels"></div>
    <div class="cc-tools">
      <button type="button" class="cc-tool cc-hand" title="Hand tool: drag to move around (hold Shift or right-drag for the same)" aria-label="Hand tool" aria-pressed="false">
        <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M18 11V6a2 2 0 0 0-4 0v1M14 10V4a2 2 0 0 0-4 0v6M10 10.5V6a2 2 0 0 0-4 0v8"/><path d="M18 8a2 2 0 1 1 4 0v6a8 8 0 0 1-8 8h-2c-2.8 0-4.5-.9-5.9-2.4L3.4 16a2 2 0 0 1 3.2-2.4L8 15"/></svg>
      </button>
    </div>
    <div class="cc-search"><input type="search" placeholder="Find building or district…" spellcheck="false" aria-label="Find in city"></div>
    <aside class="cc-panel"></aside>
    <div class="cc-legend"></div>
    <div class="cc-zoom">
      <button type="button" class="cc-tool cc-zoom-in" title="Zoom in (+)" aria-label="Zoom in">+</button>
      <button type="button" class="cc-tool cc-zoom-out" title="Zoom out (-)" aria-label="Zoom out">−</button>
    </div>
    <div class="viz-empty cc-msg">Building city…</div>`;
  stage.appendChild(host);
  const canvas = host.querySelector('.cc-canvas');
  const labelsEl = host.querySelector('.cc-labels');
  const panel = host.querySelector('.cc-panel');
  const legend = host.querySelector('.cc-legend');
  const msg = host.querySelector('.cc-msg');
  const search = host.querySelector('input');
  const handBtn = host.querySelector('.cc-hand');
  const setHand = on => {
    handTool = on;
    handBtn.setAttribute('aria-pressed', String(on));
    host.classList.toggle('cc-hand-on', on);
  };
  handBtn.addEventListener('click', () => setHand(!handTool));
  host.querySelector('.cc-zoom-in').addEventListener('click', () => zoom(0.7));
  host.querySelector('.cc-zoom-out').addEventListener('click', () => zoom(1 / 0.7));

  let T = null;           // three namespace
  let destroyed = false;
  let city = null;
  let mode = 'metrics';
  let showLabels = true;
  let handTool = false;
  let selected = null;
  let raf = 0;
  const keys = new Set();

  // three objects, created once three has loaded
  let renderer, scene, camera, districtMesh, buildingMesh, outline;
  const nodes = { district: [], building: [] };
  const cam = { target: null, theta: 0.8, phi: 1.0, dist: 100 }; // orbit around target

  const themeColor = (v, fallback) => getComputedStyle(document.body).getPropertyValue(v).trim() || fallback;

  function colorFor(n) {
    if (mode === 'disharmony' && n.kind === 'building') return disharmonyColor(n);
    return n.color || '#8aa4c8';
  }

  function recolor() {
    const c = new T.Color();
    nodes.building.forEach((n, i) => buildingMesh.setColorAt(i, c.set(colorFor(n))));
    if (buildingMesh.instanceColor) buildingMesh.instanceColor.needsUpdate = true;
    setLegend();
  }

  function build() {
    const box = new T.BoxGeometry(1, 1, 1);
    const mat = new T.MeshStandardMaterial({ roughness: 0.6, metalness: 0.04 });
    const dMat = new T.MeshStandardMaterial({ roughness: 0.92, metalness: 0.02 });
    nodes.district = []; nodes.building = [];
    (function walk(n) {
      (n.kind === 'district' ? nodes.district : nodes.building).push(n);
      (n.children || []).forEach(walk);
    })(city.root);

    districtMesh = new T.InstancedMesh(box, dMat, nodes.district.length);
    buildingMesh = new T.InstancedMesh(box, mat, nodes.building.length);
    const m = new T.Matrix4(), c = new T.Color();
    const place = (mesh, list) => list.forEach((n, i) => {
      m.compose(
        new T.Vector3(n.x + n.width / 2, (n.y || 0) + n.height / 2, n.z + n.depth / 2),
        new T.Quaternion(), new T.Vector3(n.width, n.height, n.depth));
      mesh.setMatrixAt(i, m);
      mesh.setColorAt(i, c.set(n.color || '#888'));
    });
    place(districtMesh, nodes.district);
    place(buildingMesh, nodes.building);
    buildingMesh.castShadow = nodes.building.length < 1500;
    districtMesh.receiveShadow = buildingMesh.receiveShadow = true;
    scene.add(districtMesh, buildingMesh);

    // Landmark beacons: entry points get a small lit mast.
    const marks = nodes.building.filter(n => n.landmark);
    if (marks.length) {
      const mast = new T.InstancedMesh(new T.CylinderGeometry(0.12, 0.12, 2.2, 8), new T.MeshBasicMaterial({ color: 0xffd978 }), marks.length);
      marks.forEach((n, i) => {
        m.makeTranslation(n.x + n.width / 2, n.y + n.height + 1.2, n.z + n.depth / 2);
        mast.setMatrixAt(i, m);
      });
      scene.add(mast);
    }
    recolor();
  }

  function clearScene() {
    for (const o of [...scene.children]) {
      if (o.isLight || o === outline) continue;
      scene.remove(o);
      o.geometry?.dispose?.();
      o.material?.dispose?.();
      o.dispose?.();
    }
    labelsEl.replaceChildren();
    labelList = [];
  }

  let labelList = [];
  function buildLabels() {
    // Name the districts that matter: the biggest few hundred by building count.
    const ds = nodes.district.filter(d => d.metrics.buildings >= 2)
      .sort((a, b) => b.metrics.buildings - a.metrics.buildings).slice(0, 120);
    labelList = ds.map(n => {
      const el = document.createElement('div');
      el.className = 'cc-label';
      el.textContent = n.name;
      labelsEl.appendChild(el);
      return { el, n };
    });
  }

  function applyCamera() {
    const { target, theta, phi, dist } = cam;
    camera.position.set(
      target.x + dist * Math.sin(phi) * Math.cos(theta),
      Math.max(2, target.y + dist * Math.cos(phi)),
      target.z + dist * Math.sin(phi) * Math.sin(theta));
    camera.lookAt(target);
  }

  function overview() {
    const r = city.root;
    cam.target.set(r.x + r.width / 2, 0, r.z + r.depth / 2);
    const span = Math.max(r.width, r.depth, 20);
    cam.dist = span * 1.6; cam.theta = 0.8; cam.phi = 1.0;
    maxDist = span * 3;
    select(null);
  }
  let maxDist = 900;

  function street() {
    const r = city.root;
    cam.target.set(r.x + r.width / 2, 4, r.z + r.depth / 2);
    cam.dist = 28; cam.phi = 1.35;
  }

  function focus(n) {
    if (!n) return;
    select(n);
    cam.target.set(n.x + n.width / 2, (n.y || 0) + n.height / 2, n.z + n.depth / 2);
    cam.dist = Math.max(18, Math.max(n.width, n.depth, n.height) * 2.4);
    cam.phi = 1.0;
  }

  function select(n) {
    selected = n;
    if (outline) {
      outline.visible = !!n;
      if (n) {
        outline.position.set(n.x + n.width / 2, (n.y || 0) + n.height / 2, n.z + n.depth / 2);
        outline.scale.set(n.width * 1.02, n.height * 1.02, n.depth * 1.02);
      }
    }
    renderPanel();
  }

  function renderPanel() {
    const n = selected;
    if (!n) {
      const st = city.stats;
      const langs = Object.entries(st.languages || {}).slice(0, 5).map(([k, v]) => `${esc(k)} ${v}`).join(' · ');
      panel.innerHTML = `<h3>${esc(city.name)}</h3>
        <div class="cc-kv"><span>Buildings</span><b>${st.buildings}</b><span>Districts</span><b>${st.districts}</b><span>LOC</span><b>${st.loc}</b></div>
        <p class="cc-dim">${esc(langs)}</p>
        <p class="cc-dim">Click a building or district. Drag to orbit, hand tool (H) or right-drag to pan, pinch or scroll to zoom, +/- buttons to zoom, WASD to fly, Q/E for height, F to focus, Esc to clear.</p>`;
      return;
    }
    const m = n.metrics || {};
    const tags = (n.disharmonies || []).map(t => `<span class="cc-tag cc-${esc(t)}">${esc(t)}</span>`).join('');
    const file = n.kind === 'building' ? String(n.path).split('::')[0] : '';
    panel.innerHTML = `<h3>${esc(n.name)}</h3><div class="cc-path">${esc(n.path)}</div>
      <div class="cc-tags">${tags}${n.role ? `<span class="cc-tag">${esc(n.role)}</span>` : ''}</div>
      <div class="cc-kv">
        ${n.language ? `<span>Language</span><b>${esc(n.language)}</b>` : ''}
        <span>LOC</span><b>${m.loc ?? '—'}</b>
        <span>Methods</span><b>${m.nom ?? '—'}</b>
        <span>Fields</span><b>${m.noa ?? '—'}</b>
        ${m.complexity != null && n.kind === 'building' ? `<span>Complexity</span><b>${m.complexity}</b>` : ''}
        ${n.kind === 'district' ? `<span>Files</span><b>${m.files ?? '—'}</b>` : ''}
      </div>
      ${n.classNames?.length ? `<p class="cc-dim">${esc(n.classNames.join(', '))}</p>` : ''}
      ${file ? `<button class="cc-open" type="button">Open in editor</button>` : ''}`;
    panel.querySelector('.cc-open')?.addEventListener('click', () => {
      env.close();
      openFile(file);
    });
  }

  function setLegend() {
    legend.innerHTML = mode === 'disharmony'
      ? `<b>Disharmony map</b><br><i style="background:#4d7fe8"></i>God <i style="background:#e6c84a"></i>Brain <i style="background:#e05656"></i>Both <i style="background:#4caf7a"></i>Data <i style="background:#d0894a"></i>Antenna <i style="background:${HEALTHY}"></i>Healthy`
      : `<b>Polymetric city</b><br>Height = methods · Footprint = fields<br>Color = LOC, gray → blue`;
  }

  // ---- input ----
  function pick(ev) {
    const rect = canvas.getBoundingClientRect();
    const p = new T.Vector2(((ev.clientX - rect.left) / rect.width) * 2 - 1, -((ev.clientY - rect.top) / rect.height) * 2 + 1);
    const r = new T.Raycaster();
    r.setFromCamera(p, camera);
    const hit = r.intersectObjects([buildingMesh, districtMesh], false)[0];
    if (!hit) return null;
    return (hit.object === buildingMesh ? nodes.building : nodes.district)[hit.instanceId];
  }

  // Pointers: one finger/button drags (orbit, or pan with the hand tool, right
  // button or Shift); two fingers pinch to zoom and pan together.
  const pointers = new Map();
  let drag = null, pinch = null;
  const pinchState = () => {
    const [a, b] = [...pointers.values()];
    return { d: Math.hypot(a.x - b.x, a.y - b.y) || 1, x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
  };
  function panBy(dx, dy) {
    const k = cam.dist * 0.0016;
    const fwd = new T.Vector3(); camera.getWorldDirection(fwd); fwd.y = 0; fwd.normalize();
    const right = new T.Vector3().crossVectors(fwd, new T.Vector3(0, 1, 0));
    cam.target.addScaledVector(right, -dx * k).addScaledVector(fwd, dy * k);
  }
  canvas.addEventListener('pointerdown', e => {
    canvas.setPointerCapture(e.pointerId);
    pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
    stage.focus({ preventScroll: true });
    if (pointers.size === 2) { pinch = pinchState(); drag = null; return; }
    drag = { x: e.clientX, y: e.clientY, moved: 0, pan: handTool || e.button === 2 || e.shiftKey };
    host.classList.toggle('cc-grabbing', drag.pan);
  });
  canvas.addEventListener('pointermove', e => {
    const p = pointers.get(e.pointerId);
    if (p) { p.x = e.clientX; p.y = e.clientY; }
    if (pinch && pointers.size >= 2) {
      const n = pinchState();
      zoom(pinch.d / n.d);
      panBy(n.x - pinch.x, n.y - pinch.y);
      pinch = n;
      return;
    }
    if (!drag) return;
    const dx = e.clientX - drag.x, dy = e.clientY - drag.y;
    drag.x = e.clientX; drag.y = e.clientY; drag.moved += Math.abs(dx) + Math.abs(dy);
    if (drag.pan) panBy(dx, dy);
    else {
      cam.theta -= dx * 0.006;
      cam.phi = Math.min(1.5, Math.max(0.08, cam.phi - dy * 0.006));
    }
  });
  const endPointer = e => {
    pointers.delete(e.pointerId);
    if (pointers.size < 2) pinch = null;
    if (e.type === 'pointerup' && drag && drag.moved < 4 && e.button === 0) select(pick(e));
    drag = null;
    host.classList.remove('cc-grabbing');
  };
  canvas.addEventListener('pointerup', endPointer);
  canvas.addEventListener('pointercancel', endPointer);
  canvas.addEventListener('contextmenu', e => e.preventDefault());
  // A trackpad pinch arrives as ctrl+wheel with small deltas, so it needs a
  // much larger gain than a mouse wheel or two-finger scroll.
  canvas.addEventListener('wheel', e => {
    e.preventDefault();
    const dy = e.deltaMode === 1 ? e.deltaY * 16 : e.deltaY;
    zoom(Math.exp(dy * (e.ctrlKey ? 0.012 : 0.0028)));
  }, { passive: false });
  function zoom(f) { cam.dist = Math.min(maxDist, Math.max(4, cam.dist * f)); }

  stage.addEventListener('keydown', e => {
    if (e.target === search) {
      if (e.key === 'Enter') {
        const q = search.value.trim().toLowerCase();
        if (!q) return;
        const all = [...nodes.building, ...nodes.district];
        focus(all.find(n => n.name.toLowerCase() === q) || all.find(n => n.path.toLowerCase().includes(q)));
      } else if (e.key === 'Escape') search.blur();
      return;
    }
    const k = e.key.toLowerCase();
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    if (k === 'escape') select(null);
    else if (k === 'h') setHand(!handTool);
    else if (k === 'f' && selected) focus(selected);
    else if (k === '+' || k === '=') zoom(0.8);
    else if (k === '-') zoom(1.25);
    else keys.add(k);
  });
  stage.addEventListener('keyup', e => keys.delete(e.key.toLowerCase()));
  stage.addEventListener('blur', () => keys.clear());

  function step() {
    raf = requestAnimationFrame(step);
    if (!city) return;
    const speed = cam.dist * 0.012 * (keys.has('shift') ? 2.5 : 1);
    if (keys.size) {
      const fwd = new T.Vector3(Math.cos(cam.theta) * -1, 0, Math.sin(cam.theta) * -1).normalize();
      const right = new T.Vector3().crossVectors(fwd, new T.Vector3(0, 1, 0));
      if (keys.has('w')) cam.target.addScaledVector(fwd, speed);
      if (keys.has('s')) cam.target.addScaledVector(fwd, -speed);
      if (keys.has('d')) cam.target.addScaledVector(right, speed);
      if (keys.has('a')) cam.target.addScaledVector(right, -speed);
      if (keys.has('e')) cam.target.y += speed;
      if (keys.has('q')) cam.target.y = Math.max(0, cam.target.y - speed);
    }
    applyCamera();
    renderer.render(scene, camera);
    if (showLabels && labelList.length) updateLabels();
  }

  function updateLabels() {
    const w = stage.clientWidth, h = stage.clientHeight;
    const high = cam.dist > 25;
    for (const { el, n } of labelList) {
      const v = new T.Vector3(n.x + n.width / 2, (n.y || 0) + n.height + 0.4, n.z + n.depth / 2).project(camera);
      const on = high && v.z < 1 && Math.abs(v.x) < 1.1 && Math.abs(v.y) < 1.1;
      el.style.display = on ? 'block' : 'none';
      if (on) { el.style.left = `${(v.x * 0.5 + 0.5) * w}px`; el.style.top = `${(-v.y * 0.5 + 0.5) * h}px`; }
    }
  }

  function resize() {
    if (!renderer) return;
    const w = stage.clientWidth, h = stage.clientHeight;
    if (!w || !h) return;
    renderer.setSize(w, h, false);
    camera.aspect = w / h;
    camera.updateProjectionMatrix();
  }

  async function init() {
    try {
      T = await loadThree();
    } catch (e) {
      msg.textContent = 'Could not load the 3D engine (three.js is downloaded once and cached; check your network). ' + e.message;
      return false;
    }
    if (destroyed) return false;
    renderer = new T.WebGLRenderer({ canvas, antialias: true });
    renderer.setPixelRatio(Math.min(devicePixelRatio, 2));
    scene = new T.Scene();
    camera = new T.PerspectiveCamera(50, 1, 0.1, 6000);
    cam.target = new T.Vector3();
    scene.add(new T.HemisphereLight(0xb9d4f0, 0x1a1510, 1.1));
    const sun = new T.DirectionalLight(0xfff1d6, 1.1);
    sun.position.set(80, 140, 40);
    scene.add(sun, new T.AmbientLight(0x334155, 0.35));
    outline = new T.LineSegments(new T.EdgesGeometry(new T.BoxGeometry(1, 1, 1)), new T.LineBasicMaterial({ color: 0xffd978 }));
    outline.visible = false;
    scene.add(outline);
    applyTheme();
    resize();
    raf = requestAnimationFrame(step);
    return true;
  }

  function applyTheme() {
    if (!scene) return;
    const bgc = themeColor('--bg', '#0d141c');
    scene.background = new T.Color(bgc);
    scene.fog = new T.Fog(new T.Color(bgc), 120, 900);
  }

  const ready = init();

  return {
    async render(result) {
      if (!(await ready) || destroyed) return;
      city = result.data;
      msg.hidden = true;
      clearScene();
      if (!city.stats.buildings) {
        msg.hidden = false;
        msg.textContent = 'No source files recognized in this branch.';
        return;
      }
      build();
      buildLabels();
      overview();
      resize();
    },
    fail(text) { msg.hidden = false; msg.textContent = 'Could not build the city: ' + text; },
    resize,
    menu() {
      const disabled = !city;
      return [
        { header: 'Color by' },
        { label: 'Size metrics (LOC)', checked: mode === 'metrics', disabled, run: () => { mode = 'metrics'; recolor(); } },
        { label: 'Disharmony map', desc: 'God, brain, data classes and antennas (heuristic)', checked: mode === 'disharmony', disabled, run: () => { mode = 'disharmony'; recolor(); } },
        { sep: true },
        { header: 'Camera' },
        { label: 'Overview', disabled, run: overview },
        { label: 'Street level', disabled, run: street },
        { label: 'District labels', checked: showLabels, disabled, run: () => { showLabels = !showLabels; labelsEl.style.display = showLabels ? '' : 'none'; } },
      ];
    },
    destroy() {
      destroyed = true;
      cancelAnimationFrame(raf);
      if (scene) clearScene();
      renderer?.dispose();
      renderer?.forceContextLoss?.();
      host.remove();
    },
  };
}

export function registerCodeCity(register) {
  register({ id: 'codecity', mount: cityMount });
}
