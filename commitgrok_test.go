package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A history with one merge:
//
//	M  merge of f into main
//	|\
//	| f  feature commit
//	b |  main commit
//	|/
//	a  root
func TestCommitGrokLayoutMerge(t *testing.T) {
	cs := []cgCommit{
		{Sha: "M", Parents: []string{"b", "f"}},
		{Sha: "f", Parents: []string{"a"}},
		{Sha: "b", Parents: []string{"a"}},
		{Sha: "a"},
	}
	cgLayout(cs)
	if cs[0].Node != 0 || len(cs[0].Bottom) != 2 || cs[0].Bottom[0].Col != 0 || cs[0].Bottom[1].Col != 1 {
		t.Fatalf("merge row = %+v", cs[0])
	}
	if cs[0].Bottom[0].Color == cs[0].Bottom[1].Color {
		t.Errorf("merged branches share a colour: %+v", cs[0].Bottom)
	}
	if cs[1].Node != 1 || len(cs[1].Top) != 2 || cs[1].Top[0].ToNode || !cs[1].Top[1].ToNode {
		t.Fatalf("feature row = %+v", cs[1])
	}
	if cs[2].Node != 0 || !cs[2].Top[0].ToNode {
		t.Fatalf("main row = %+v", cs[2])
	}
	// Both lanes end at the common ancestor, which sits in the first column.
	if cs[3].Node != 0 || len(cs[3].Top) != 2 || !cs[3].Top[0].ToNode || !cs[3].Top[1].ToNode || len(cs[3].Bottom) != 0 {
		t.Fatalf("root row = %+v", cs[3])
	}
}

func TestCommitGrokLayoutIndependentBranchTips(t *testing.T) {
	cs := []cgCommit{{Sha: "x", Parents: []string{"p"}}, {Sha: "y", Parents: []string{"p"}}, {Sha: "p"}}
	cgLayout(cs)
	if cs[1].Node != 1 || len(cs[1].Top) != 1 || cs[1].Top[0].Col != 0 || cs[1].Top[0].ToNode {
		t.Fatalf("second tip = %+v", cs[1])
	}
	if cs[2].Node != 0 || len(cs[2].Top) != 2 {
		t.Fatalf("shared parent = %+v", cs[2])
	}
}

func TestCommitGrokParseRefs(t *testing.T) {
	got := cgParseRefs("HEAD -> main, origin/main, tag: v1.0, feature")
	want := []cgRef{{"main", "head"}, {"origin/main", "remote"}, {"v1.0", "tag"}, {"feature", "branch"}}
	if len(got) != len(want) {
		t.Fatalf("refs = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ref %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func cgTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := execGit(dir, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	commit := func(file, content, msg string) {
		os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644)
		run("add", file)
		run("-c", "user.name=T", "-c", "user.email=t@t", "commit", "-q", "-m", msg)
	}
	run("init", "-q", "-b", "main")
	commit("a.txt", "one\n", "root")
	run("checkout", "-q", "-b", "feature")
	commit("f.txt", "feat\n", "feature work")
	run("checkout", "-q", "main")
	commit("b.txt", "main\n", "main work")
	run("-c", "user.name=T", "-c", "user.email=t@t", "merge", "-q", "--no-ff", "-m", "merge feature", "feature")
	return dir
}

func TestCommitGrokLogPagesAndGraph(t *testing.T) {
	dir := cgTestRepo(t)
	ctx := context.Background()
	page, err := cgLog(ctx, dir, "main", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 4 || page.HasMore || page.Cols != 2 {
		t.Fatalf("page = %d items, more=%v, cols=%d", len(page.Items), page.HasMore, page.Cols)
	}
	if first := page.Items[0]; first.Subject != "merge feature" || !first.Merge || len(first.Bottom) != 2 {
		t.Fatalf("head = %+v", first)
	}
	if page.Items[0].Refs[0].Kind != "head" {
		t.Errorf("refs = %+v", page.Items[0].Refs)
	}
	if page.Items[3].Subject != "root" {
		t.Errorf("last = %+v", page.Items[3])
	}
	if bs, err := cgBranches(ctx, dir); err != nil || len(bs) != 2 || bs[0].Remote {
		t.Errorf("branches = %+v, %v", bs, err)
	}
	if cgCurrentBranch(ctx, dir) != "main" {
		t.Error("current branch")
	}
	if cgValidBranch(ctx, dir, "--output=/tmp/x") || !cgValidBranch(ctx, dir, "feature") || !cgValidBranch(ctx, dir, "HEAD") {
		t.Error("branch validation")
	}
}

// A page is cut from a layout computed from the start of the history, so the
// second page continues the first one's lanes.
func TestCommitGrokLogSecondPageKeepsLanes(t *testing.T) {
	dir := cgTestRepo(t)
	ctx := context.Background()
	all, _ := cgLogSized(ctx, dir, "main", 1, 10)
	p1, _ := cgLogSized(ctx, dir, "main", 1, 2)
	p2, err := cgLogSized(ctx, dir, "main", 2, 2)
	if err != nil || !p1.HasMore || p2.HasMore || len(p2.Items) != 2 {
		t.Fatalf("pages: %+v %+v %v", p1, p2, err)
	}
	got := append(append([]cgCommit{}, p1.Items...), p2.Items...)
	for i := range all.Items {
		if got[i].Sha != all.Items[i].Sha || got[i].Node != all.Items[i].Node || len(got[i].Top) != len(all.Items[i].Top) {
			t.Fatalf("row %d differs between paged and whole history: %+v vs %+v", i, got[i], all.Items[i])
		}
	}
}

func TestCommitGrokFetchCommit(t *testing.T) {
	dir := cgTestRepo(t)
	ctx := context.Background()
	log, _ := cgLog(ctx, dir, "main", 1)
	byMsg := map[string]cgCommit{}
	for _, c := range log.Items {
		byMsg[c.Subject] = c
	}
	// A merge is diffed against its first parent: it brings in the feature file.
	m, diff, err := cgFetchCommit(ctx, dir, byMsg["merge feature"].Sha)
	if err != nil || m.Title != "merge feature" || len(m.Parents) != 2 {
		t.Fatalf("merge meta = %+v, %v", m, err)
	}
	files := prgParseDiff(diff)
	if len(files) != 1 || files[0].Path != "f.txt" || files[0].Status != "added" {
		t.Fatalf("merge files = %+v", files)
	}
	// The root commit has no parent to diff against.
	_, diff, err = cgFetchCommit(ctx, dir, byMsg["root"].Sha[:10])
	if files := prgParseDiff(diff); err != nil || len(files) != 1 || files[0].Path != "a.txt" {
		t.Fatalf("root files = %+v, %v", files, err)
	}
	for _, bad := range []string{"", "HEAD", "zzzzzzz", "--help", "abc"} {
		if _, _, err := cgFetchCommit(ctx, dir, bad); err == nil {
			t.Errorf("sha %q accepted", bad)
		}
	}
}

func TestCommitGrokPathIgnoresViewAndHarness(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	sha := strings.Repeat("ab", 20)
	a := cgPath("o/r", sha, prgRequest{Ref: sha, Action: "explain"})
	if b := cgPath("o/r", sha, prgRequest{Ref: sha, Action: "explain", Refresh: true}); a != b {
		t.Errorf("path changed: %s vs %s", a, b)
	}
	if !strings.HasSuffix(a, filepath.Join("data", "commitgrok", "o_r", sha, "explain.json")) {
		t.Errorf("path = %s", a)
	}
	n3 := cgPath("o/r", sha, prgRequest{Action: "narrate", Source: "explain", Minutes: 3})
	n5 := cgPath("o/r", sha, prgRequest{Action: "narrate", Source: "explain", Minutes: 5})
	other := cgPath("o/r", strings.Repeat("cd", 20), prgRequest{Action: "explain"})
	if n3 == n5 || n3 == a || other == a || strings.Contains(a, "prgrok") {
		t.Errorf("keys do not separate: %s %s %s %s", a, n3, n5, other)
	}
}

// The commit subject feeds the shared engine: the prompt input carries the
// commit's identity and the same outline PRGrok builds.
func TestCommitGrokSubject(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := cgTestRepo(t)
	s := &Server{}
	log, _ := cgLog(context.Background(), dir, "main", 1)
	sha := log.Items[0].Sha
	subj, status, err := s.cgSubject(dir)(context.Background(), prgRequest{Ref: sha, Action: "explain"})
	if err != nil || status != 0 {
		t.Fatalf("subject: %d %v", status, err)
	}
	in := grokExplainInput(subj.Base, subj.Files)
	if in["kind"] != "commit" || in["ref"] != sha || in["title"] != "merge feature" || in["diff"] == "" {
		t.Fatalf("input = %+v", in)
	}
	if _, status, err := s.cgSubject(dir)(context.Background(), prgRequest{}); err == nil || status != 400 {
		t.Errorf("missing ref: %d %v", status, err)
	}
}
