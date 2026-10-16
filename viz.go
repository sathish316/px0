package main

// Visualizations: alternative views of the checked-out workspace that replace
// the editor in the center pane. Each visualizer is a Go-side data provider
// registered in visualizers; the browser side has a matching renderer in
// web/src/viz.js. Providers are deterministic and need no LLM or agent.
//
// Results are always for the currently checked-out branch. They are built the
// first time a visualizer is opened on a branch, cached under the user cache
// directory (never inside the workspace), and rebuilt only on request.

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"px0/internal/codecity"
)

// visualizer describes one entry in the visualization menu.
type visualizer struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// build produces the JSON-serializable model the browser renders.
	build func(s *Server, branch, head string) (any, error)
}

var visualizers = []visualizer{
	{
		ID:          "codecity",
		Name:        "CodeCity",
		Description: "Directories as districts, files as buildings. Height = methods, footprint = fields, color = lines of code.",
		build:       buildCodeCity,
	},
}

func findVisualizer(id string) *visualizer {
	for i := range visualizers {
		if visualizers[i].ID == id {
			return &visualizers[i]
		}
	}
	return nil
}

func buildCodeCity(s *Server, branch, head string) (any, error) {
	files := s.ix.Files()
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	city, err := codecity.Build(s.ix.Root(), paths, codecity.Options{})
	if err != nil {
		return nil, err
	}
	city.Branch, city.Head = branch, head
	return city, nil
}

// vizResult is the envelope returned for one visualizer.
type vizResult struct {
	Viz    string `json:"viz"`
	Branch string `json:"branch"`
	Head   string `json:"head"`
	// Stale is true when HEAD has moved since the cached model was built;
	// uncommitted edits are not detected, so the UI offers Refresh anyway.
	Stale bool            `json:"stale"`
	Data  json.RawMessage `json:"data"`
}

type vizCache struct {
	mu sync.Mutex
}

func vizCacheDir() string {
	return filepath.Join(filepath.Dir(vendorCacheDir()), "viz")
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func vizCachePath(root, id, branch string) string {
	sum := sha1.Sum([]byte(root))
	name := id + "-" + hex.EncodeToString(sum[:6]) + "-" + unsafeFileChars.ReplaceAllString(branch, "_") + ".json"
	return filepath.Join(vizCacheDir(), name)
}

// loadViz returns the cached result for the checked-out branch, building it
// when absent or when refresh is set.
func (s *Server) loadViz(v *visualizer, refresh bool) (*vizResult, error) {
	s.viz.mu.Lock()
	defer s.viz.mu.Unlock()

	root := s.ix.Root()
	branch := gitCurrentBranch(root)
	head := gitHeadCommit(root)
	cachePath := vizCachePath(root, v.ID, branch)

	if !refresh {
		if b, err := os.ReadFile(cachePath); err == nil {
			var res vizResult
			if json.Unmarshal(b, &res) == nil && res.Branch == branch {
				res.Stale = res.Head != head
				return &res, nil
			}
		}
	}

	data, err := v.build(s, branch, head)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	res := &vizResult{Viz: v.ID, Branch: branch, Head: head, Data: raw}
	if b, err := json.Marshal(res); err == nil && os.MkdirAll(vizCacheDir(), 0o755) == nil {
		tmp := cachePath + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, cachePath)
		}
	}
	return res, nil
}

func (s *Server) handleVizList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"visualizers": visualizers})
}

func (s *Server) handleViz(w http.ResponseWriter, r *http.Request) {
	v := findVisualizer(r.URL.Query().Get("id"))
	if v == nil {
		fail(w, http.StatusNotFound, "unknown visualization")
		return
	}
	if !s.ix.Ready() {
		if err := s.ix.WaitReady(r.Context()); err != nil {
			fail(w, http.StatusServiceUnavailable, "workspace is still indexing")
			return
		}
	}
	res, err := s.loadViz(v, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, res)
}
