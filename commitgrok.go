package main

// CommitGrok: browse the local git history (with its merge graph) and explain,
// visualise and narrate one commit.
//
// It is PRGrok with a different subject. Commits are read with the local git
// client; everything that happens after that (diff model, prompts, validation,
// cache, agent thread, job polling, narration) is the shared engine in
// prgrok.go, reached through a grokSubject. The agent's job endpoint is shared too:
// /api/prgrok/job settles any Grok job.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	cgPageSize   = 10
	cgMaxCommits = 5000 // the deepest history one page request will walk
	cgLaneColors = 8
)

var cgShaRe = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// ---------------------------------------------------------------- git

func cgGit(ctx context.Context, root string, args ...string) (string, error) {
	out, err := prgExec(ctx, root, "git", append([]string{"-c", "core.quotepath=false"}, args...)...)
	return string(out), err
}

type cgBranch struct {
	Name   string `json:"name"`
	Remote bool   `json:"remote,omitempty"`
}

// cgBranches lists the refs a history can start from: local branches, then
// remote-tracking ones.
func cgBranches(ctx context.Context, root string) ([]cgBranch, error) {
	out, err := cgGit(ctx, root, "for-each-ref", "--sort=-committerdate", "--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	var local, remote []cgBranch
	for _, ref := range strings.Split(strings.TrimSpace(out), "\n") {
		switch {
		case strings.HasPrefix(ref, "refs/heads/"):
			local = append(local, cgBranch{Name: strings.TrimPrefix(ref, "refs/heads/")})
		case strings.HasPrefix(ref, "refs/remotes/") && !strings.HasSuffix(ref, "/HEAD"):
			remote = append(remote, cgBranch{Name: strings.TrimPrefix(ref, "refs/remotes/"), Remote: true})
		}
	}
	return append(local, remote...), nil
}

// cgCurrentBranch is the checked-out branch, or "HEAD" when detached.
func cgCurrentBranch(ctx context.Context, root string) string {
	out, err := cgGit(ctx, root, "rev-parse", "--abbrev-ref", "HEAD")
	if b := strings.TrimSpace(out); err == nil && b != "" {
		return b
	}
	return "HEAD"
}

// cgValidBranch accepts a listed branch or "HEAD" and nothing else, so a
// request cannot smuggle an option or an arbitrary revision expression to git.
func cgValidBranch(ctx context.Context, root, name string) bool {
	if name == "HEAD" {
		return true
	}
	bs, err := cgBranches(ctx, root)
	if err != nil {
		return false
	}
	for _, b := range bs {
		if b.Name == name {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- history graph

// cgLane is one line of the graph in one row. Col is its column. A lane in
// Top enters the row from above and either ends at the commit (ToNode) or runs
// straight through; a lane in Bottom leaves the commit towards a parent.
type cgLane struct {
	Col    int  `json:"col"`
	Color  int  `json:"color"`
	ToNode bool `json:"toNode,omitempty"`
}

type cgRef struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // head, branch, remote, tag
}

type cgCommit struct {
	Sha     string   `json:"sha"`
	Short   string   `json:"short"`
	Parents []string `json:"parents"`
	Author  string   `json:"author"`
	Date    string   `json:"date"`
	Subject string   `json:"subject"`
	Refs    []cgRef  `json:"refs,omitempty"`
	Merge   bool     `json:"merge,omitempty"`
	Node    int      `json:"node"`
	Color   int      `json:"color"`
	Top     []cgLane `json:"top"`
	Bottom  []cgLane `json:"bottom"`
}

// cgLayout assigns every commit (newest first, children before parents) its
// column and the lines drawn through its row. Columns are stable across rows,
// so a page of a long history lines up with the pages around it as long as the
// layout is computed from the start of the history, which cgLog does.
func cgLayout(commits []cgCommit) {
	type slot struct {
		sha   string
		color int
	}
	var lanes []slot
	nextColor := 0
	newColor := func() int { c := nextColor % cgLaneColors; nextColor++; return c }
	free := func() int {
		for i, l := range lanes {
			if l.sha == "" {
				return i
			}
		}
		lanes = append(lanes, slot{})
		return len(lanes) - 1
	}
	for ci := range commits {
		c := &commits[ci]
		node := -1
		for i, l := range lanes {
			if l.sha == c.Sha {
				node = i
				break
			}
		}
		fresh := node < 0 // a branch tip nothing above leads to
		if fresh {
			node = free()
			lanes[node] = slot{c.Sha, newColor()}
		}
		c.Node, c.Color = node, lanes[node].color
		c.Top = []cgLane{}
		for i, l := range lanes {
			if l.sha == "" || (fresh && i == node) {
				continue
			}
			c.Top = append(c.Top, cgLane{Col: i, Color: l.color, ToNode: l.sha == c.Sha})
		}
		for i := range lanes {
			if lanes[i].sha == c.Sha && i != node {
				lanes[i] = slot{}
			}
		}
		c.Bottom = []cgLane{}
		if len(c.Parents) == 0 {
			lanes[node] = slot{}
		} else {
			lanes[node] = slot{c.Parents[0], c.Color}
			c.Bottom = append(c.Bottom, cgLane{Col: node, Color: c.Color})
			for _, p := range c.Parents[1:] {
				at := -1
				for i, l := range lanes {
					if l.sha == p {
						at = i
						break
					}
				}
				if at < 0 {
					at = free()
					lanes[at] = slot{p, newColor()}
				}
				c.Bottom = append(c.Bottom, cgLane{Col: at, Color: lanes[at].color})
			}
		}
		for len(lanes) > 0 && lanes[len(lanes)-1].sha == "" {
			lanes = lanes[:len(lanes)-1]
		}
	}
}

func cgParseRefs(decorate string) []cgRef {
	var refs []cgRef
	for _, part := range strings.Split(decorate, ",") {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
		case strings.HasPrefix(part, "HEAD -> "):
			refs = append(refs, cgRef{Name: strings.TrimPrefix(part, "HEAD -> "), Kind: "head"})
		case part == "HEAD":
			refs = append(refs, cgRef{Name: "HEAD", Kind: "head"})
		case strings.HasPrefix(part, "tag: "):
			refs = append(refs, cgRef{Name: strings.TrimPrefix(part, "tag: "), Kind: "tag"})
		case strings.Contains(part, "/"):
			refs = append(refs, cgRef{Name: part, Kind: "remote"})
		default:
			refs = append(refs, cgRef{Name: part, Kind: "branch"})
		}
	}
	return refs
}

const cgLogFormat = "%H%x1f%P%x1f%an%x1f%aI%x1f%D%x1f%s%x1e"

func cgParseLog(out string) []cgCommit {
	var commits []cgCommit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		f := strings.Split(rec, "\x1f")
		if len(f) < 6 || f[0] == "" {
			continue
		}
		c := cgCommit{Sha: f[0], Short: f[0][:min(7, len(f[0]))], Parents: strings.Fields(f[1]),
			Author: f[2], Date: f[3], Refs: cgParseRefs(f[4]), Subject: f[5]}
		c.Merge = len(c.Parents) > 1
		commits = append(commits, c)
	}
	return commits
}

type cgLogPage struct {
	Branch  string     `json:"branch"`
	Page    int        `json:"page"`
	HasMore bool       `json:"hasMore"`
	Cols    int        `json:"cols"` // widest graph on this page
	Items   []cgCommit `json:"items"`
}

// cgLog returns one page of a branch's history, newest first. The graph is
// laid out from the newest commit down to the end of the page, then cut, so
// columns and colours carry over from page to page.
func cgLog(ctx context.Context, root, branch string, page int) (*cgLogPage, error) {
	return cgLogSized(ctx, root, branch, page, cgPageSize)
}

func cgLogSized(ctx context.Context, root, branch string, page, size int) (*cgLogPage, error) {
	if page < 1 {
		page = 1
	}
	if page*size > cgMaxCommits {
		return nil, fmt.Errorf("history is paged only to %d commits", cgMaxCommits)
	}
	out, err := cgGit(ctx, root, "log", "--topo-order", "--no-color", "-n", strconv.Itoa(page*size+1),
		"--format="+cgLogFormat, branch, "--")
	if err != nil {
		return nil, err
	}
	all := cgParseLog(out)
	cgLayout(all)
	res := &cgLogPage{Branch: branch, Page: page, Items: []cgCommit{}}
	start := (page - 1) * size
	if start < len(all) {
		end := min(start+size, len(all))
		res.Items = all[start:end]
		res.HasMore = len(all) > end
	}
	for _, c := range res.Items {
		res.Cols = max(res.Cols, c.Node+1)
		for _, l := range append(append([]cgLane{}, c.Top...), c.Bottom...) {
			res.Cols = max(res.Cols, l.Col+1)
		}
	}
	return res, nil
}

// ---------------------------------------------------------------- one commit

type cgMeta struct {
	Sha     string   `json:"sha"`
	Short   string   `json:"short"`
	Parents []string `json:"parents"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	Date    string   `json:"date"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Refs    []cgRef  `json:"refs,omitempty"`
}

// cgFetchCommit reads a commit and its diff. A merge commit is diffed against
// its first parent: what the merge brought into the branch.
func cgFetchCommit(ctx context.Context, root, sha string) (*cgMeta, string, error) {
	if !cgShaRe.MatchString(sha) {
		return nil, "", errors.New("sha is not a commit id")
	}
	full, err := cgGit(ctx, root, "rev-parse", "--verify", "--quiet", sha+"^{commit}")
	if err != nil || strings.TrimSpace(full) == "" {
		return nil, "", errors.New("no such commit: " + sha)
	}
	full = strings.TrimSpace(full)
	out, err := cgGit(ctx, root, "show", "-s", "--no-color", "--format=%H%x1f%P%x1f%an%x1f%ae%x1f%aI%x1f%s%x1f%D%x1f%b", full)
	if err != nil {
		return nil, "", err
	}
	f := strings.SplitN(strings.TrimRight(out, "\n"), "\x1f", 8)
	if len(f) < 8 {
		return nil, "", errors.New("unexpected git output")
	}
	m := &cgMeta{Sha: f[0], Short: f[0][:min(7, len(f[0]))], Parents: strings.Fields(f[1]), Author: f[2], Email: f[3],
		Date: f[4], Title: f[5], Refs: cgParseRefs(f[6]), Body: strings.TrimSpace(f[7])}
	var diff string
	if len(m.Parents) > 0 {
		diff, err = cgGit(ctx, root, "diff", "--no-color", "--no-ext-diff", "--no-textconv", "-M", m.Parents[0], full, "--")
	} else {
		diff, err = cgGit(ctx, root, "show", "--no-color", "--no-ext-diff", "--no-textconv", "-M", "--root", "--format=", full, "--")
	}
	if err != nil {
		return nil, "", err
	}
	return m, diff, nil
}

// ---------------------------------------------------------------- cache

// cgDataDir sits beside PRGrok's: ~/.px0/cache/data/commitgrok.
func cgDataDir() string { return filepath.Join(filepath.Dir(prgDataDir()), "commitgrok") }

// cgRepoName names the repository in prompts and in the cache: owner/repo when
// the remote says so, else the project folder.
func cgRepoName(root string) string {
	if r := prgDetect(root); r.Owner != "" && r.Repo != "" {
		return r.slug()
	}
	return filepath.Base(root)
}

// cgPath names the result file for one request, e.g.
// <data>/acme_widgets/<full sha>/explain.json. A commit never changes, so its
// full sha is the whole version key: no head to track, nothing to invalidate.
// Like PRGrok's, it ignores the view and the harness.
func cgPath(repo, sha string, q prgRequest) string {
	return filepath.Join(cgDataDir(), prgSafe(repo), prgSafe(sha), grokResultName(q))
}

// ---------------------------------------------------------------- HTTP

func (s *Server) cgSubject(root string) grokResolver {
	return func(ctx context.Context, q prgRequest) (*grokSubject, int, error) {
		if q.Ref == "" {
			return nil, http.StatusBadRequest, errors.New("ref is required")
		}
		meta, diff, err := cgFetchCommit(ctx, root, q.Ref)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		repo := cgRepoName(root)
		return &grokSubject{
			Kind: "commit", Tool: "CommitGrok", Label: meta.Short, Title: meta.Title,
			Base: map[string]any{
				"kind": "commit", "repo": repo, "ref": meta.Sha, "author": meta.Author,
				"title": meta.Title, "description": meta.Body,
			},
			Files: prgParseDiff(diff),
			Cache: func(r prgRequest) string { return cgPath(repo, meta.Sha, r) },
		}, 0, nil
	}
}

func (s *Server) handleCommitGrokInfo(w http.ResponseWriter, r *http.Request) {
	root := s.ix.Root()
	branches, err := cgBranches(r.Context(), root)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	sort.SliceStable(branches, func(i, j int) bool { return !branches[i].Remote && branches[j].Remote })
	info := map[string]any{
		"repo": cgRepoName(root), "pageSize": cgPageSize, "branches": branches,
		"current": cgCurrentBranch(r.Context(), root), "dev": devWebDir != "",
		"promptDir": prgPromptDir(), "wordsPerMinute": prgWordsPerMin,
	}
	if s.agent != nil {
		info["agent"] = map[string]any{"harness": s.agent.Name(), "model": s.agent.Model()}
	}
	writeJSON(w, info)
}

func (s *Server) handleCommitGrokLog(w http.ResponseWriter, r *http.Request) {
	root := s.ix.Root()
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = cgCurrentBranch(r.Context(), root)
	}
	if !cgValidBranch(r.Context(), root, branch) {
		fail(w, http.StatusBadRequest, "unknown branch: "+branch)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	res, err := cgLog(r.Context(), root, branch, page)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, res)
}

func (s *Server) handleCommitGrokCommit(w http.ResponseWriter, r *http.Request) {
	meta, diff, err := cgFetchCommit(r.Context(), s.ix.Root(), r.URL.Query().Get("sha"))
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
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

func (s *Server) handleCommitGrokResult(w http.ResponseWriter, r *http.Request) {
	s.grokResult(w, r, s.cgSubject(s.ix.Root()))
}

func (s *Server) handleCommitGrokRun(w http.ResponseWriter, r *http.Request) {
	if !localPost(w, r) {
		return
	}
	root := s.ix.Root()
	s.grokRun(w, r, root, s.cgSubject(root))
}
