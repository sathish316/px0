# CommitGrok

CommitGrok is [PRGrok](prgrok.md) for your local git history. It lists the commits of a branch, with the merge graph, and explains one of them hunk by hunk, with diagrams, and as a spoken walkthrough. Click the commit button in the sidebar footer (right of the PR button) to open it; click it again, or choose **Back to editor**, to return to your tabs.

It needs only a git repository: commits are read with the local `git` client, so there is no provider, no remote and no extra CLI. It never edits your files.

## Browsing

The left pane has a **branch** picker at the top (local branches, then remote-tracking ones; it starts on the branch you have checked out) and shows **10 commits per page** with **Prev** and **Next**. Each row has the commit subject, its branch and tag labels, the short sha, author and age. A merge commit is a hollow dot labelled *merge*.

The lines on the left are the history graph: each branch keeps its column and colour, a merge draws a line from the merge commit to the branch it brought in, and the lines carry over from one page to the next. Click a commit to load its diff in the center pane. A merge commit is shown against its first parent, that is, what the merge brought into the branch.

## Reading a commit

The center pane is the same view PRGrok uses (see [Reading a PR](prgrok.md#reading-a-pr)): **View** is **Entire commit** (the default) or **One file at a time**; **Diff**, **Explain Hunks**, **Explain visually**, **Narrate** and **Regenerate** work the same, run on the harness picked in the control bar, and always cover the whole commit.

## Saved results

Results are saved in `~/.px0/cache/data/commitgrok/<repo>/<full commit sha>/` (or under `$XDG_CACHE_HOME/px0/data/commitgrok/`), one JSON file per action (and per narration source and length). A commit never changes, so the full sha is the whole key: there is nothing to invalidate, and the same commit read from another branch, or another clone of the repository, finds the same results. **Regenerate** replaces a saved one.

## How it is built

CommitGrok adds only what is specific to commits. Everything else is PRGrok's shared engine, so the next Grok (a code file) can reuse it the same way:

- **Server**: [`commitgrok.go`](../../commitgrok.go) reads history and diffs and turns a commit into a `grokSubject`; `grokRun` and `grokResult` in [`prgrok.go`](../../prgrok.go) do the rest (prompt, agent thread, validation, cache). `/api/prgrok/job` settles any Grok job.
- **Web**: [`commitgrok.js`](../../web/src/commitgrok.js) is the list and the graph; [`grokview.js`](../../web/src/grokview.js) is the toolbar, the diff with its notes, diagrams and narration.
- **Prompts**: the same [`prgrok/prompts/`](../../prgrok/prompts/README.md), with `kind: "commit"`.
