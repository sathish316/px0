# PRGrok

PRGrok lists your project's pull requests and explains one of them, hunk by hunk, with diagrams, and as a spoken walkthrough. Click the PR button in the sidebar footer (right of the skyscraper) to open it; click it again, or choose **Back to editor**, to return to your tabs.

It works on GitHub and Bitbucket projects. The host comes from the project's git remote.

| Host | Needs | Used for |
| :--- | :--- | :--- |
| GitHub | the [`gh`](https://cli.github.com) CLI, logged in | listing PRs and fetching a PR's diff |
| Bitbucket | the `twg` CLI, logged in | listing PRs and fetching a PR's diff |

PRGrok is independent of the PR review mode (`px0 <PR URL>`): nothing is checked out. It reads PRs through the CLIs and never edits your files.

## Browsing

The left pane shows 10 pull requests per page with **Prev** and **Next**, and a state filter (Open, Closed, All on GitHub; Open, Merged, Declined on Bitbucket). Click one to load its diff in the center pane.

## Reading a PR

The control bar has:

- **View**: **Entire PR** or **One file at a time** (with a file dropdown), and a **Side by side** or **Unified** layout. Side by side is the default and uses the full width.
- **Diff**: the plain diff.
- **Explain Hunks**: a note for the PR, each file and each hunk saying what changed and why, with extra notes for the elements of a busy hunk, placed above the lines they describe.
- **Explain visually**: the same overview plus up to three Mermaid diagrams (ER, sequence, architecture before/after, state, flowchart) with an explanation of each.
- **Narrate**: turns the current explanation into spoken talking points. It needs an Explain Hunks or Explain visually result first, and says so if there is none.
- **Regenerate**: runs the agent again for the current view.

Each of these is **one run of a coding harness**, for the **whole PR** whichever view is showing (the view only filters what you see). It runs as a thread, so you watch the agent work live in the Agent bar. Pick the harness and model with the dropdowns in the control bar (the same selection as the Agent bar). **Cancel** stops a run.

## Narration and voices

Narrate shows a voice row: minutes, accent, gender, voice, speed, **Download**, **Try**, and play, pause, previous, next and stop. Speech is generated in your browser with [Kokoro](https://huggingface.co/onnx-community/Kokoro-82M-v1.0-ONNX). The first **Download** (or first Play) fetches the 82M-parameter model (about 80 MB) and the browser caches it. Each narrated note gets a **speak** pill that plays just that note, and the note being spoken is highlighted.

## Saved results

Results are saved so that leaving a file and coming back, or reopening the PR, is instant and costs no agent run. They live in `~/.px0/cache/data/prgrok/` (or `$XDG_CACHE_HOME/px0/data/prgrok/`), one JSON file per PR and action. A new commit on the PR starts fresh results; **Regenerate** replaces a saved one.

## Prompts

The prompts, and the JSON each action takes in and must return, are plain files you can edit: [`prgrok/prompts/`](../../prgrok/prompts/README.md). In dev mode (`start-dev.sh`) they are re-read on every run.
