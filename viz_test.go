package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestVizBuildsOncePerBranchAndRefreshes(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644)
	run("add", ".")
	run("commit", "-qm", "init")

	ix := NewIndex(root)
	ix.Build()
	s := NewServer(ix, nil)

	get := func(q string) vizResult {
		t.Helper()
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/viz?id=codecity"+q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var res vizResult
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	buildings := func(r vizResult) int {
		var c struct{ Stats struct{ Buildings int } }
		json.Unmarshal(r.Data, &c)
		return c.Stats.Buildings
	}

	first := get("")
	if first.Branch != "main" || buildings(first) != 1 || first.Stale {
		t.Fatalf("first = %+v", first)
	}

	// New file: cached model is served until a refresh is requested.
	os.WriteFile(filepath.Join(root, "b.go"), []byte("package a\n\nfunc B() {}\n"), 0o644)
	ix.Build()
	if n := buildings(get("")); n != 1 {
		t.Fatalf("cached buildings = %d, want 1", n)
	}
	if n := buildings(get("&refresh=1")); n != 2 {
		t.Fatalf("refreshed buildings = %d, want 2", n)
	}

	// A new commit marks the cached model stale; another branch gets its own.
	run("add", ".")
	run("commit", "-qm", "b")
	if !get("").Stale {
		t.Fatal("want stale after HEAD moved")
	}
	run("checkout", "-qb", "feature")
	if r := get(""); r.Branch != "feature" || r.Stale {
		t.Fatalf("feature = %+v", r)
	}

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/viz?id=nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown viz status %d", rec.Code)
	}
}
