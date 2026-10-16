package codecity

import (
	"fmt"
	"math"
	"sort"
)

const (
	streetGap         = 1.4
	districtMargin    = 2.2
	platformThickness = 0.55
	platformStep      = 0.75
)

var heightBins = [5]float64{2.2, 5.5, 11.0, 22.0, 42.0}

// Metrics are the per-node measurements shown in the inspector. For districts
// they are rolled up from every descendant.
type Metrics struct {
	LOC        int `json:"loc"`
	NCLOC      int `json:"ncloc,omitempty"`
	NOM        int `json:"nom"`
	NOA        int `json:"noa"`
	Classes    int `json:"classes,omitempty"`
	Complexity int `json:"complexity,omitempty"`
	Files      int `json:"files,omitempty"`
	Buildings  int `json:"buildings,omitempty"`
}

// Node is a district (directory) or building (file or class) with absolute
// world coordinates ready for rendering.
type Node struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"` // "district" | "building"
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	X            float64  `json:"x"`
	Y            float64  `json:"y"`
	Z            float64  `json:"z"`
	Width        float64  `json:"width"`
	Depth        float64  `json:"depth"`
	Height       float64  `json:"height"`
	Color        string   `json:"color"`
	Metrics      Metrics  `json:"metrics"`
	Nesting      int      `json:"nesting,omitempty"`
	Language     string   `json:"language,omitempty"`
	ClassNames   []string `json:"classNames,omitempty"`
	Disharmonies []string `json:"disharmonies,omitempty"`
	Landmark     bool     `json:"landmark,omitempty"`
	Role         string   `json:"role,omitempty"`
	Children     []*Node  `json:"children,omitempty"`
}

func (n *Node) isDistrict() bool { return n.Kind == "district" }

func newDistrict(name, p string, nesting int) *Node {
	if p == "" {
		p = "."
	}
	return &Node{ID: "d:" + p, Kind: "district", Name: name, Path: p, Nesting: nesting}
}

// ensureDistrict returns the district for parts below root, creating the chain.
func ensureDistrict(root *Node, parts []string, index map[string]*Node) *Node {
	node, acc := root, ""
	for i, part := range parts {
		if acc != "" {
			acc += "/"
		}
		acc += part
		child := index[acc]
		if child == nil {
			child = newDistrict(part, acc, i+1)
			index[acc] = child
			node.Children = append(node.Children, child)
		}
		node = child
	}
	return node
}

func rollup(n *Node) {
	if !n.isDistrict() {
		return
	}
	for _, c := range n.Children {
		if c.isDistrict() {
			rollup(c)
			n.Metrics.LOC += c.Metrics.LOC
			n.Metrics.NOM += c.Metrics.NOM
			n.Metrics.NOA += c.Metrics.NOA
			n.Metrics.Files += c.Metrics.Files
			n.Metrics.Buildings += c.Metrics.Buildings
		} else {
			n.Metrics.LOC += c.Metrics.LOC
			n.Metrics.NOM += c.Metrics.NOM
			n.Metrics.NOA += c.Metrics.NOA
			n.Metrics.Files++
			n.Metrics.Buildings++
		}
	}
}

// packer is a binary-tree rectangle packer (Wettel-style modified treemap).
type packer struct{ root *packNode }

type packNode struct {
	x, z, w, d  float64
	used        bool
	right, down *packNode
}

func (p *packer) insert(w, d float64) (x, z float64, ok bool) {
	n := p.root.insert(w, d)
	if n == nil {
		return 0, 0, false
	}
	return n.x, n.z, true
}

func (n *packNode) insert(w, d float64) *packNode {
	if n == nil {
		return nil
	}
	if n.used {
		if r := n.right.insert(w, d); r != nil {
			return r
		}
		return n.down.insert(w, d)
	}
	if w <= n.w && d <= n.d {
		n.used = true
		n.right = &packNode{x: n.x + w, z: n.z, w: n.w - w, d: d}
		n.down = &packNode{x: n.x, z: n.z + d, w: n.w, d: n.d - d}
		return n
	}
	return nil
}

// packItems places items (relative x/z) and returns the bounding size.
func packItems(items []*Node) (float64, float64) {
	if len(items) == 0 {
		return districtMargin * 2, districtMargin * 2
	}
	sized := append([]*Node(nil), items...)
	sort.SliceStable(sized, func(i, j int) bool {
		return sized[i].Width*sized[i].Depth > sized[j].Width*sized[j].Depth
	})
	total := 0.0
	for _, it := range sized {
		total += (it.Width + streetGap) * (it.Depth + streetGap)
	}
	side := math.Max(8, math.Sqrt(total)*1.15)
	for range 12 {
		p := &packer{root: &packNode{w: side, d: side}}
		placed := true
		for _, it := range sized {
			x, z, ok := p.insert(it.Width+streetGap, it.Depth+streetGap)
			if !ok {
				placed = false
				break
			}
			it.X, it.Z = x, z
		}
		if placed {
			var mx, mz float64
			for _, it := range sized {
				mx = math.Max(mx, it.X+it.Width)
				mz = math.Max(mz, it.Z+it.Depth)
			}
			return mx + districtMargin, mz + districtMargin
		}
		side *= 1.25
	}
	// Fallback: simple row layout.
	x, z, rowH, maxX := districtMargin, districtMargin, 0.0, 0.0
	rowW := math.Sqrt(total) * 1.4
	for _, it := range sized {
		if x+it.Width > rowW {
			z += rowH + streetGap
			x, rowH = districtMargin, 0
		}
		it.X, it.Z = x, z
		x += it.Width + streetGap
		rowH = math.Max(rowH, it.Depth)
		maxX = math.Max(maxX, x)
	}
	return maxX + districtMargin, z + rowH + districtMargin
}

// layoutDistrict sizes d bottom-up; coordinates are relative to the parent.
func layoutDistrict(d *Node) {
	for _, c := range d.Children {
		if c.isDistrict() {
			layoutDistrict(c)
		}
	}
	packItems(d.Children)
	w, dp := districtMargin*2, districtMargin*2
	if len(d.Children) > 0 {
		w, dp = 0, 0
		for _, c := range d.Children {
			w = math.Max(w, c.X+c.Width)
			dp = math.Max(dp, c.Z+c.Depth)
		}
		w += districtMargin
		dp += districtMargin
	}
	d.Width, d.Depth = w, dp
	d.Height = platformThickness
	d.Color = districtColor(d.Nesting)
}

// applyAbsolute converts relative coordinates to world coordinates.
func applyAbsolute(n *Node, px, pz, py float64) {
	if n.isDistrict() {
		n.X += px + districtMargin*0.5
		n.Z += pz + districtMargin*0.5
		n.Y = float64(n.Nesting) * platformStep
		for _, c := range n.Children {
			applyAbsolute(c, n.X, n.Z, n.Y+platformThickness)
		}
		return
	}
	n.X += px
	n.Z += pz
	n.Y = py
}

func percentile(sorted []float64, p float64) float64 {
	switch len(sorted) {
	case 0:
		return 0
	case 1:
		return sorted[0]
	}
	k := float64(len(sorted)-1) * p
	f, c := math.Floor(k), math.Ceil(k)
	if f == c {
		return sorted[int(k)]
	}
	return sorted[int(f)]*(c-k) + sorted[int(c)]*(k-f)
}

func sortedFloats(v []int) []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = float64(x)
	}
	sort.Float64s(out)
	return out
}

// boxplotHeight bins value into five urban heights so one outlier cannot
// flatten the skyline.
func boxplotHeight(value float64, sortedSample []float64) float64 {
	edges := [4]float64{
		percentile(sortedSample, 0.20), percentile(sortedSample, 0.40),
		percentile(sortedSample, 0.70), percentile(sortedSample, 0.90),
	}
	for i, e := range edges {
		if value <= e {
			return heightBins[i]
		}
	}
	return heightBins[4]
}

func lerpColor(a, b [3]int, t float64) string {
	t = math.Max(0, math.Min(1, t))
	var c [3]int
	for i := range c {
		c[i] = int(float64(a[i]) + float64(b[i]-a[i])*t)
	}
	return fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2])
}

// locColor: dark gray (small) to intense blue (large), Wettel's default.
func locColor(loc, locMax int) string {
	t := 0.0
	if locMax > 0 {
		t = math.Sqrt(float64(loc) / float64(locMax))
	}
	return lerpColor([3]int{90, 90, 96}, [3]int{46, 110, 210}, t)
}

func districtColor(nesting int) string {
	return lerpColor([3]int{168, 206, 236}, [3]int{28, 72, 128}, math.Min(1, float64(nesting)/6))
}

func footprintSide(noa, loc int) float64 {
	if noa > 0 {
		return math.Max(1.4, math.Min(22, 1.3+math.Sqrt(float64(noa))*1.35))
	}
	return math.Max(1.4, math.Min(10, 1.3+math.Sqrt(float64(max(loc, 1)))*0.12))
}
