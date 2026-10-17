# PRGrok

Go half: `prgrok.go`. Browser half: `web/src/prgrok.js` (UI) and `web/src/prgrok-tts.js` (Kokoro). Prompts and schemas: `prgrok/prompts/`, embedded with `go:embed`. It does not touch the PR review session in `pr.go`.

## Reading PRs

`prgDetect` parses the git remote (`gitRemoteURL`, `remoteHostPath`) into provider, owner and repo; a host containing `bitbucket` is Bitbucket, one containing `github` is GitHub. All CLI calls go through `prgExec` (a variable, so tests stub it): 90 s timeout, `GIT_TERMINAL_PROMPT=0`, output capped at 32 MB.

- GitHub: `gh api --include -X GET repos/o/r/pulls -f state= -f per_page=10 -f page=N`. `hasMore` is the `rel="next"` Link header. Details and the diff are `gh api repos/o/r/pulls/N`, with `Accept: application/vnd.github.v3.diff` for the diff.
- Bitbucket: `twg -o json --output-summary none bb pull-requests query|get|diff -w <workspace> -r <repo>`, with `--limit 10 --page N`. `prgBitbucketList` accepts a bare array or an object holding the rows under `values`, `data`, `items` or `pullRequests`, and reports `ok:false` errors.

`prgParseDiff` turns the git-format diff into files and hunks with ids `f0`, `f0h0`, and `prgAnnotate` renders the text the prompts describe (`FILE`/`HUNK` headings, 6-column new-file line gutter, capped at 100,000 characters).

## Running an action

`POST /api/prgrok/run {number, action, source, minutes, refresh}` (local POST only):

1. Fetch the PR (its head SHA is part of the cache key) and look for a saved result; if there is one and `refresh` is not set, return it.
2. Build the action's **input object** (`prgInputFor`, or the narration sections from the saved explain/visual result, which is also the narrate gate: 409 if none), and render `<action>.prompt.md` with `{{input_json}}`, `{{output_schema}}`, `{{output_example}}` (a one-shot example from `<action>.example.json`), `{{source}}` and `{{output_path}}`.
3. Make a unique file `<project>/.px0-prgrok/<action>-pr<N>-<ns>.json` as the agent's output path and start **one** run as a **thread** (`threadManager.Create`), so the agent's output streams live in the Agent bar and it uses the harness and model selected there (the PRGrok bar has the same pickers).
4. A goroutine (`prgWatch`) waits for the thread's turn to end and settles it, so the result is saved even if the page is closed. `GET /api/prgrok/job?id=<thread id>` reports whether it is running and the settled result.

Settling (`prgFinish`) reads the agent's file (falling back to the first JSON object in stdout if the harness could not write one), validates it, **stores the result in the cache, and deletes the agent's file** (and `.px0-prgrok/` when empty). Validation (`prgNormalize`, `prgNormalizeNarrate`) drops file, hunk and element ids that are not in the PR, element notes without a line, unknown diagram types, and Mermaid code fences; narration sections the agent dropped fall back to their written text.

The agent writes in the project, not in the cache, because that is where its harness is allowed to write. Only Go writes the cache, so the key and layout can change without touching the prompts.

## Saved results

`prgPath` names `~/.px0/cache/data/prgrok/<owner_repo>/pr-<N>/<head8>/<action>[.<source>.<n>min].json` (`vendorCacheDir`'s parent plus `data/prgrok`). The file holds the normalized `prgResult` plus `cachedAt`. `GET /api/prgrok/result` peeks without running anything; the UI uses it to load saved results when a PR, scope or file is selected. Every action is for the whole PR, so the name depends on neither the view (entire PR or one file) nor the harness; the one-file view just filters what is shown.

## Browser

`prgrok.js` follows the `#vizview` pattern: `#prgrokview` in the center and `#prgrok-panel` in the sidebar, with `body.prgrok-active` hiding the editor and the explorer. It closes the visualizer when it opens and closes itself when a visualizer opens. Notes are keyed by the same ids as the narration sections (`overall`, `f0`, `f0h0`, `f0h0e0`, `d0`), which is how speak pills and the highlight find their notes. Diagrams use `renderMermaidBlocks` from `mermaid.js`.

`prgrok-tts.js` loads `kokoro-js` from the jsDelivr CDN on first use, picks WebGPU (fp32) or WASM (q8), synthesizes one WAV per section and prefetches the next. Voice, speed and minutes persist in `localStorage` (`px0.prgrok.voice`).

## Tests

`prgrok_test.go` covers diff parsing, annotation, JSON extraction, normalization, prompt rendering (including every schema being valid JSON, and every one-shot example passing the same validation as real agent output), provider detection, GitHub and Bitbucket listing with a stubbed `prgExec`, and settling (output file, cache, cleanup, stdout fallback).
