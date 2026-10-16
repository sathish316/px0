package codecity

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildGroupsDirectoriesIntoDistricts(t *testing.T) {
	root := t.TempDir()
	write(t, root, "main.go", "package main\n\nfunc main() {}\nfunc helper() {}\n")
	write(t, root, "pkg/util/strings.go", "package util\n\nfunc Trim(s string) string { return s }\n")
	write(t, root, "pkg/util/notes.txt", "not source")
	write(t, root, "web/app.js", "function boot() {}\nclass App { run() {} }\n")

	files := []string{"main.go", "pkg/util/strings.go", "pkg/util/notes.txt", "web/app.js"}
	city, err := Build(root, files, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if city.Stats.Buildings != 3 {
		t.Fatalf("buildings = %d, want 3 (txt skipped)", city.Stats.Buildings)
	}
	if city.Stats.Districts != 3 { // pkg, pkg/util, web
		t.Fatalf("districts = %d, want 3", city.Stats.Districts)
	}
	if city.Stats.Languages["go"] != 2 || city.Stats.Languages["javascript"] != 1 {
		t.Fatalf("languages = %v", city.Stats.Languages)
	}
	if city.Root.Metrics.Buildings != 3 || city.Root.Metrics.LOC == 0 {
		t.Fatalf("root rollup = %+v", city.Root.Metrics)
	}
	if len(city.Landmarks) != 2 { // main.go and app.js
		t.Fatalf("landmarks = %+v, want main.go and app.js", city.Landmarks)
	}
}

func TestBuildingsDoNotOverlapWithinADistrict(t *testing.T) {
	root := t.TempDir()
	var files []string
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		rel := "src/" + n + ".py"
		write(t, root, rel, "class X:\n    def __init__(self):\n        self.a = 1\n    def run(self):\n        pass\n")
		files = append(files, rel)
	}
	city, _ := Build(root, files, Options{})
	var src *Node
	for _, c := range city.Root.Children {
		src = c
	}
	for i, a := range src.Children {
		for _, b := range src.Children[i+1:] {
			if a.X < b.X+b.Width && b.X < a.X+a.Width && a.Z < b.Z+b.Depth && b.Z < a.Z+a.Depth {
				t.Fatalf("%s overlaps %s", a.Name, b.Name)
			}
		}
	}
}

func TestClassGranularity(t *testing.T) {
	root := t.TempDir()
	write(t, root, "m.py", "class A:\n    def f(self): pass\nclass B:\n    def g(self): pass\n")
	city, _ := Build(root, []string{"m.py"}, Options{Granularity: "class"})
	if city.Stats.Buildings != 2 {
		t.Fatalf("buildings = %d, want 2", city.Stats.Buildings)
	}
}

func TestEmptyRepo(t *testing.T) {
	city, err := Build(t.TempDir(), nil, Options{})
	if err != nil || city.Stats.Buildings != 0 {
		t.Fatalf("got %v, %v", city, err)
	}
}
