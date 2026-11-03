package scene

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/codemode"
)

// RackProgram builds a rack of 6 bays × 4 shelves with a box on every shelf.
const rackProgram = `
const bays = 6, levels = 4;
const bayW = 1.0, depth = 0.6, gap = 0.45, post = 0.05, plank = 0.03;
scene.material('steel', {color: '#3d6fb6', metalness: 0.5, roughness: 0.5});
scene.material('carton', {color: '#c8a26b'});
const width = bays * bayW;
scene.group('rack', {position: {x: -width / 2}}, () => {
  for (let b = 0; b <= bays; b++) {
    for (const z of [-depth / 2 + post / 2, depth / 2 - post / 2]) {
      scene.box({w: post, h: levels * gap + 0.1, d: post}, {x: b * bayW, y: (levels * gap + 0.1) / 2, z}, {name: 'post-' + b, material: 'steel'});
    }
  }
  for (let b = 0; b < bays; b++) {
    for (let l = 0; l < levels; l++) {
      const y = 0.1 + l * gap;
      const cx = b * bayW + bayW / 2;
      scene.box({w: bayW - post, h: plank, d: depth}, {x: cx, y}, {name: 'shelf-' + b + '-' + l, material: 'steel'});
      const h = 0.25;
      scene.box({w: 0.4, h, d: 0.35}, {x: cx, y: y + plank / 2 + h / 2, z: 0}, {name: 'box-' + b + '-' + l, material: 'carton'});
    }
  }
});
`

func TestRackBuildsExpectedTree(t *testing.T) {
	res := codemode.Run(context.Background(), Scene3D(), rackProgram)
	if !res.OK() {
		t.Fatalf("rack failed:\n%s", res.Report())
	}
	s, err := Parse(res.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Nodes) != 1 || s.Nodes[0].Type != "group" || s.Nodes[0].Name != "rack" {
		t.Fatalf("want one rack group, got %+v", s.Nodes)
	}
	boxes := 0
	for _, m := range s.Meshes() {
		if strings.HasPrefix(m.Path, "rack/box-") {
			boxes++
		}
	}
	if boxes != 24 {
		t.Fatalf("want 24 cartons, got %d", boxes)
	}
	// The line numbers the validator used are stripped from the artifact.
	if strings.Contains(string(res.Artifact), `"line"`) {
		t.Fatal("artifact still carries line numbers")
	}
	if _, ok := s.Materials["steel"]; !ok {
		t.Fatal("steel material missing")
	}
	// The group's -width/2 offset moves the whole rack to be centred on x.
	r := res.Report()
	if !strings.Contains(r, "size [6.05,") {
		t.Fatalf("report should carry the rack's size:\n%s", r)
	}
	if strings.Contains(r, "overlap") {
		t.Fatalf("shelves and cartons only touch, no overlap expected:\n%s", r)
	}
}

func TestGroupTransformsApplyToChildren(t *testing.T) {
	prog := `scene.group('g', {position: {x: 10, y: 1}, rotation: {y: 90}, scale: 2}, () => {
  scene.box({w: 1, h: 1, d: 0.5}, {x: 1}, {name: 'b'});
});`
	res := codemode.Run(context.Background(), Scene3D(), prog)
	if !res.OK() {
		t.Fatal(res.Report())
	}
	s, _ := Parse(res.Artifact)
	m := s.Meshes()[0]
	// local x=1 scaled to 2, rotated 90° about y → z = -2; size scaled 2 and swapped.
	want := MeshBox{Min: [3]float64{9.5, 0, -3}, Max: [3]float64{10.5, 2, -1}}
	for k := 0; k < 3; k++ {
		if abs(m.Min[k]-want.Min[k]) > 1e-9 || abs(m.Max[k]-want.Max[k]) > 1e-9 {
			t.Fatalf("world box = %v..%v, want %v..%v", m.Min, m.Max, want.Min, want.Max)
		}
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestBuilderRejectsBadArgumentsAtTheProgramLine(t *testing.T) {
	prog := "scene.box({w: 1, h: 1, d: 1}, {y: 0.5});\nscene.box({w: -1, h: 1, d: 1}, {y: 0.5});\n"
	res := codemode.Run(context.Background(), Scene3D(), prog)
	if res.OK() || res.Error == nil {
		t.Fatal("negative width must fail")
	}
	if res.Error.Line != 2 || !strings.Contains(res.Error.Message, "w must be greater than 0") {
		t.Fatalf("want line 2 width error, got %+v", res.Error)
	}
	rep := res.Report()
	if !strings.Contains(rep, "line 2") || !strings.Contains(rep, "> scene.box({w: -1") {
		t.Fatalf("report should point at line 2 and quote it:\n%s", rep)
	}
}

func TestUnknownOptionAndMaterialAreCaught(t *testing.T) {
	res := codemode.Run(context.Background(), Scene3D(), `scene.box({w:1,h:1,d:1}, {y:0.5}, {colour: '#fff'})`)
	if res.Error == nil || !strings.Contains(res.Error.Message, "unknown option 'colour'") {
		t.Fatalf("got %+v", res.Error)
	}
	res = codemode.Run(context.Background(), Scene3D(), `scene.box({w:1,h:1,d:1}, {y:0.5}, {material: 'wood'})`)
	if res.Error == nil || !strings.Contains(res.Error.Message, "unknown material") {
		t.Fatalf("got %+v", res.Error)
	}
}

func TestValidatorFindsOverlapsAndFloor(t *testing.T) {
	prog := `scene.box({w: 1, h: 1, d: 1}, {y: 0.5}, {name: 'a'});
scene.box({w: 1, h: 1, d: 1}, {x: 0.5, y: 0.5}, {name: 'b'});
scene.sphere({r: 0.5}, {x: 5}, {name: 'sunk'});`
	res := codemode.Run(context.Background(), Scene3D(), prog)
	if !res.OK() {
		t.Fatalf("overlaps are warnings by default:\n%s", res.Report())
	}
	rep := res.Report()
	for _, want := range []string{`overlaps "b"`, `"sunk": reaches below the floor`, "line 3", "PASSED with 2 warning(s)"} {
		if !strings.Contains(rep, want) && !strings.Contains(rep, strings.Replace(want, `"b"`, `"a"`, 1)) {
			t.Fatalf("report missing %q:\n%s", want, rep)
		}
	}

	strict := codemode.Run(context.Background(), Scene3D(NoOverlaps()), prog)
	if strict.OK() {
		t.Fatal("NoOverlaps should fail the attempt")
	}
}

func TestEmptySceneIsAnError(t *testing.T) {
	res := codemode.Run(context.Background(), Scene3D(), `console.log('nothing to build')`)
	if res.OK() {
		t.Fatal("empty scene must fail")
	}
	rep := res.Report()
	if !strings.Contains(rep, "no meshes") || !strings.Contains(rep, "nothing to build") {
		t.Fatalf("report:\n%s", rep)
	}
}

func TestNodeLimit(t *testing.T) {
	res := codemode.Run(context.Background(), Scene3D(MaxNodes(10)), `for (let i = 0; i < 100; i++) scene.sphere({r: 0.1}, {x: i, y: 0.1});`)
	if res.Error == nil || !strings.Contains(res.Error.Message, "node limit") || res.Error.Line != 1 {
		t.Fatalf("got %+v", res.Error)
	}
}

func TestAppValidator(t *testing.T) {
	needSix := WithValidator(func(_ context.Context, raw json.RawMessage) codemode.Check {
		s, err := Parse(raw)
		if err != nil || len(s.Nodes) == 6 {
			return codemode.Check{}
		}
		return codemode.Check{Issues: []codemode.Issue{{Severity: codemode.SeverityError, Message: "want 6 top-level bays"}}}
	})
	res := codemode.Run(context.Background(), Scene3D(needSix), `scene.box({w:1,h:1,d:1},{y:0.5})`)
	if res.OK() || !strings.Contains(res.Report(), "want 6 top-level bays") {
		t.Fatalf("app validator ignored:\n%s", res.Report())
	}
}
