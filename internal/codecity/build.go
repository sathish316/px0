package codecity

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxFileBytes    = 1_000_000
	defaultMaxFiles = 8000
)

// Options tunes a build. The zero value is the default (file buildings).
type Options struct {
	// Granularity is "file" (default) or "class".
	Granularity string
	MaxFiles    int
}

// Stats summarizes a built city.
type Stats struct {
	Files     int            `json:"files"`
	Buildings int            `json:"buildings"`
	Districts int            `json:"districts"`
	LOC       int            `json:"loc"`
	Languages map[string]int `json:"languages"`
}

// City is the complete, render-ready model.
type City struct {
	Version     int       `json:"version"`
	Name        string    `json:"name"`
	Branch      string    `json:"branch,omitempty"`
	Head        string    `json:"head,omitempty"`
	GeneratedAt time.Time `json:"generatedAt"`
	Stats       Stats     `json:"stats"`
	Root        *Node     `json:"root"`
	Landmarks   []Ref     `json:"landmarks"`
}

// Ref points at a node from a summary list.
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
	Role string `json:"role,omitempty"`
}

type raw struct {
	name, path, file, lang string
	m                      Metrics
}

var entryNames = map[string]bool{
	"main": true, "index": true, "app": true, "server": true, "cli": true, "program": true,
	"application": true, "mod": true, "lib": true, "init": true, "wsgi": true, "asgi": true,
}

func looksLikeEntry(rel string) bool {
	base := path.Base(rel)
	return entryNames[strings.ToLower(strings.TrimSuffix(base, path.Ext(base)))]
}

// Build scans the given workspace-relative files (slash separated) under root
// and returns the city. Non-source and oversized files are skipped, so callers
// can pass an index's full file list.
func Build(root string, files []string, opt Options) (*City, error) {
	if opt.MaxFiles <= 0 {
		opt.MaxFiles = defaultMaxFiles
	}
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)

	var raws []raw
	langs := map[string]int{}
	scanned := 0
	for _, rel := range sorted {
		lang := Language(rel)
		if lang == "" {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		st, err := os.Stat(abs)
		if err != nil || !st.Mode().IsRegular() || st.Size() == 0 || st.Size() > maxFileBytes {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		if scanned++; scanned > opt.MaxFiles {
			break
		}
		langs[lang]++
		fm := measure(string(data), lang)
		if opt.Granularity == "class" && len(fm.Spans) > 0 {
			for _, sp := range fm.Spans {
				raws = append(raws, raw{
					name: sp.Name, path: rel + "::" + sp.Name, file: rel, lang: lang,
					m: Metrics{LOC: sp.LOC, NCLOC: sp.LOC, NOM: sp.NOM, NOA: sp.NOA, Classes: 1, Complexity: sp.Complexity},
				})
			}
			continue
		}
		raws = append(raws, raw{
			name: path.Base(rel), path: rel, file: rel, lang: lang,
			m: Metrics{LOC: fm.LOC, NCLOC: fm.NCLOC, NOM: fm.NOM, NOA: fm.NOA, Classes: fm.Classes, Complexity: fm.Complexity},
		})
	}

	name := filepath.Base(root)
	rootNode := newDistrict(name, "", 0)
	city := &City{Version: 1, Name: name, GeneratedAt: time.Now().UTC(), Root: rootNode, Landmarks: []Ref{}}
	city.Stats = Stats{Files: scanned, Languages: langs}
	if len(raws) == 0 {
		layoutDistrict(rootNode)
		applyAbsolute(rootNode, 0, 0, 0)
		return city, nil
	}

	noms, noas, locs := make([]int, len(raws)), make([]int, len(raws)), make([]int, len(raws))
	locMax := 1
	for i, r := range raws {
		noms[i], noas[i], locs[i] = r.m.NOM, r.m.NOA, r.m.LOC
		locMax = max(locMax, r.m.LOC)
	}
	nomSorted, noaSorted, locSorted := sortedFloats(noms), sortedFloats(noas), sortedFloats(locs)
	nomP90, noaP90, locP75 := percentile(nomSorted, 0.9), percentile(noaSorted, 0.9), percentile(locSorted, 0.75)

	districts := map[string]*Node{}
	var buildings []*Node
	for _, r := range raws {
		side := footprintSide(r.m.NOA, r.m.LOC)
		b := &Node{
			ID: "b:" + r.path, Kind: "building", Name: r.name, Path: r.path, Language: r.lang,
			Width: side, Depth: side,
			Height:       boxplotHeight(float64(r.m.NOM), nomSorted),
			Color:        locColor(r.m.LOC, locMax),
			Metrics:      r.m,
			Disharmonies: classifyShape(r.m, nomP90, noaP90, locP75),
		}
		if looksLikeEntry(r.file) && strings.Count(r.file, "/") <= 3 {
			b.Landmark, b.Role = true, "entry-point"
		}
		parts := strings.Split(r.file, "/")
		parent := rootNode
		if len(parts) > 1 {
			parent = ensureDistrict(rootNode, parts[:len(parts)-1], districts)
		}
		parent.Children = append(parent.Children, b)
		buildings = append(buildings, b)
	}

	rollup(rootNode)
	layoutDistrict(rootNode)
	applyAbsolute(rootNode, 0, 0, 0)

	for _, b := range buildings {
		if b.Landmark && len(city.Landmarks) < 40 {
			city.Landmarks = append(city.Landmarks, Ref{ID: b.ID, Name: b.Name, Path: b.Path, Role: b.Role})
		}
	}
	city.Stats.Buildings = len(buildings)
	city.Stats.Districts = len(districts)
	city.Stats.LOC = rootNode.Metrics.LOC
	return city, nil
}

// classifyShape tags Wettel-style disharmony candidates. They are heuristics,
// not verdicts: generated code is a frequent false positive.
func classifyShape(m Metrics, nomP90, noaP90, locP75 float64) []string {
	nom, noa, loc := float64(m.NOM), float64(m.NOA), float64(m.LOC)
	var tags []string
	if nom >= max(nomP90, 12) && noa >= max(noaP90*0.6, 8) && loc >= locP75 {
		tags = append(tags, "god-class")
	}
	if nom >= max(nomP90, 15) && noa <= 3 {
		tags = append(tags, "antenna")
	}
	if noa >= max(noaP90, 10) && nom <= 3 {
		tags = append(tags, "data-class")
	}
	if loc >= max(locP75*1.5, 400) && nom >= 8 {
		tags = append(tags, "brain-class")
	}
	return tags
}
