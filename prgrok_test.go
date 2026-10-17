package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const prgSampleDiff = `diff --git a/main.go b/main.go
index 111..222 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
-	println("hi")
+	fmt.Println("hi")
 }
diff --git a/new.txt b/new.txt
new file mode 100644
--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+a
+b
`

func TestPRGrokParseDiff(t *testing.T) {
	files := prgParseDiff(prgSampleDiff)
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
	f := files[0]
	if f.ID != "f0" || f.Path != "main.go" || f.Status != "modified" || f.Additions != 2 || f.Deletions != 1 {
		t.Fatalf("file 0 = %+v", f)
	}
	if len(f.Hunks) != 1 || f.Hunks[0].ID != "f0h0" {
		t.Fatalf("hunks = %+v", f.Hunks)
	}
	if got := f.Hunks[0].Lines[1]; got.T != "a" || got.N != 2 || got.X != `import "fmt"` {
		t.Fatalf("added line = %+v", got)
	}
	if got := f.Hunks[0].Lines[3]; got.T != "d" || got.O != 3 {
		t.Fatalf("deleted line = %+v", got)
	}
	if files[1].Status != "added" || files[1].ID != "f1" || files[1].Hunks[0].ID != "f1h0" {
		t.Fatalf("file 1 = %+v", files[1])
	}
	ann := prgAnnotate(files)
	for _, want := range []string{"FILE f0: main.go  [modified]", "HUNK f0h0: @@ -1,3 +1,4 @@", "     2 +import \"fmt\"", "FILE f1: new.txt  [added]"} {
		if !strings.Contains(ann, want) {
			t.Errorf("annotated diff missing %q:\n%s", want, ann)
		}
	}
}

func TestPRGrokExtractJSON(t *testing.T) {
	for _, in := range []string{`{"a":1}`, "text\n```json\n{\"a\":1}\n```\nmore", "noise {\"a\":1} tail"} {
		b, err := prgExtractJSON(in)
		if err != nil || !json.Valid(b) {
			t.Errorf("extract(%q) = %q, %v", in, b, err)
		}
	}
	if _, err := prgExtractJSON("no json here"); err == nil {
		t.Error("expected error without JSON")
	}
}

func TestPRGrokRepairsRawNewlinesInStrings(t *testing.T) {
	raw := "{\"overall\":\"line one\n\nline two\ttab\",\"files\":[]}"
	b, err := prgExtractJSON(raw)
	if err != nil || !json.Valid(b) {
		t.Fatalf("not repaired: %q %v", b, err)
	}
	var v struct{ Overall string }
	json.Unmarshal(b, &v)
	if v.Overall != "line one\n\nline two\ttab" {
		t.Errorf("overall = %q", v.Overall)
	}
	// Already-escaped content is left alone.
	if b, _ := prgExtractJSON("{\"a\":\"x\\ny \\\"q\\\"\"}"); !json.Valid(b) {
		t.Errorf("valid input was damaged: %s", b)
	}
}

func TestPRGrokNormalizeDropsUnknownIDs(t *testing.T) {
	files := prgParseDiff(prgSampleDiff)
	raw := `{"overall":"o","files":[
	  {"id":"f0","summary":"s","hunks":[{"id":"f0h0","summary":"h","elements":[{"line":2,"name":"x","summary":"e"},{"line":0,"summary":"bad"}]},{"id":"f0h9","summary":"ghost"}]},
	  {"id":"f7","summary":"ghost","hunks":[]}],
	 "diagrams":[{"type":"weird","title":"t","mermaid":"` + "```mermaid\\ngraph TD\\nA-->B\\n```" + `","explanation":"x"},{"type":"er","mermaid":"","explanation":"empty"}]}`
	res, err := prgNormalize("visual", []byte(raw), files)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 || len(res.Files[0].Hunks) != 1 {
		t.Fatalf("files = %+v", res.Files)
	}
	if len(res.Files[0].Hunks[0].Elements) != 0 {
		t.Error("visual results must not carry elements")
	}
	if len(res.Diagrams) != 1 || res.Diagrams[0].Type != "flowchart" || strings.Contains(res.Diagrams[0].Mermaid, "```") {
		t.Fatalf("diagrams = %+v", res.Diagrams)
	}
	ex, _ := prgNormalize("explain", []byte(raw), files)
	if got := ex.Files[0].Hunks[0].Elements; len(got) != 1 || got[0].ID != "f0h0e0" {
		t.Fatalf("elements = %+v", got)
	}
	if _, err := prgNormalize("explain", []byte(`{}`), files); err == nil {
		t.Error("empty reply should fail")
	}
}

func TestPRGrokNarrateFallsBackToSourceText(t *testing.T) {
	in := []prgSection{{ID: "overall", Kind: "overall", Label: "Overview", Text: "written"}, {ID: "f0", Kind: "file", Label: "main.go", Text: "file text"}}
	res, err := prgNormalizeNarrate([]byte(`{"sections":[{"id":"overall","text":"spoken"}]}`), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sections[0].Text != "spoken" || res.Sections[1].Text != "file text" {
		t.Fatalf("sections = %+v", res.Sections)
	}
}

func TestPRGrokRenderPrompt(t *testing.T) {
	for _, action := range []string{"explain", "visual", "narrate"} {
		p, err := prgRenderPrompt(action, map[string]any{"source": "explain", "k": "v"}, "/tmp/out.json")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p, "{{") {
			t.Errorf("%s: unreplaced placeholder in prompt", action)
		}
		if !strings.Contains(p, "/tmp/out.json") || !strings.Contains(p, `"k": "v"`) || !strings.Contains(p, "```json\n{") {
			t.Errorf("%s: output path or input missing", action)
		}
		for _, suffix := range []string{".input.schema.json", ".output.schema.json"} {
			b, err := prgReadPromptFile(action + suffix)
			if err != nil || !json.Valid([]byte(b)) {
				t.Errorf("%s%s invalid: %v", action, suffix, err)
			}
		}
	}
}

// The one-shot examples in the prompts must pass the same validation as real
// agent output, so a stale example cannot teach the agent a shape px0 rejects.
func TestPRGrokExamplesAreValid(t *testing.T) {
	for _, action := range []string{"explain", "visual"} {
		raw, err := prgReadPromptFile(action + ".example.json")
		if err != nil || !json.Valid([]byte(raw)) {
			t.Fatalf("%s example: %v", action, err)
		}
		var shape struct {
			Files []struct {
				ID    string
				Hunks []struct {
					ID       string
					Elements []any
				}
			}
			Diagrams []any
		}
		json.Unmarshal([]byte(raw), &shape)
		var files []prgFile
		wantHunks, wantElems := 0, 0
		for _, f := range shape.Files {
			pf := prgFile{ID: f.ID}
			for _, h := range f.Hunks {
				pf.Hunks = append(pf.Hunks, prgHunk{ID: h.ID})
				wantHunks++
				wantElems += len(h.Elements)
			}
			files = append(files, pf)
		}
		res, err := prgNormalize(action, []byte(raw), files)
		if err != nil {
			t.Fatalf("%s example rejected: %v", action, err)
		}
		gotHunks, gotElems := 0, 0
		for _, f := range res.Files {
			gotHunks += len(f.Hunks)
			for _, h := range f.Hunks {
				gotElems += len(h.Elements)
			}
		}
		if gotHunks != wantHunks || len(res.Files) != len(shape.Files) {
			t.Errorf("%s example lost files or hunks in validation", action)
		}
		if action == "explain" && gotElems != wantElems {
			t.Errorf("explain example lost element notes: %d of %d", gotElems, wantElems)
		}
		if action == "visual" && len(res.Diagrams) != len(shape.Diagrams) {
			t.Errorf("visual example lost diagrams: %d of %d", len(res.Diagrams), len(shape.Diagrams))
		}
	}
	raw, _ := prgReadPromptFile("narrate.example.json")
	in := []prgSection{{ID: "overall", Text: "a"}, {ID: "f0", Text: "b"}, {ID: "f0h1", Text: "c"}}
	res, err := prgNormalizeNarrate([]byte(raw), in)
	if err != nil || res.Sections[0].Text == "a" || res.Sections[2].Text == "c" {
		t.Errorf("narrate example not used as written: %+v %v", res, err)
	}
}

// Prompts come from the binary (or an absolute override), never from the
// directory px0 happens to be started in.
func TestPRGrokPromptsIgnoreWorkingDirectory(t *testing.T) {
	t.Setenv("PX0_PRGROK_PROMPTS", "")
	old, _ := os.Getwd()
	defer os.Chdir(old)
	other := t.TempDir()
	os.MkdirAll(filepath.Join(other, "prgrok", "prompts"), 0o755)
	os.WriteFile(filepath.Join(other, "prgrok", "prompts", "explain.prompt.md"), []byte("DECOY {{input_json}}"), 0o644)
	if err := os.Chdir(other); err != nil {
		t.Fatal(err)
	}
	p, err := prgRenderPrompt("explain", map[string]any{"k": "v"}, "/x/out.json")
	if err != nil || strings.Contains(p, "DECOY") || !strings.Contains(p, "/x/out.json") {
		t.Fatalf("prompt depended on the working directory: %v\n%s", err, p)
	}
	// A relative override is resolved once, to an absolute path.
	custom := t.TempDir()
	os.WriteFile(filepath.Join(custom, "explain.prompt.md"), []byte("CUSTOM {{output_path}}"), 0o644)
	rel, _ := filepath.Rel(other, custom)
	t.Setenv("PX0_PRGROK_PROMPTS", rel)
	if d := prgPromptDir(); !filepath.IsAbs(d) {
		t.Fatalf("override %q is not absolute", d)
	}
	if p, _ := prgRenderPrompt("explain", map[string]any{}, "/o.json"); !strings.HasPrefix(p, "CUSTOM /o.json") {
		t.Errorf("override not used: %q", p)
	}
}

func TestPRGrokPathIgnoresViewAndHarness(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	s := &Server{}
	r := prgRepo{Owner: "o", Repo: "r"}
	a := s.prgPath(r, "abcdef0123", prgRequest{Number: 3, Action: "explain"})
	if b := s.prgPath(r, "abcdef0123", prgRequest{Number: 3, Action: "explain", Refresh: true}); a != b {
		t.Errorf("path changed: %s vs %s", a, b)
	}
	n3 := s.prgPath(r, "abcdef0123", prgRequest{Number: 3, Action: "narrate", Source: "explain", Minutes: 3})
	n5 := s.prgPath(r, "abcdef0123", prgRequest{Number: 3, Action: "narrate", Source: "explain", Minutes: 5})
	if n3 == n5 || n3 == a {
		t.Errorf("narration paths should differ by length and action: %s %s %s", a, n3, n5)
	}
}

func TestPRGrokDetect(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := execGit(dir, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	cases := map[string]prgRepo{
		"git@github.com:acme/widgets.git":         {Provider: "github", Host: "github.com", Owner: "acme", Repo: "widgets"},
		"https://bitbucket.org/ws/repo.git":       {Provider: "bitbucket", Host: "bitbucket.org", Owner: "ws", Repo: "repo"},
		"git@bitbucket.org:ws/repo.git":           {Provider: "bitbucket", Host: "bitbucket.org", Owner: "ws", Repo: "repo"},
		"https://gitlab.example.com/grp/proj.git": {Host: "gitlab.example.com", Owner: "grp", Repo: "proj"},
	}
	for url, want := range cases {
		execGit(dir, "remote", "remove", "origin") // absent on the first pass
		run("remote", "add", "origin", url)
		got := prgDetect(dir)
		got.Reason = ""
		if got != want {
			t.Errorf("%s: got %+v want %+v", url, got, want)
		}
	}
}

func TestPRGrokDetectFallsBackToAnyRemote(t *testing.T) {
	dir := t.TempDir()
	execGit(dir, "init", "-q")
	execGit(dir, "remote", "add", "fork", "https://github.com/me/proj.git")
	if got := prgDetect(dir); got.Provider != "github" || got.Owner != "me" || got.Repo != "proj" {
		t.Fatalf("only-remote-is-fork: %+v", got)
	}
	execGit(dir, "remote", "add", "upstream", "https://github.com/org/proj.git")
	if got := prgDetect(dir); got.Owner != "org" {
		t.Errorf("upstream should win over a fork remote: %+v", got)
	}
	execGit(dir, "remote", "add", "origin", "https://github.com/me2/proj.git")
	if got := prgDetect(dir); got.Owner != "me2" {
		t.Errorf("origin should win when present: %+v", got)
	}
}

func execGit(dir string, args ...string) (string, error) {
	out, err := prgExec(context.Background(), dir, "git", args...)
	return string(out), err
}

func TestPRGrokListGitHub(t *testing.T) {
	orig := prgExec
	defer func() { prgExec = orig }()
	var gotArgs []string
	prgExec = func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte("HTTP/2.0 200 OK\r\nLink: <https://api.github.com/x?page=2>; rel=\"next\"\r\n\r\n" +
			`[{"number":7,"title":"T","user":{"login":"me"},"state":"open","head":{"ref":"feat"},"base":{"ref":"main"},"html_url":"u","updated_at":"now","draft":true}]`), nil
	}
	l, err := prgListPRs(context.Background(), ".", prgRepo{Provider: "github", Owner: "o", Repo: "r"}, 1, "open")
	if err != nil {
		t.Fatal(err)
	}
	if !l.HasMore || len(l.Items) != 1 || l.Items[0].Number != 7 || l.Items[0].Author != "me" || !l.Items[0].Draft || l.Items[0].Source != "feat" {
		t.Fatalf("list = %+v", l)
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "per_page=10") {
		t.Errorf("page size not requested: %v", gotArgs)
	}
}

func TestPRGrokListBitbucketShapes(t *testing.T) {
	orig := prgExec
	defer func() { prgExec = orig }()
	for _, out := range []string{
		`{"values":[{"id":3,"title":"T","state":"OPEN","author":{"display_name":"A"},"source":{"branch":{"name":"s"}},"destination":{"branch":{"name":"d"}},"links":{"html":{"href":"u"}}}],"hasNextPage":true}`,
		`{"ok":true,"data":{"values":[{"id":3,"title":"T","state":"OPEN","author":{"display_name":"A"},"source":{"branch":{"name":"s"}},"destination":{"branch":{"name":"d"}},"links":{"html":{"href":"u"}}}],"hasMore":true}}`,
		`[{"id":3,"title":"T","state":"OPEN","author":{"display_name":"A"},"source":{"branch":{"name":"s"}},"destination":{"branch":{"name":"d"}},"links":{"html":{"href":"u"}}}]`,
	} {
		prgExec = func(context.Context, string, string, ...string) ([]byte, error) { return []byte(out), nil }
		l, err := prgListPRs(context.Background(), ".", prgRepo{Provider: "bitbucket", Owner: "w", Repo: "r"}, 1, "open")
		if err != nil || len(l.Items) != 1 || l.Items[0].Number != 3 || l.Items[0].Author != "A" || l.Items[0].Dest != "d" || l.Items[0].State != "open" {
			t.Fatalf("out=%s list=%+v err=%v", out, l, err)
		}
	}
	prgExec = func(context.Context, string, string, ...string) ([]byte, error) {
		return []byte(`{"ok":false,"error":{"code":"AUTH_INVALID","message":"Authentication failed."}}`), nil
	}
	if _, err := prgListPRs(context.Background(), ".", prgRepo{Provider: "bitbucket"}, 1, "open"); err == nil || !strings.Contains(err.Error(), "Authentication failed") {
		t.Errorf("auth error not surfaced: %v", err)
	}
}

// The agent writes its JSON into the project; prgFinish validates it, stores
// the cleaned result in the cache and removes the project copy. A second
// request for the same file reads that cache.
func TestPRGrokFinishUsesOutputFile(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	s := &Server{}
	q := prgRequest{Number: 5, Action: "explain"}
	final := s.prgPath(prgRepo{Owner: "o", Repo: "r"}, "abcdef0123456789", q)
	if !strings.Contains(final, filepath.Join("data", "prgrok", "o_r", "pr-5", "abcdef01", "explain.json")) {
		t.Fatalf("unexpected cache path %s", final)
	}
	files := prgParseDiff(prgSampleDiff)
	newJob := func(content string) *prgJob {
		pending := prgOutputPath(root, q)
		os.MkdirAll(filepath.Dir(pending), 0o755)
		if content != "" {
			os.WriteFile(pending, []byte(content), 0o644)
		}
		return &prgJob{req: q, files: files, cache: final, pending: pending}
	}
	pj := newJob(`{"overall":"o","files":[{"id":"f0","summary":"s","hunks":[{"id":"f0h0","summary":"h"}]}]}`)
	res, errMsg := s.prgFinish(pj, prgOutcome{Harness: "claude", Stdout: "done"})
	if errMsg != "" || res == nil || res.Overall != "o" {
		t.Fatalf("finish = %+v, %q", res, errMsg)
	}
	if _, err := os.Stat(pj.pending); !os.IsNotExist(err) {
		t.Error("the agent's file should be removed from the project")
	}
	if _, err := os.Stat(filepath.Join(root, prgWorkDirName)); !os.IsNotExist(err) {
		t.Error("the empty work dir should be removed")
	}
	if cached := prgCacheRead(final); cached == nil || cached.Overall != "o" || cached.Harness != "claude" {
		t.Fatalf("cache = %+v", cached)
	}
	// No file and no JSON on stdout is a clear failure.
	if _, errMsg := s.prgFinish(newJob(""), prgOutcome{Stdout: "done"}); !strings.Contains(errMsg, "wrote no output file") {
		t.Errorf("errMsg = %q", errMsg)
	}
	// A file that is not valid output fails and is still cleaned up.
	bad := newJob(`{"overall":"","files":[]}`)
	if _, errMsg := s.prgFinish(bad, prgOutcome{}); errMsg == "" {
		t.Error("empty reply should fail")
	}
	if _, err := os.Stat(bad.pending); !os.IsNotExist(err) {
		t.Error("a rejected file should be removed too")
	}
	// Stdout JSON is the fallback when the agent could not write the file.
	if res, errMsg := s.prgFinish(newJob(""), prgOutcome{Stdout: `{"overall":"x","files":[]}`}); errMsg != "" || res.Overall != "x" {
		t.Errorf("fallback = %+v %q", res, errMsg)
	}
}
