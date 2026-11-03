// Package scene is a code mode for 3D scenes: the model writes JavaScript
// against a small scene builder (boxes, cylinders, spheres, nested groups,
// materials) and the result is a plain JSON node tree that a browser turns
// into three.js objects. No three.js runs in Go.
//
//	svc, _ := agent.New("builder").WithLLM(llm).WithCodeMode(scene.Scene3D()).Build()
//	out, _ := svc.RunCode(ctx, "a rack of 6×4 shelves with a box on each")
//	os.WriteFile("scene.json", out.Artifact, 0o644)
//
// Conventions: metres, y up, a mesh's position is its centre, rotations are
// in degrees and applied in X, Y, Z order (three.js "XYZ"). Groups carry a
// position, rotation and scale that apply to their children.
package scene

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/codemode"
)

//go:embed scene-api.js
var prelude string

// Scene is the artifact a scene program builds.
type Scene struct {
	Version   int                 `json:"version"`
	Units     string              `json:"units"`
	Up        string              `json:"up"`
	Materials map[string]Material `json:"materials"`
	Nodes     []*Node             `json:"nodes"`
}

// Material is a named or inline surface.
type Material struct {
	Color     string   `json:"color"`
	Metalness *float64 `json:"metalness,omitempty"`
	Roughness *float64 `json:"roughness,omitempty"`
	Opacity   *float64 `json:"opacity,omitempty"`
	Emissive  string   `json:"emissive,omitempty"`
}

// Node is one group or mesh. Material is either a material name (string) or
// an inline Material object.
type Node struct {
	Type     string             `json:"type"` // group, box, cylinder, sphere
	Name     string             `json:"name,omitempty"`
	Geometry map[string]float64 `json:"geometry,omitempty"`
	Position [3]float64         `json:"position"`
	Rotation *[3]float64        `json:"rotation,omitempty"` // degrees, XYZ order
	Scale    *[3]float64        `json:"scale,omitempty"`
	Material json.RawMessage    `json:"material,omitempty"`
	Children []*Node            `json:"children,omitempty"`
	Line     int                `json:"line,omitempty"`
}

// Option tunes the scene mode's limits and checks.
type Option func(*config)

type config struct {
	maxNodes         int
	overlapsAreError bool
	allowBelowGround bool
	maxSize          float64
	validators       []codemode.Validator
}

// MaxNodes caps how many nodes (groups and meshes) a program may create.
// Default 5000.
func MaxNodes(n int) Option { return func(c *config) { c.maxNodes = n } }

// NoOverlaps makes overlapping meshes an error instead of a warning.
func NoOverlaps() Option { return func(c *config) { c.overlapsAreError = true } }

// AllowBelowGround stops the warning for meshes that reach below y = 0.
func AllowBelowGround() Option { return func(c *config) { c.allowBelowGround = true } }

// MaxSize makes a scene larger than size metres along any axis an error.
func MaxSize(size float64) Option { return func(c *config) { c.maxSize = size } }

// WithValidator adds an application check after the built-in ones. Use
// Parse to read the artifact.
func WithValidator(v codemode.Validator) Option {
	return func(c *config) { c.validators = append(c.validators, v) }
}

// Scene3D returns the 3D scene code mode. The tool the model calls is
// "build_scene".
func Scene3D(opts ...Option) *codemode.Mode {
	cfg := config{maxNodes: 5000}
	for _, o := range opts {
		o(&cfg)
	}
	m := &codemode.Mode{
		Name:     "scene3d",
		Tool:     "build_scene",
		Summary:  "Build a 3D scene by writing a JavaScript program against a small scene builder. The result is a scene graph (JSON) that is rendered with three.js.",
		Docs:     docs,
		Example:  example,
		Prelude:  prelude,
		Export:   "__scene.export()",
		Globals:  map[string]any{"__sceneMaxNodes": cfg.maxNodes},
		Finalize: stripLines,
	}
	m.Validators = append(m.Validators, func(ctx context.Context, raw json.RawMessage) codemode.Check {
		return validate(raw, cfg)
	})
	m.Validators = append(m.Validators, cfg.validators...)
	return m
}

const docs = `Units are metres, y is up, the floor is y = 0. A mesh's position is its CENTRE,
so a box of height h standing on the floor has y = h/2. Rotations are in degrees, XYZ order.
Positions, rotations and scales are {x, y, z} (missing parts are 0) or [x, y, z].

scene.box({w, h, d}, position, opts)                 w along x, h along y, d along z
scene.cylinder({r, h} or {rTop, rBottom, h, segments}, position, opts)   axis along y
scene.sphere({r}, position, opts)
  opts: {name, color: '#rrggbb' | 0xRRGGBB, material: 'name', rotation, metalness, roughness, opacity}
scene.group(name, fn)                                 children added inside fn are grouped
scene.group(name, {position, rotation, scale}, fn)    the transform applies to everything inside fn
scene.material(name, {color, metalness, roughness, opacity, emissive}) -> name, for opts.material
scene.count() -> nodes created so far

Give meaningful names (they show up in the report). Each call returns {name, type}.
The report lists the overall bounding box, counts per type, per top-level group sizes,
and warns about meshes whose bounding boxes overlap or that sink below the floor.`

const example = `scene.material('steel', {color: '#8a9099', metalness: 0.6, roughness: 0.4});
for (let i = 0; i < 3; i++) {
  scene.group('table-' + i, {position: {x: i * 2}}, () => {
    scene.box({w: 1.2, h: 0.05, d: 0.6}, {y: 0.75}, {name: 'top', color: '#c8a27a'});
    for (const [x, z] of [[-0.55, -0.25], [0.55, -0.25], [-0.55, 0.25], [0.55, 0.25]]) {
      scene.cylinder({r: 0.025, h: 0.725}, {x, y: 0.3625, z}, {name: 'leg', material: 'steel'});
    }
  });
}`

// Parse reads a scene artifact.
func Parse(raw json.RawMessage) (*Scene, error) {
	var s Scene
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// MeshBox is one mesh with its world-space axis-aligned bounding box.
type MeshBox struct {
	Path string
	Type string
	Line int
	Min  [3]float64
	Max  [3]float64
}

func (b MeshBox) volume() float64 {
	return (b.Max[0] - b.Min[0]) * (b.Max[1] - b.Min[1]) * (b.Max[2] - b.Min[2])
}

// Meshes flattens the scene into world-space bounding boxes, applying every
// group's transform. Paths are slash-joined node names.
func (s *Scene) Meshes() []MeshBox {
	var out []MeshBox
	var walk func(nodes []*Node, parent mat4, prefix string)
	walk = func(nodes []*Node, parent mat4, prefix string) {
		for i, n := range nodes {
			if n == nil {
				continue
			}
			name := n.Name
			if name == "" {
				name = fmt.Sprintf("%s#%d", n.Type, i)
			}
			path := name
			if prefix != "" {
				path = prefix + "/" + name
			}
			world := parent.mul(localMatrix(n))
			if n.Type == "group" {
				walk(n.Children, world, path)
				continue
			}
			hx, hy, hz := halfExtents(n)
			box := MeshBox{Path: path, Type: n.Type, Line: n.Line,
				Min: [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)},
				Max: [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}}
			for _, sx := range []float64{-1, 1} {
				for _, sy := range []float64{-1, 1} {
					for _, sz := range []float64{-1, 1} {
						p := world.apply([3]float64{sx * hx, sy * hy, sz * hz})
						for k := 0; k < 3; k++ {
							box.Min[k] = math.Min(box.Min[k], p[k])
							box.Max[k] = math.Max(box.Max[k], p[k])
						}
					}
				}
			}
			out = append(out, box)
		}
	}
	walk(s.Nodes, identity(), "")
	return out
}

func halfExtents(n *Node) (float64, float64, float64) {
	g := n.Geometry
	switch n.Type {
	case "box":
		return g["w"] / 2, g["h"] / 2, g["d"] / 2
	case "cylinder":
		r := math.Max(g["rTop"], g["rBottom"])
		return r, g["h"] / 2, r
	case "sphere":
		return g["r"], g["r"], g["r"]
	}
	return 0, 0, 0
}

func validate(raw json.RawMessage, cfg config) codemode.Check {
	var c codemode.Check
	s, err := Parse(raw)
	if err != nil {
		c.Issues = append(c.Issues, codemode.Issue{Severity: codemode.SeverityError, Message: "scene JSON does not parse: " + err.Error()})
		return c
	}
	errorf := func(line int, node, format string, a ...any) {
		c.Issues = append(c.Issues, codemode.Issue{Severity: codemode.SeverityError, Line: line, Node: node, Message: fmt.Sprintf(format, a...)})
	}
	warnf := func(line int, node, format string, a ...any) {
		c.Issues = append(c.Issues, codemode.Issue{Severity: codemode.SeverityWarning, Line: line, Node: node, Message: fmt.Sprintf(format, a...)})
	}

	// Structure: counts, empty groups, unknown types, materials.
	counts := map[string]int{}
	groups, emptyGroups := 0, 0
	usedMaterials := map[string]bool{}
	var walk func(nodes []*Node)
	walk = func(nodes []*Node) {
		for _, n := range nodes {
			switch n.Type {
			case "group":
				groups++
				if len(n.Children) == 0 {
					emptyGroups++
					if emptyGroups <= 3 {
						warnf(n.Line, n.Name, "group is empty")
					}
				}
				walk(n.Children)
			case "box", "cylinder", "sphere":
				counts[n.Type]++
				if len(n.Material) > 0 && n.Material[0] == '"' {
					var name string
					_ = json.Unmarshal(n.Material, &name)
					usedMaterials[name] = true
				}
			default:
				errorf(n.Line, n.Name, "unknown node type %q", n.Type)
			}
		}
	}
	walk(s.Nodes)
	meshes := s.Meshes()
	if len(meshes) == 0 {
		errorf(0, "", "the scene has no meshes; call scene.box / scene.cylinder / scene.sphere")
		c.Stats = append(c.Stats, codemode.Stat{Key: "meshes", Value: "0"})
		return c
	}

	min := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	max := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, m := range meshes {
		for k := 0; k < 3; k++ {
			min[k] = math.Min(min[k], m.Min[k])
			max[k] = math.Max(max[k], m.Max[k])
		}
	}
	size := [3]float64{max[0] - min[0], max[1] - min[1], max[2] - min[2]}

	typeNames := make([]string, 0, len(counts))
	for t := range counts {
		typeNames = append(typeNames, t)
	}
	sort.Strings(typeNames)
	parts := make([]string, 0, len(typeNames))
	for _, t := range typeNames {
		parts = append(parts, fmt.Sprintf("%s %d", t, counts[t]))
	}
	c.Stats = append(c.Stats,
		codemode.Stat{Key: "meshes", Value: fmt.Sprintf("%d (%s)", len(meshes), strings.Join(parts, ", "))},
		codemode.Stat{Key: "groups", Value: fmt.Sprint(groups)},
		codemode.Stat{Key: "bounds", Value: fmt.Sprintf("min %s max %s, size %s m", fv(min), fv(max), fv(size))},
	)
	if len(s.Materials) > 0 {
		names := make([]string, 0, len(s.Materials))
		for n := range s.Materials {
			names = append(names, n)
		}
		sort.Strings(names)
		c.Stats = append(c.Stats, codemode.Stat{Key: "materials", Value: strings.Join(names, ", ")})
		for _, n := range names {
			if !usedMaterials[n] {
				warnf(0, n, "material is defined but never used")
			}
		}
	}

	// Top-level groups: how big each one is, so a model building "6 bays"
	// can see six of them and their sizes.
	// When everything sits in one wrapper group, look one level further in.
	for depth := 1; depth <= 3; depth++ {
		n, tops := groupStats(meshes, depth)
		if n == 0 {
			break
		}
		if n > 1 || depth == 3 {
			key := "top-level groups"
			if depth > 1 {
				key = fmt.Sprintf("groups at depth %d", depth)
			}
			c.Stats = append(c.Stats, codemode.Stat{Key: key, Value: tops})
			break
		}
		if depth == 1 {
			c.Stats = append(c.Stats, codemode.Stat{Key: "top-level groups", Value: tops})
		}
	}

	if cfg.maxSize > 0 {
		for k, axis := range []string{"x", "y", "z"} {
			if size[k] > cfg.maxSize {
				errorf(0, "", "the scene is %.3g m along %s; the limit is %.3g m", size[k], axis, cfg.maxSize)
			}
		}
	}

	if !cfg.allowBelowGround {
		below := 0
		for _, m := range meshes {
			if m.Min[1] < -1e-3 {
				below++
				if below <= 3 {
					warnf(m.Line, m.Path, "reaches below the floor (bottom at y = %.3g); remember position is the centre", m.Min[1])
				}
			}
		}
		if below > 3 {
			warnf(0, "", "%d meshes in all reach below the floor", below)
		}
	}

	pairs, sample := overlaps(meshes, 6)
	if pairs > 0 {
		sev := codemode.SeverityWarning
		if cfg.overlapsAreError {
			sev = codemode.SeverityError
		}
		for _, p := range sample {
			c.Issues = append(c.Issues, codemode.Issue{Severity: sev, Line: p.a.Line, Node: p.a.Path,
				Message: fmt.Sprintf("bounding box overlaps %q (line %d) by %s m", p.b.Path, p.b.Line, fv(p.depth))})
		}
		if pairs > len(sample) {
			c.Issues = append(c.Issues, codemode.Issue{Severity: sev, Message: fmt.Sprintf("%d overlapping pairs in all", pairs)})
		}
		c.Stats = append(c.Stats, codemode.Stat{Key: "overlapping pairs", Value: fmt.Sprint(pairs)})
	}
	return c
}

// groupStats summarises the groups at a path depth (1 = top level): how many
// there are and each one's mesh count and size.
func groupStats(meshes []MeshBox, depth int) (int, string) {
	type agg struct {
		n        int
		min, max [3]float64
	}
	order := []string{}
	byTop := map[string]*agg{}
	for _, m := range meshes {
		segs := strings.Split(m.Path, "/")
		if len(segs) <= depth {
			continue // a mesh at this level, not inside a group here
		}
		top := strings.Join(segs[:depth], "/")
		a := byTop[top]
		if a == nil {
			a = &agg{min: [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}, max: [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}}
			byTop[top] = a
			order = append(order, top)
		}
		a.n++
		for k := 0; k < 3; k++ {
			a.min[k] = math.Min(a.min[k], m.Min[k])
			a.max[k] = math.Max(a.max[k], m.Max[k])
		}
	}
	if len(order) == 0 {
		return 0, ""
	}
	var parts []string
	for i, name := range order {
		if i == 10 {
			parts = append(parts, fmt.Sprintf("… %d more", len(order)-10))
			break
		}
		a := byTop[name]
		parts = append(parts, fmt.Sprintf("%s: %d meshes, size %s", name, a.n,
			fv([3]float64{a.max[0] - a.min[0], a.max[1] - a.min[1], a.max[2] - a.min[2]})))
	}
	return len(order), fmt.Sprintf("%d; %s", len(order), strings.Join(parts, "; "))
}

type overlap struct {
	a, b  MeshBox
	depth [3]float64
}

// overlaps counts pairs of meshes whose bounding boxes interpenetrate by more
// than a millimetre on every axis (touching faces are fine), using a sweep
// along x. It returns the count and up to keep examples, deepest first.
func overlaps(meshes []MeshBox, keep int) (int, []overlap) {
	const eps = 1e-3
	idx := make([]int, len(meshes))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(i, j int) bool { return meshes[idx[i]].Min[0] < meshes[idx[j]].Min[0] })
	var found []overlap
	count := 0
	for ii := 0; ii < len(idx); ii++ {
		a := meshes[idx[ii]]
		for jj := ii + 1; jj < len(idx); jj++ {
			b := meshes[idx[jj]]
			if b.Min[0] >= a.Max[0]-eps {
				break
			}
			var d [3]float64
			ok := true
			for k := 0; k < 3; k++ {
				d[k] = math.Min(a.Max[k], b.Max[k]) - math.Max(a.Min[k], b.Min[k])
				if d[k] <= eps {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			count++
			if len(found) < 200 {
				found = append(found, overlap{a: a, b: b, depth: d})
			}
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		return found[i].depth[0]*found[i].depth[1]*found[i].depth[2] > found[j].depth[0]*found[j].depth[1]*found[j].depth[2]
	})
	if len(found) > keep {
		found = found[:keep]
	}
	return count, found
}

func fv(v [3]float64) string {
	return fmt.Sprintf("[%s, %s, %s]", fnum(v[0]), fnum(v[1]), fnum(v[2]))
}

func fnum(f float64) string {
	if math.Abs(f) < 5e-4 {
		return "0"
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", f), "0"), ".")
}

// stripLines removes the program line numbers the validator used; the
// artifact handed out is just the scene.
func stripLines(raw json.RawMessage) (json.RawMessage, error) {
	s, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	var strip func(nodes []*Node)
	strip = func(nodes []*Node) {
		for _, n := range nodes {
			n.Line = 0
			strip(n.Children)
		}
	}
	strip(s.Nodes)
	if s.Materials == nil {
		s.Materials = map[string]Material{}
	}
	return json.Marshal(s)
}
