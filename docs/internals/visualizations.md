# Visualizations

A visualizer has a Go half that produces a model and a browser half that draws it. Adding one touches `viz.go` and one `web/src/` module.

## Server (`viz.go`)

- `visualizers` is the registry: `id`, `name`, `description`, and `build(s, branch, head) (any, error)`. `GET /api/viz/list` serves it (minus `build`).
- `GET /api/viz?id=<id>[&refresh=1]` returns `{viz, branch, head, stale, data}` for the **checked-out branch** (`gitCurrentBranch`, `gitHeadCommit`). `data` is the provider's model.
- `loadViz` holds one mutex, so concurrent opens build once. Without `refresh` it serves the cached file for `(workspace, id, branch)`; otherwise it builds and rewrites it. `stale` is set when the cached `head` differs from the current HEAD. Uncommitted edits are not detected, which is why Refresh exists.
- The cache lives in `<cache dir>/viz/<id>-<hash of root>-<branch>.json`, a sibling of the vendor cache. Nothing is written into the workspace (AGENT.md tenet 3).
- Providers must be deterministic and must not call a harness.

### CodeCity (`internal/codecity`)

`Build(root, files, opts)` takes the index's file list. `metrics.go` extracts LOC, methods, fields, classes and complexity with regexes per language (RE2; no lookaround). `layout.go` packs children of each district with a binary-tree rectangle packer, largest first, then converts to absolute coordinates; heights use five percentile bins. Build time is dominated by reading files (capped at 8000 files of at most 1 MB each).

## Browser

- `web/src/viz.js` is the host: picker menu (`#viz-menu`), center pane (`#vizview`, with `body.viz-active` hiding `#tabs`, `#view-switches` and `#editor`), top bar and Controls menu. `registerVisualizer({id, mount})`; `mount(stage, env)` returns `{render(result), menu?(), resize?(), fail?(msg), destroy()}`. The host owns load/refresh/errors; the visualizer owns its DOM and must release everything in `destroy()`.
- On window focus the host re-requests the cached model to notice a branch switch or new commits; it re-renders only if the branch or head changed.
- `web/src/codecity.js` draws with three.js, loaded by dynamic `import()` from `static/vendor/three-0.170.0.module.min.js`, which the server fetches from the CDN once and caches (`vendorCDNs` in `server.go`), exactly like Mermaid. Districts and buildings are one `InstancedMesh` each; picking uses the instance id. The orbit camera is hand-written because three's `OrbitControls` needs an import map.
