package main

// PRGrok: browse a project's pull requests (GitHub via gh, Bitbucket via twg),
// and explain, visualise and narrate one with a coding harness.
//
// It is deliberately standalone: it does not use the PR checkout session in
// pr.go. PRs are read through the provider CLIs, parsed here, and each AI
// action is a single harness invocation (agentManager.StartPromptSized) whose
// stdout is one JSON object. Prompts and schemas live in prgrok/prompts/.

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed prgrok/prompts
var prgPromptFS embed.FS

const (
	prgPageSize     = 10
	prgCmdTimeout   = 90 * time.Second
	prgMaxOutput    = 32 << 20
	prgDiffPromptCh = 100000 // annotated diff characters sent to the agent
	prgWordsPerMin  = 130
)

// prgLimits are the word and diagram budgets passed to the agent as input.limits.
var prgLimits = map[string]int{
	"overall": 180, "file": 80, "hunk": 80, "element": 40,
	"design": 80, "min_diagrams": 1, "max_diagrams": 3,
}

// ---------------------------------------------------------------- provider

type prgRepo struct {
	Provider string `json:"provider"` // "github", "bitbucket" or ""
	Host     string `json:"host"`
	Owner    string `json:"owner"` // GitHub owner or Bitbucket workspace
	Repo     string `json:"repo"`
	Reason   string `json:"reason,omitempty"`
}

func (r prgRepo) slug() string { return r.Owner + "/" + r.Repo }

// prgRemoteURL is the remote PRs are read from: the branch's upstream or
// origin when there is one, else "upstream" (where a fork's PRs live), else the
// first remote, so a clone that only has a remote named "fork" still works.
func prgRemoteURL(root string) string {
	if u := gitRemoteURL(root, gitCurrentBranch(root)); u != "" {
		return u
	}
	out, err := exec.Command("git", "-C", root, "remote").Output()
	if err != nil {
		return ""
	}
	names := strings.Fields(string(out))
	pick := ""
	for _, n := range names {
		if n == "upstream" {
			pick = n
		}
	}
	if pick == "" && len(names) > 0 {
		pick = names[0]
	}
	if pick == "" {
		return ""
	}
	u, err := exec.Command("git", "-C", root, "config", "--get", "remote."+pick+".url").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(u))
}

// prgDetect reads the git remote of the project and decides which provider
// serves its PRs.
func prgDetect(root string) prgRepo {
	raw := prgRemoteURL(root)
	if raw == "" {
		return prgRepo{Reason: "no git remote found for this project"}
	}
	host, p, ok := remoteHostPath(raw)
	if !ok {
		return prgRepo{Reason: "could not parse git remote " + raw}
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 2 {
		return prgRepo{Host: host, Reason: "git remote has no owner/repo path"}
	}
	r := prgRepo{Host: host, Owner: parts[len(parts)-2], Repo: parts[len(parts)-1]}
	switch {
	case strings.Contains(host, "bitbucket"):
		r.Provider = "bitbucket"
	case strings.Contains(host, "github"):
		r.Provider = "github"
	default:
		r.Reason = "remote host " + host + " is neither GitHub nor Bitbucket"
	}
	return r
}

// prgExec runs a provider CLI; a variable so tests can stub it.
var prgExec = func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, prgCmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	var out, errb bytes.Buffer
	cmd.Stdout = &limitWriter{w: &out, n: prgMaxOutput}
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		if errors.Is(err, exec.ErrNotFound) {
			msg = name + " is not installed or not on PATH"
		}
		return out.Bytes(), errors.New(msg)
	}
	return out.Bytes(), nil
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if len(p) > l.n {
		l.w.Write(p[:l.n])
		l.n = 0
		return len(p), nil
	}
	l.n -= len(p)
	return l.w.Write(p)
}

type prgPR struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Author  string `json:"author"`
	State   string `json:"state"`
	Draft   bool   `json:"draft,omitempty"`
	Source  string `json:"source"`
	Dest    string `json:"dest"`
	URL     string `json:"url"`
	Updated string `json:"updated"`
}

type prgPRList struct {
	Items   []prgPR `json:"items"`
	Page    int     `json:"page"`
	HasMore bool    `json:"hasMore"`
	State   string  `json:"state"`
}

type prgPRMeta struct {
	prgPR
	Body string `json:"body"`
	Head string `json:"head"` // head commit, part of the cache key
}

func prgStates(provider string) []string {
	if provider == "bitbucket" {
		return []string{"open", "merged", "declined"}
	}
	return []string{"open", "closed", "all"}
}

func prgListPRs(ctx context.Context, root string, r prgRepo, page int, state string) (*prgPRList, error) {
	if page < 1 {
		page = 1
	}
	out := &prgPRList{Page: page, State: state}
	switch r.Provider {
	case "github":
		path := fmt.Sprintf("repos/%s/%s/pulls", r.Owner, r.Repo)
		raw, err := prgExec(ctx, root, "gh", "api", "--include", "-X", "GET", path,
			"-f", "state="+state, "-f", "per_page="+strconv.Itoa(prgPageSize), "-f", "page="+strconv.Itoa(page))
		if err != nil {
			return nil, err
		}
		head, body := prgSplitHTTP(raw)
		out.HasMore = strings.Contains(head, `rel="next"`)
		var items []map[string]any
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, fmt.Errorf("unexpected gh output: %v", err)
		}
		for _, it := range items {
			out.Items = append(out.Items, prgGitHubPR(it))
		}
	case "bitbucket":
		bs := map[string]string{"open": "OPEN", "merged": "MERGED", "declined": "DECLINED"}[state]
		if bs == "" {
			bs = "OPEN"
		}
		raw, err := prgExec(ctx, root, "twg", "-o", "json", "--output-summary", "none", "bb", "pull-requests", "query",
			"-w", r.Owner, "-r", r.Repo, "--state", bs, "--limit", strconv.Itoa(prgPageSize), "--page", strconv.Itoa(page))
		if err != nil {
			return nil, err
		}
		items, more, err := prgBitbucketList(raw)
		if err != nil {
			return nil, err
		}
		out.HasMore = more
		for _, it := range items {
			out.Items = append(out.Items, prgBitbucketPR(it))
		}
		if len(out.Items) > prgPageSize {
			out.Items = out.Items[:prgPageSize]
		}
	default:
		return nil, errors.New(prgNoProvider(r))
	}
	if out.Items == nil {
		out.Items = []prgPR{}
	}
	return out, nil
}

func prgNoProvider(r prgRepo) string {
	if r.Reason != "" {
		return r.Reason
	}
	return "unsupported git host"
}

// prgSplitHTTP splits `gh api --include` output into headers and body.
func prgSplitHTTP(raw []byte) (string, []byte) {
	s := string(raw)
	for _, sep := range []string{"\r\n\r\n", "\n\n"} {
		if i := strings.Index(s, sep); i >= 0 {
			return s[:i], []byte(s[i+len(sep):])
		}
	}
	return "", raw
}

// dig walks nested maps by key; "" when anything is missing or not a string.
func dig(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	switch v := cur.(type) {
	case string:
		return v
	case float64:
		return strconv.Itoa(int(v))
	}
	return ""
}

func prgGitHubPR(it map[string]any) prgPR {
	n, _ := it["number"].(float64)
	state := dig(it, "state")
	if dig(it, "merged_at") != "" {
		state = "merged"
	}
	draft, _ := it["draft"].(bool)
	return prgPR{
		Number: int(n), Title: dig(it, "title"), Author: dig(it, "user", "login"),
		State: state, Draft: draft, Source: dig(it, "head", "ref"), Dest: dig(it, "base", "ref"),
		URL: dig(it, "html_url"), Updated: dig(it, "updated_at"),
	}
}

func prgBitbucketPR(it map[string]any) prgPR {
	id, _ := strconv.Atoi(dig(it, "id"))
	author := dig(it, "author", "display_name")
	if author == "" {
		author = dig(it, "author", "nickname")
	}
	return prgPR{
		Number: id, Title: dig(it, "title"), Author: author, State: strings.ToLower(dig(it, "state")),
		Source: dig(it, "source", "branch", "name"), Dest: dig(it, "destination", "branch", "name"),
		URL: dig(it, "links", "html", "href"), Updated: dig(it, "updated_on"),
	}
}

// prgBitbucketList accepts the shapes twg may emit: a bare array, or an object
// holding the rows under values, data, items or pullRequests.
func prgBitbucketList(raw []byte) ([]map[string]any, bool, error) {
	raw = bytes.TrimSpace(raw)
	var arr []map[string]any
	if json.Unmarshal(raw, &arr) == nil {
		return arr, len(arr) >= prgPageSize, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false, fmt.Errorf("unexpected twg output: %v", err)
	}
	if ok, _ := obj["ok"].(bool); !ok && obj["error"] != nil {
		return nil, false, fmt.Errorf("twg: %s", prgAnyMsg(obj["error"]))
	}
	if d, ok := obj["data"].(map[string]any); ok {
		for k, v := range d {
			if _, set := obj[k]; !set {
				obj[k] = v
			}
		}
	}
	var rows []map[string]any
	for _, k := range []string{"values", "items", "pullRequests", "pullrequests", "data"} {
		if a, ok := obj[k].([]any); ok {
			for _, e := range a {
				if m, ok := e.(map[string]any); ok {
					rows = append(rows, m)
				}
			}
			break
		}
	}
	more := false
	for _, k := range []string{"hasNextPage", "hasMore"} {
		if b, ok := obj[k].(bool); ok && b {
			more = true
		}
	}
	if s, ok := obj["next"].(string); ok && s != "" {
		more = true
	}
	return rows, more, nil
}

func prgAnyMsg(v any) string {
	if m, ok := v.(map[string]any); ok {
		if s := dig(m, "message"); s != "" {
			return s
		}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// prgFetchPR returns the PR's metadata and raw unified diff.
func prgFetchPR(ctx context.Context, root string, r prgRepo, number int) (*prgPRMeta, string, error) {
	n := strconv.Itoa(number)
	switch r.Provider {
	case "github":
		path := fmt.Sprintf("repos/%s/%s/pulls/%s", r.Owner, r.Repo, n)
		raw, err := prgExec(ctx, root, "gh", "api", path)
		if err != nil {
			return nil, "", err
		}
		var it map[string]any
		if err := json.Unmarshal(raw, &it); err != nil {
			return nil, "", fmt.Errorf("unexpected gh output: %v", err)
		}
		meta := &prgPRMeta{prgPR: prgGitHubPR(it), Body: dig(it, "body"), Head: dig(it, "head", "sha")}
		diff, err := prgExec(ctx, root, "gh", "api", path, "-H", "Accept: application/vnd.github.v3.diff")
		if err != nil {
			return nil, "", err
		}
		return meta, string(diff), nil
	case "bitbucket":
		raw, err := prgExec(ctx, root, "twg", "-o", "json", "--output-summary", "none", "bb", "pull-requests", "get", n, "-w", r.Owner, "-r", r.Repo)
		if err != nil {
			return nil, "", err
		}
		var it map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(raw), &it); err != nil {
			return nil, "", fmt.Errorf("unexpected twg output: %v", err)
		}
		if d, ok := it["data"].(map[string]any); ok {
			it = d
		} else if vs, ok := it["values"].([]any); ok && len(vs) > 0 {
			if m, ok := vs[0].(map[string]any); ok {
				it = m
			}
		}
		meta := &prgPRMeta{prgPR: prgBitbucketPR(it), Body: dig(it, "description"), Head: dig(it, "source", "commit", "hash")}
		if meta.Number == 0 {
			meta.Number = number
		}
		diff, err := prgExec(ctx, root, "twg", "bb", "pull-requests", "diff", n, "-w", r.Owner, "-r", r.Repo)
		if err != nil {
			return nil, "", err
		}
		return meta, string(diff), nil
	}
	return nil, "", errors.New(prgNoProvider(r))
}

// ---------------------------------------------------------------- diff model

type prgLine struct {
	T string `json:"t"`           // "c" context, "a" added, "d" deleted, "m" meta
	O int    `json:"o,omitempty"` // old line number
	N int    `json:"n,omitempty"` // new line number
	X string `json:"x"`
}

type prgHunk struct {
	ID     string    `json:"id"`
	Header string    `json:"header"`
	Lines  []prgLine `json:"lines"`
}

type prgFile struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	OldPath   string    `json:"oldPath,omitempty"`
	Status    string    `json:"status"` // added, modified, deleted, renamed
	Binary    bool      `json:"binary,omitempty"`
	Additions int       `json:"additions"`
	Deletions int       `json:"deletions"`
	Hunks     []prgHunk `json:"hunks"`
}

var prgHunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

func prgStripPrefix(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}

// prgParseDiff turns a git-format unified diff into files and hunks with
// stable ids (f0, f0h0, ...).
func prgParseDiff(text string) []prgFile {
	var files []prgFile
	var cur *prgFile
	var hunk *prgHunk
	var oldNo, newNo int
	flush := func() {
		if hunk != nil && cur != nil {
			cur.Hunks = append(cur.Hunks, *hunk)
		}
		hunk = nil
	}
	finish := func() {
		flush()
		if cur != nil {
			files = append(files, *cur)
		}
		cur = nil
	}
	for _, ln := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(ln, "diff --git ") {
			finish()
			cur = &prgFile{ID: fmt.Sprintf("f%d", len(files)), Status: "modified"}
			rest := strings.TrimPrefix(ln, "diff --git ")
			if i := strings.Index(rest, " b/"); i >= 0 {
				cur.OldPath, cur.Path = prgStripPrefix(rest[:i]), prgStripPrefix(rest[i+1:])
			} else {
				cur.Path = prgStripPrefix(rest)
			}
			continue
		}
		if cur == nil {
			continue
		}
		if hunk == nil {
			switch {
			case strings.HasPrefix(ln, "new file mode"):
				cur.Status = "added"
			case strings.HasPrefix(ln, "deleted file mode"):
				cur.Status = "deleted"
			case strings.HasPrefix(ln, "rename from "):
				cur.Status, cur.OldPath = "renamed", strings.TrimPrefix(ln, "rename from ")
			case strings.HasPrefix(ln, "rename to "):
				cur.Path = strings.TrimPrefix(ln, "rename to ")
			case strings.HasPrefix(ln, "Binary files") || strings.HasPrefix(ln, "GIT binary patch"):
				cur.Binary = true
			case strings.HasPrefix(ln, "+++ "):
				if p := strings.TrimPrefix(ln, "+++ "); p != "/dev/null" {
					cur.Path = prgStripPrefix(p)
				}
			}
		}
		if m := prgHunkRe.FindStringSubmatch(ln); m != nil {
			flush()
			oldNo, _ = strconv.Atoi(m[1])
			newNo, _ = strconv.Atoi(m[2])
			hunk = &prgHunk{ID: fmt.Sprintf("%sh%d", cur.ID, len(cur.Hunks)), Header: ln}
			continue
		}
		if hunk == nil || ln == "" && false {
			continue
		}
		switch {
		case strings.HasPrefix(ln, "+"):
			hunk.Lines = append(hunk.Lines, prgLine{T: "a", N: newNo, X: ln[1:]})
			newNo++
			cur.Additions++
		case strings.HasPrefix(ln, "-"):
			hunk.Lines = append(hunk.Lines, prgLine{T: "d", O: oldNo, X: ln[1:]})
			oldNo++
			cur.Deletions++
		case strings.HasPrefix(ln, `\`):
			hunk.Lines = append(hunk.Lines, prgLine{T: "m", X: ln})
		case strings.HasPrefix(ln, " ") || ln == "":
			x := ""
			if ln != "" {
				x = ln[1:]
			}
			hunk.Lines = append(hunk.Lines, prgLine{T: "c", O: oldNo, N: newNo, X: x})
			oldNo++
			newNo++
		}
	}
	finish()
	// A trailing blank context line after the last hunk is an artifact of the
	// final newline in the text.
	for i := range files {
		for j := range files[i].Hunks {
			h := &files[i].Hunks[j]
			if n := len(h.Lines); j == len(files[i].Hunks)-1 && n > 0 && h.Lines[n-1].T == "c" && h.Lines[n-1].X == "" {
				h.Lines = h.Lines[:n-1]
			}
		}
	}
	return files
}

// prgAnnotate renders files the way the prompts describe: FILE/HUNK headings
// and a 6-column new-file line gutter.
func prgAnnotate(files []prgFile) string {
	var b strings.Builder
	for _, f := range files {
		extra := ""
		if f.Binary {
			extra = ", binary"
		}
		fmt.Fprintf(&b, "FILE %s: %s  [%s%s]\n", f.ID, f.Path, f.Status, extra)
		for _, h := range f.Hunks {
			fmt.Fprintf(&b, "HUNK %s: %s\n", h.ID, h.Header)
			for _, l := range h.Lines {
				gutter, sign := "      ", " "
				switch l.T {
				case "a":
					sign = "+"
					gutter = fmt.Sprintf("%6d", l.N)
				case "c":
					gutter = fmt.Sprintf("%6d", l.N)
				case "d":
					sign = "-"
				case "m":
					sign = "\\"
				}
				fmt.Fprintf(&b, "%s %s%s\n", gutter, sign, l.X)
			}
		}
		b.WriteString("\n")
		if b.Len() > prgDiffPromptCh {
			b.WriteString("... [diff truncated] ...\n")
			break
		}
	}
	return b.String()
}

// ---------------------------------------------------------------- prompts

// prgPromptDir is the on-disk override for prompts, or "" for the embedded ones.
func prgPromptDir() string {
	// Absolute, so the answer does not depend on the directory px0 was started from.
	if d := os.Getenv("PX0_PRGROK_PROMPTS"); d != "" {
		if abs, err := filepath.Abs(d); err == nil {
			return abs
		}
		return d
	}
	if devWebDir != "" {
		return filepath.Join(devWebDir, "prgrok", "prompts")
	}
	return ""
}

func prgReadPromptFile(name string) (string, error) {
	if d := prgPromptDir(); d != "" {
		if b, err := os.ReadFile(filepath.Join(d, name)); err == nil {
			return string(b), nil
		}
	}
	b, err := prgPromptFS.ReadFile("prgrok/prompts/" + name)
	return string(b), err
}

// prgRenderPrompt builds the single prompt for an action from its template,
// the input object and the output schema.
func prgRenderPrompt(action string, input map[string]any, outputPath string) (string, error) {
	tmpl, err := prgReadPromptFile(action + ".prompt.md")
	if err != nil {
		return "", err
	}
	schema, err := prgReadPromptFile(action + ".output.schema.json")
	if err != nil {
		return "", err
	}
	in, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", err
	}
	example, _ := prgReadPromptFile(action + ".example.json")
	src, _ := input["source"].(string)
	return strings.NewReplacer(
		"{{input_json}}", string(in),
		"{{output_schema}}", strings.TrimSpace(schema),
		"{{output_example}}", strings.TrimSpace(example),
		"{{source}}", src,
		"{{output_path}}", outputPath,
	).Replace(tmpl), nil
}

func prgInputBase(r prgRepo, meta *prgPRMeta) map[string]any {
	return map[string]any{
		"kind": "pull request", "repo": r.slug(), "number": meta.Number,
		"title": meta.Title, "description": meta.Body,
	}
}

func prgInputFor(r prgRepo, meta *prgPRMeta, files []prgFile) map[string]any {
	return grokExplainInput(prgInputBase(r, meta), files)
}

// grokExplainInput completes a subject's header fields (kind, repo, title,
// description, ...) with what every explain and visual run is given: the word
// limits, the file/hunk outline and the annotated diff.
func grokExplainInput(base map[string]any, files []prgFile) map[string]any {
	outline := make([]map[string]any, 0, len(files))
	for _, f := range files {
		hs := make([]map[string]string, 0, len(f.Hunks))
		for _, h := range f.Hunks {
			hs = append(hs, map[string]string{"id": h.ID, "header": h.Header})
		}
		outline = append(outline, map[string]any{
			"id": f.ID, "path": f.Path, "status": f.Status,
			"additions": f.Additions, "deletions": f.Deletions, "hunks": hs,
		})
	}
	in := map[string]any{"limits": prgLimits, "files": outline, "diff": prgAnnotate(files)}
	for k, v := range base {
		in[k] = v
	}
	return in
}

// ---------------------------------------------------------------- results

type prgSection struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Text  string `json:"text"`
}

type prgElement struct {
	ID      string `json:"id"`
	Line    int    `json:"line"`
	Kind    string `json:"kind,omitempty"`
	Name    string `json:"name,omitempty"`
	Summary string `json:"summary"`
}

type prgHunkNote struct {
	ID       string       `json:"id"`
	Summary  string       `json:"summary"`
	Elements []prgElement `json:"elements,omitempty"`
}

type prgFileNote struct {
	ID      string        `json:"id"`
	Summary string        `json:"summary"`
	Hunks   []prgHunkNote `json:"hunks"`
}

type prgDiagram struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	Before      string `json:"before,omitempty"`
	Mermaid     string `json:"mermaid"`
	Explanation string `json:"explanation"`
}

type prgResult struct {
	Action   string        `json:"action"`
	Source   string        `json:"source,omitempty"` // narrate: explain or visual
	Number   int           `json:"number"`
	Ref      string        `json:"ref,omitempty"` // CommitGrok: the commit
	Harness  string        `json:"harness"`
	Model    string        `json:"model,omitempty"`
	At       string        `json:"cachedAt"`
	Overall  string        `json:"overall,omitempty"`
	Files    []prgFileNote `json:"files,omitempty"`
	Diagrams []prgDiagram  `json:"diagrams,omitempty"`
	Sections []prgSection  `json:"sections,omitempty"` // narrate
	Minutes  int           `json:"minutes,omitempty"`
}

var prgFenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*\\})\\s*```")

// prgExtractJSON pulls the JSON object out of agent stdout: the first fenced
// block when there is one, else the span from the first { to the last }.
func prgExtractJSON(out string) ([]byte, error) {
	if m := prgFenceRe.FindStringSubmatch(out); m != nil {
		return prgValidOrRepaired([]byte(m[1])), nil
	}
	i, j := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if i < 0 || j <= i {
		return nil, errors.New("the agent did not return a JSON object")
	}
	return prgValidOrRepaired([]byte(out[i : j+1])), nil
}

func prgValidOrRepaired(b []byte) []byte {
	if json.Valid(b) {
		return b
	}
	if fixed := prgRepairJSON(b); json.Valid(fixed) {
		return fixed
	}
	return b
}

// prgRepairJSON fixes the most common slip in model-written JSON: raw newlines,
// carriage returns and tabs inside string values. It cannot recover unescaped
// double quotes inside a string; the prompts forbid those.
func prgRepairJSON(b []byte) []byte {
	var out bytes.Buffer
	inStr, esc := false, false
	for _, c := range b {
		switch {
		case esc:
			esc = false
			out.WriteByte(c)
		case inStr && c == '\\':
			esc = true
			out.WriteByte(c)
		case c == '"':
			inStr = !inStr
			out.WriteByte(c)
		case inStr && c == '\n':
			out.WriteString(`\n`)
		case inStr && c == '\r':
			out.WriteString(`\r`)
		case inStr && c == '\t':
			out.WriteString(`\t`)
		default:
			out.WriteByte(c)
		}
	}
	return out.Bytes()
}

var prgMermaidFence = regexp.MustCompile("(?s)^\\s*```(?:mermaid)?\\s*(.*?)\\s*```\\s*$")

func prgCleanMermaid(s string) string {
	if m := prgMermaidFence.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return strings.TrimSpace(s)
}

func prgTrunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// prgNormalize validates an explain or visual reply against the PR's real ids
// and keeps only what the UI can place.
func prgNormalize(action string, data []byte, files []prgFile) (*prgResult, error) {
	var raw struct {
		Overall string `json:"overall"`
		Files   []struct {
			ID      string `json:"id"`
			Summary string `json:"summary"`
			Hunks   []struct {
				ID       string `json:"id"`
				Summary  string `json:"summary"`
				Elements []struct {
					Line    int    `json:"line"`
					Kind    string `json:"kind"`
					Name    string `json:"name"`
					Summary string `json:"summary"`
				} `json:"elements"`
			} `json:"hunks"`
		} `json:"files"`
		Diagrams []struct {
			Type, Title, Before, Mermaid, Explanation string
		} `json:"diagrams"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("the agent's JSON is invalid: %v", err)
	}
	if strings.TrimSpace(raw.Overall) == "" && len(raw.Files) == 0 {
		return nil, errors.New("the agent's JSON has no overall or files")
	}
	known := map[string]map[string]bool{}
	for _, f := range files {
		hs := map[string]bool{}
		for _, h := range f.Hunks {
			hs[h.ID] = true
		}
		known[f.ID] = hs
	}
	res := &prgResult{Action: action, Overall: strings.TrimSpace(raw.Overall)}
	for _, f := range raw.Files {
		hs, ok := known[f.ID]
		if !ok {
			continue
		}
		fn := prgFileNote{ID: f.ID, Summary: strings.TrimSpace(f.Summary)}
		for _, h := range f.Hunks {
			if !hs[h.ID] {
				continue
			}
			hn := prgHunkNote{ID: h.ID, Summary: strings.TrimSpace(h.Summary)}
			if action == "explain" {
				for _, e := range h.Elements {
					if e.Line > 0 && strings.TrimSpace(e.Summary) != "" {
						hn.Elements = append(hn.Elements, prgElement{
							ID: fmt.Sprintf("%se%d", h.ID, len(hn.Elements)), Line: e.Line,
							Kind: e.Kind, Name: e.Name, Summary: strings.TrimSpace(e.Summary),
						})
					}
				}
			}
			fn.Hunks = append(fn.Hunks, hn)
		}
		res.Files = append(res.Files, fn)
	}
	if action == "visual" {
		types := map[string]bool{"er": true, "sequence": true, "architecture": true, "state": true, "flowchart": true}
		for _, d := range raw.Diagrams {
			m := prgCleanMermaid(d.Mermaid)
			if m == "" {
				continue
			}
			t := strings.ToLower(strings.TrimSpace(d.Type))
			if !types[t] {
				t = "flowchart"
			}
			dg := prgDiagram{ID: fmt.Sprintf("d%d", len(res.Diagrams)), Type: t, Title: strings.TrimSpace(d.Title),
				Mermaid: m, Explanation: strings.TrimSpace(d.Explanation)}
			if t == "architecture" {
				dg.Before = prgCleanMermaid(d.Before)
			}
			res.Diagrams = append(res.Diagrams, dg)
		}
	}
	return res, nil
}

// prgSections flattens an explain or visual result into the narration input.
func prgSections(src *prgResult, files []prgFile) []prgSection {
	paths := map[string]string{}
	for _, f := range files {
		paths[f.ID] = f.Path
	}
	var out []prgSection
	add := func(id, kind, label, text string) {
		if strings.TrimSpace(text) != "" {
			out = append(out, prgSection{ID: id, Kind: kind, Label: label, Text: strings.TrimSpace(text)})
		}
	}
	add("overall", "overall", "Overview", src.Overall)
	for _, f := range src.Files {
		add(f.ID, "file", paths[f.ID], f.Summary)
		for _, h := range f.Hunks {
			add(h.ID, "hunk", paths[f.ID]+" · "+h.ID, h.Summary)
			for _, e := range h.Elements {
				add(e.ID, "element", paths[f.ID]+" · "+prgFirst(e.Name, e.ID), e.Summary)
			}
		}
	}
	for _, d := range src.Diagrams {
		add(d.ID, "diagram", prgFirst(d.Title, d.ID), d.Explanation)
	}
	return out
}

func prgFirst(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// prgNormalizeNarrate keeps one talking point per input section, falling back
// to the written text when the agent dropped one.
func prgNormalizeNarrate(data []byte, in []prgSection) (*prgResult, error) {
	var raw struct {
		Sections []struct{ ID, Text string } `json:"sections"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("the agent's JSON is invalid: %v", err)
	}
	spoken := map[string]string{}
	for _, s := range raw.Sections {
		if t := strings.TrimSpace(s.Text); t != "" {
			spoken[s.ID] = t
		}
	}
	if len(spoken) == 0 {
		return nil, errors.New("the agent's JSON has no sections")
	}
	res := &prgResult{Action: "narrate"}
	for _, s := range in {
		t := spoken[s.ID]
		if t == "" {
			t = s.Text
		}
		res.Sections = append(res.Sections, prgSection{ID: s.ID, Kind: s.Kind, Label: s.Label, Text: t})
	}
	return res, nil
}

// ---------------------------------------------------------------- data files

// prgDataDir holds both what the agent writes and the cache built from it:
// ~/.px0/cache/data/prgrok (or $XDG_CACHE_HOME/px0/data/prgrok).
func prgDataDir() string { return filepath.Join(filepath.Dir(vendorCacheDir()), "data", "prgrok") }

func prgSafe(s string) string {
	s = strings.Trim(unsafeFileChars.ReplaceAllString(s, "_"), "_.")
	if s == "" {
		return "x"
	}
	return s
}

// prgPath names the result file for one request, e.g.
// <data>/acme_widgets/pr-42/1a2b3c4d/explain.json. The name depends on the PR,
// its head commit and the action only: not on which file or layout is on screen,
// and not on the harness, so switching the harness dropdown still shows the
// saved result (the result records which harness wrote it). Regenerate replaces
// it. Narration also depends on what it narrates and its length.
func (s *Server) prgPath(repo prgRepo, head string, q prgRequest) string {
	if len(head) > 8 {
		head = head[:8]
	}
	return filepath.Join(prgDataDir(), prgSafe(repo.slug()), "pr-"+strconv.Itoa(q.Number), prgSafe(head), grokResultName(q))
}

// grokResultName is the file name of a request's result inside its subject's
// directory: the action, and for narration what it narrates and how long.
func grokResultName(q prgRequest) string {
	name := []string{q.Action}
	if q.Action == "narrate" {
		name = append(name, q.Source, strconv.Itoa(q.Minutes)+"min")
	}
	for i := range name {
		name[i] = prgSafe(name[i])
	}
	return strings.Join(name, ".") + ".json"
}

// prgWorkDirName is the directory, inside the project, where the agent writes
// its raw JSON. The agent can write there (its harness runs in the project), and
// px0 deletes the file once it has validated it and stored it in the cache, so
// the output path and the cache key/layout stay independent of each other.
const prgWorkDirName = ".px0-prgrok"

func prgOutputPath(root string, q prgRequest) string {
	tag := "pr" + strconv.Itoa(q.Number)
	if q.Ref != "" {
		tag = prgSafe(q.Ref)
		if len(tag) > 12 {
			tag = tag[:12]
		}
	}
	name := fmt.Sprintf("%s-%s-%d.json", q.Action, tag, time.Now().UnixNano())
	return filepath.Join(root, prgWorkDirName, name)
}

// prgDiscard removes the agent's output file, and its directory when empty.
func prgDiscard(path string) {
	os.Remove(path)
	os.Remove(filepath.Dir(path)) // fails, harmlessly, if other jobs still use it
}

func prgCacheRead(path string) *prgResult {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var r prgResult
	if json.Unmarshal(b, &r) != nil || r.At == "" {
		return nil
	}
	return &r
}

func prgCacheWrite(path string, r *prgResult) {
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	if b, err := json.MarshalIndent(r, "", "  "); err == nil {
		tmp := path + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			os.Rename(tmp, path)
		}
	}
}

// ---------------------------------------------------------------- HTTP

type prgRequest struct {
	Number  int    `json:"number"`  // PRGrok: the pull request
	Ref     string `json:"ref"`     // CommitGrok: the full commit sha
	Action  string `json:"action"`  // explain, visual, narrate. Always the whole PR.
	Source  string `json:"source"`  // narrate: explain or visual
	Minutes int    `json:"minutes"` // narrate
	Refresh bool   `json:"refresh"`
}

// prgJob remembers what a thread was asked, so the watcher that sees its turn
// finish can parse and store the result exactly once.
type prgJob struct {
	req      prgRequest
	files    []prgFile // the files the agent was shown
	sections []prgSection
	cache    string // final result file
	pending  string // where the agent writes its JSON (in the project)
	raw      string // what the agent produced, kept for error reports
	result   *prgResult
	err      string
	done     bool
}

type prgState struct {
	mu   sync.Mutex
	jobs map[string]*prgJob // by thread id
}

// prgOutcome is how a finished agent turn is described to prgFinish.
type prgOutcome struct {
	Stdout  string // the agent's reply text
	Error   string
	Harness string
	Model   string
}

func (s *Server) prgContext(w http.ResponseWriter) (string, prgRepo, bool) {
	root := s.ix.Root()
	r := prgDetect(root)
	if r.Provider == "" {
		fail(w, http.StatusBadRequest, prgNoProvider(r))
		return root, r, false
	}
	return root, r, true
}

func (s *Server) handlePRGrokInfo(w http.ResponseWriter, r *http.Request) {
	root := s.ix.Root()
	repo := prgDetect(root)
	tool := map[string]string{"github": "gh", "bitbucket": "twg"}[repo.Provider]
	info := map[string]any{
		"provider": repo.Provider, "host": repo.Host, "repo": repo.slug(), "reason": repo.Reason,
		"pageSize": prgPageSize, "states": prgStates(repo.Provider), "dev": devWebDir != "",
		"promptDir": prgPromptDir(), "wordsPerMinute": prgWordsPerMin,
	}
	if tool != "" {
		_, err := exec.LookPath(tool)
		info["tool"], info["toolOk"] = tool, err == nil
	}
	if s.agent != nil {
		info["agent"] = map[string]any{"harness": s.agent.Name(), "model": s.agent.Model()}
	}
	writeJSON(w, info)
}

func (s *Server) handlePRGrokPRs(w http.ResponseWriter, r *http.Request) {
	root, repo, ok := s.prgContext(w)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	state := r.URL.Query().Get("state")
	valid := false
	for _, st := range prgStates(repo.Provider) {
		valid = valid || st == state
	}
	if !valid {
		state = "open"
	}
	list, err := prgListPRs(r.Context(), root, repo, page, state)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, list)
}

func (s *Server) handlePRGrokPR(w http.ResponseWriter, r *http.Request) {
	root, repo, ok := s.prgContext(w)
	if !ok {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("number"))
	if n <= 0 {
		fail(w, http.StatusBadRequest, "number is required")
		return
	}
	meta, diff, err := prgFetchPR(r.Context(), root, repo, n)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	files := prgParseDiff(diff)
	adds, dels := 0, 0
	for _, f := range files {
		adds += f.Additions
		dels += f.Deletions
	}
	writeJSON(w, map[string]any{"meta": meta, "files": files, "additions": adds, "deletions": dels})
}

func prgValidRequest(q *prgRequest) error {
	if q.Number <= 0 {
		return errors.New("number is required")
	}
	return grokValidAction(q)
}

// grokValidAction checks the part of a request every Grok shares: the action and
// its narration options. Which subject it is about is the caller's business.
func grokValidAction(q *prgRequest) error {
	switch q.Action {
	case "explain", "visual", "narrate":
	default:
		return errors.New("action must be explain, visual or narrate")
	}
	if q.Action == "narrate" {
		if q.Source != "visual" {
			q.Source = "explain"
		}
		if q.Minutes < 1 {
			q.Minutes = 3
		}
		if q.Minutes > 30 {
			q.Minutes = 30
		}
	} else {
		q.Source = ""
	}
	return nil
}

// grokSubject is what one AI action explains: a pull request, a commit, and
// later a code file. Everything below the resolver (diff model, prompts,
// validation, cache, thread, job) is shared by every subject.
type grokSubject struct {
	Kind  string // "pull request", "commit": sent to the prompts as input.kind
	Tool  string // "PRGrok", "CommitGrok": names the agent thread
	Label string // "#42", "a1b2c3d": names the agent thread
	Title string
	Base  map[string]any          // header fields of the prompt input
	Files []prgFile               // what the agent is shown; ids are the contract
	Cache func(prgRequest) string // the saved result's path for a request
}

// grokResolver finds the subject a request is about. On failure it returns the
// HTTP status to answer with.
type grokResolver func(ctx context.Context, q prgRequest) (*grokSubject, int, error)

func grokQuery(r *http.Request) prgRequest {
	qs := r.URL.Query()
	q := prgRequest{Action: qs.Get("action"), Source: qs.Get("source"), Ref: qs.Get("ref")}
	q.Number, _ = strconv.Atoi(qs.Get("number"))
	q.Minutes, _ = strconv.Atoi(qs.Get("minutes"))
	return q
}

func (s *Server) prSubject(root string, repo prgRepo) grokResolver {
	return func(ctx context.Context, q prgRequest) (*grokSubject, int, error) {
		if q.Number <= 0 {
			return nil, http.StatusBadRequest, errors.New("number is required")
		}
		meta, diff, err := prgFetchPR(ctx, root, repo, q.Number)
		if err != nil {
			return nil, http.StatusBadGateway, err
		}
		files := prgParseDiff(diff)
		return &grokSubject{
			Kind: "pull request", Tool: "PRGrok", Label: fmt.Sprintf("#%d", q.Number), Title: meta.Title,
			Base:  prgInputBase(repo, meta),
			Files: files,
			Cache: func(r prgRequest) string { return s.prgPath(repo, meta.Head, r) },
		}, 0, nil
	}
}

// handlePRGrokResult peeks at the saved result without running anything.
func (s *Server) handlePRGrokResult(w http.ResponseWriter, r *http.Request) {
	root, repo, ok := s.prgContext(w)
	if !ok {
		return
	}
	s.grokResult(w, r, s.prSubject(root, repo))
}

func (s *Server) grokResult(w http.ResponseWriter, r *http.Request, resolve grokResolver) {
	q := grokQuery(r)
	if err := grokValidAction(&q); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	subj, status, err := resolve(r.Context(), q)
	if err != nil {
		fail(w, status, err.Error())
		return
	}
	if res := prgCacheRead(subj.Cache(q)); res != nil {
		writeJSON(w, map[string]any{"cached": true, "result": res})
		return
	}
	writeJSON(w, map[string]any{"cached": false})
}

// handlePRGrokRun starts (or answers from the saved result) one AI action for
// the whole PR. It runs as a thread, so the agent's output streams live in the
// Agent bar; the response carries the thread id.
func (s *Server) handlePRGrokRun(w http.ResponseWriter, r *http.Request) {
	if !localPost(w, r) {
		return
	}
	root, repo, ok := s.prgContext(w)
	if !ok {
		return
	}
	s.grokRun(w, r, root, s.prSubject(root, repo))
}

// grokRun starts one AI action for the whole subject as a thread, or answers from
// its saved result.
func (s *Server) grokRun(w http.ResponseWriter, r *http.Request, root string, resolve grokResolver) {
	var q prgRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&q); err != nil {
		fail(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := grokValidAction(&q); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.agent == nil || s.threads == nil || s.agent.Name() == "" {
		fail(w, http.StatusServiceUnavailable, "no coding harness is selected: pick one in the Grok bar or the Agent bar")
		return
	}
	subj, status, err := resolve(r.Context(), q)
	if err != nil {
		fail(w, status, err.Error())
		return
	}
	files := subj.Files
	cache := subj.Cache(q)
	if !q.Refresh {
		if res := prgCacheRead(cache); res != nil {
			writeJSON(w, map[string]any{"cached": true, "result": res})
			return
		}
	}

	var input map[string]any
	var sections []prgSection
	if q.Action == "narrate" {
		src := prgCacheRead(subj.Cache(prgRequest{Number: q.Number, Ref: q.Ref, Action: q.Source}))
		if src == nil {
			fail(w, http.StatusConflict, "Run Explain Hunks or Explain visually first: there is nothing to narrate yet.")
			return
		}
		sections = prgSections(src, files)
		if len(sections) == 0 {
			fail(w, http.StatusConflict, "The "+q.Source+" result has no text to narrate.")
			return
		}
		input = map[string]any{
			"kind": subj.Kind, "source": q.Source, "minutes": q.Minutes,
			"budget_words": q.Minutes * prgWordsPerMin, "sections": sections,
		}
	} else {
		input = grokExplainInput(subj.Base, files)
	}
	pending := prgOutputPath(root, q)
	if err := os.MkdirAll(filepath.Dir(pending), 0o755); err != nil {
		fail(w, http.StatusInternalServerError, "cannot create "+filepath.Dir(pending)+": "+err.Error())
		return
	}
	prompt, err := prgRenderPrompt(q.Action, input, pending)
	if err != nil {
		prgDiscard(pending)
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	title := fmt.Sprintf("%s %s %s: %s", subj.Tool, map[string]string{"explain": "Explain Hunks", "visual": "Explain visually", "narrate": "Narrate"}[q.Action], subj.Label, subj.Title)
	t, err := s.threads.Create("", "", 0, 0, prompt, "", title)
	if err != nil {
		prgDiscard(pending)
		fail(w, threadStatus(err), err.Error())
		return
	}
	pj := &prgJob{req: q, files: files, sections: sections, cache: cache, pending: pending}
	s.prg.mu.Lock()
	if s.prg.jobs == nil {
		s.prg.jobs = map[string]*prgJob{}
	}
	s.prg.jobs[t.ID] = pj
	s.prg.mu.Unlock()
	// Settle the job when the turn ends, whether or not the page is still
	// polling, so the result is saved and the agent's file removed.
	go s.prgWatch(t.ID, pj)
	writeJSON(w, map[string]any{"cached": false, "thread": t.ID})
}

// prgTurnOutcome describes the thread's latest turn, and whether it is still running.
func (s *Server) prgTurnOutcome(id string) (out prgOutcome, running, found bool) {
	t := s.threads.Get(id)
	if t == nil || len(t.Turns) == 0 {
		return out, false, t != nil
	}
	tr := t.Turns[len(t.Turns)-1]
	return prgOutcome{Stdout: tr.Reply, Error: tr.Error, Harness: tr.Harness, Model: tr.Model}, tr.Running, true
}

// prgWatch waits for the thread's turn to end, then settles the job.
func (s *Server) prgWatch(id string, pj *prgJob) {
	for {
		out, running, found := s.prgTurnOutcome(id)
		if !found {
			prgDiscard(pj.pending) // thread deleted
			return
		}
		if !running {
			s.prgSettle(pj, out)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// prgSettle validates and saves a finished job exactly once.
func (s *Server) prgSettle(pj *prgJob, out prgOutcome) {
	s.prg.mu.Lock()
	defer s.prg.mu.Unlock()
	if !pj.done {
		pj.done = true
		pj.result, pj.err = s.prgFinish(pj, out)
	}
}

// handlePRGrokJob reports an action's progress; once the thread's turn ends
// it returns the settled result or error. The live output itself is in the thread.
func (s *Server) handlePRGrokJob(w http.ResponseWriter, r *http.Request) {
	if s.threads == nil {
		fail(w, http.StatusNotFound, "no agent")
		return
	}
	id := r.URL.Query().Get("id")
	s.prg.mu.Lock()
	pj := s.prg.jobs[id]
	s.prg.mu.Unlock()
	out, running, found := s.prgTurnOutcome(id)
	if pj == nil || !found {
		fail(w, http.StatusNotFound, "unknown job")
		return
	}
	if running {
		writeJSON(w, map[string]any{"running": true})
		return
	}
	s.prgSettle(pj, out)
	s.prg.mu.Lock()
	defer s.prg.mu.Unlock()
	if pj.err != "" {
		writeJSON(w, map[string]any{"running": false, "error": pj.err, "raw": prgTrunc(prgFirst(pj.raw, out.Stdout), 2000)})
		return
	}
	writeJSON(w, map[string]any{"running": false, "result": pj.result})
}

func (s *Server) prgFinish(pj *prgJob, out prgOutcome) (*prgResult, string) {
	defer prgDiscard(pj.pending)
	if out.Error != "" {
		return nil, out.Error
	}
	// The agent was asked to write its JSON to pj.pending. Only when it could
	// not (a harness that cannot write files) is its reply parsed.
	text := out.Stdout
	fromFile := false
	if b, err := os.ReadFile(pj.pending); err == nil {
		text, fromFile = string(b), true
	}
	pj.raw = text
	data, err := prgExtractJSON(text)
	if err != nil {
		if !fromFile {
			return nil, "the agent wrote no output file at " + pj.pending + " and its reply had no JSON"
		}
		return nil, "the agent's output file is not JSON: " + err.Error()
	}
	var res *prgResult
	if pj.req.Action == "narrate" {
		res, err = prgNormalizeNarrate(data, pj.sections)
	} else {
		res, err = prgNormalize(pj.req.Action, data, pj.files)
	}
	if err != nil {
		return nil, err.Error()
	}
	res.Action, res.Source, res.Number, res.Ref = pj.req.Action, pj.req.Source, pj.req.Number, pj.req.Ref
	res.Minutes = 0
	if pj.req.Action == "narrate" {
		res.Minutes = pj.req.Minutes
	}
	res.Harness, res.Model = out.Harness, out.Model
	res.At = time.Now().UTC().Format(time.RFC3339)
	// Validated: store it where the saved results live; the deferred discard
	// then removes the agent's copy from the project.
	prgCacheWrite(pj.cache, res)
	return res, ""
}
