# PRGrok prompts

Everything the agent sees for PRGrok lives in this directory. Edit freely; no
Go changes are needed unless you add or rename a placeholder.

Each action is **one agent invocation** (one harness run through the Agent
bar's selected harness and model). There is no session, no follow-up turn: the
prompt carries all the input and the agent answers with a single JSON object.

| Action | Button | Files |
| :--- | :--- | :--- |
| `explain` | Explain Hunks | `explain.prompt.md`, `explain.input.schema.json`, `explain.output.schema.json`, `explain.example.json` |
| `visual` | Explain visually | `visual.prompt.md`, `visual.input.schema.json`, `visual.output.schema.json`, `visual.example.json` |
| `narrate` | Narrate | `narrate.prompt.md`, `narrate.input.schema.json`, `narrate.output.schema.json`, `narrate.example.json` |

Keep each `*.example.json` valid against its output schema: a test
(`TestPRGrokExamplesAreValid`) runs them through the same validation as real
agent output, so a broken example fails the build.

## How a prompt is built (`prgrok.go`: `prgRenderPrompt`)

1. Go builds the **input object** for the action (see `<action>.input.schema.json`).
2. `<action>.prompt.md` is loaded and two placeholders are replaced:

   | Placeholder | Replaced with |
   | :--- | :--- |
   | `{{input_json}}` | The input object, pretty-printed JSON |
   | `{{output_schema}}` | The contents of `<action>.output.schema.json` |
   | `{{output_example}}` | The contents of `<action>.example.json`: a one-shot example of a good answer |
   | `{{output_path}}` | Absolute path the agent must write its JSON to (see "Output file") |
   | `{{source}}` | `narrate` only: `explain` or `visual`, whichever result is being narrated |

3. The result is passed as the `{prompt}` argument of the harness.
4. The agent writes its JSON to `{{output_path}}`, a file in the project's
   working directory (`.px0-prgrok/`, where the harness is allowed to write), and
   replies `done`. Go reads that file, checks the required keys, drops unknown
   ids, stores the cleaned result in the cache (below), and deletes the file. If
   the agent could not write the file, Go falls back to the first JSON object in
   stdout.

## Output file and cache

All PRGrok data lives under `~/.px0/cache/data/prgrok/` (or
`$XDG_CACHE_HOME/px0/data/prgrok/`):

```
<owner_repo>/pr-<number>/<head8>/<action>[.<source>.<n>min].json
```

- The agent never writes here. Only Go does, after validation, so the cache
  key and layout can change without touching the prompts.
- `*.json`: the cleaned result; it is also the cache. The name does not depend on
  the view (entire PR or one file) or on the harness; the file records which
  harness and model wrote it. Opening a file you already
  explained, or re-opening the PR, reads it instead of calling the agent. The
  Regenerate button (or deleting the file) forces a new run.
- The key includes the PR head commit, so a force-push gets fresh results.

## CommitGrok shares these prompts

CommitGrok (commitgrok.go) runs the same three actions on one commit, with
`kind: "commit"`, `ref` (the full sha) and `author` in the input instead of
`number`. Its results live apart, keyed by the immutable sha:

```
~/.px0/cache/data/commitgrok/<repo>/<full sha>/<action>[.<source>.<n>min].json
```

A merge commit is explained against its first parent. A new subject (a code
file, say) only has to build a `grokSubject` in Go (kind, header fields, files
and hunks, cache path) and reuse `grokResult`/`grokRun`; the prompts need no change.

## Always the whole PR

Every action covers the **entire PR**, whatever the view (entire PR or one file
at a time) is showing: the view only decides what is displayed. `files` and
`diff` therefore always hold every file. Ids (`f0`, `f0h1`) are stable for a
given PR diff and are the only thing the UI uses to attach notes to hunks, so
the agent must echo them exactly.

## Live output

Each action runs as a thread, so you watch the agent work in the Agent bar's
Threads tab. The agent still writes its JSON to `{{output_path}}`; the thread
ends when it is done and Go then validates the file.

## Narrate gate

`narrate` needs an existing `explain` or `visual` result. Go turns that result
into `input.sections` (one per overall/file/hunk/element/diagram); the agent
only rewrites them as spoken talking points. The same section ids are returned.

## Overriding while developing

In dev mode (`start-dev.sh`) prompts are read from this directory on every run,
so edits apply to the next click. Elsewhere set `PX0_PRGROK_PROMPTS=/path/to/dir`.
Without either, the copies embedded in the binary are used.

## Limits

Word and diagram limits are passed in `input.limits` (defaults in
`prgrok.go`: `prgLimits`). Change them there or in the prompt text.
