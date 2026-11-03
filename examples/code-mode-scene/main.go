// Command code-mode-scene shows code mode: the model writes a JavaScript
// program against a small scene builder, the sandbox runs it, a validator
// measures what it built, and the report goes back to the model until the
// scene is right. The result is a scene graph as JSON plus a single-file
// three.js viewer.
//
// With a model (any OpenAI-compatible endpoint):
//
//	LLM_BASE_URL=... LLM_API_KEY=... LLM_MODEL=... go run ./examples/code-mode-scene
//	go run ./examples/code-mode-scene -goal "a playground with 3 swings and a slide"
//
// Without a model, running a built-in program through the same sandbox and
// validator:
//
//	go run ./examples/code-mode-scene -offline
//
// Output goes to -out (default: a folder in the temp dir): scene.json,
// program.js (the kept program, replayable with codemode.Run) and scene.html.
// -serve serves that folder on a local port so a browser can open it.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/codemode"
	"github.com/liliang-cn/agent-go/v3/pkg/codemode/scene"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

//go:embed viewer.html
var viewer string

const defaultGoal = `Build a warehouse pallet rack: 6 bays side by side along x, each bay 1.0 m wide and 0.8 m deep,
with 4 shelf levels (the lowest at 0.15 m, 0.5 m apart). Use steel-blue uprights at every bay boundary
(front and back), orange beams under each shelf, and put one cardboard box (0.5 × 0.3 × 0.5 m) centred on
every shelf. Nothing may float or sink into the floor, and boxes must sit exactly on their shelves.`

// offlineProgram is what a model might write for the default goal.
const offlineProgram = `const bays = 6, levels = 4, bayW = 1.0, depth = 0.8;
const first = 0.15, pitch = 0.5, post = 0.06, deck = 0.03, beam = 0.06;
scene.material('upright', {color: '#3d6fb6', metalness: 0.5, roughness: 0.45});
scene.material('beam', {color: '#e8772e', metalness: 0.4, roughness: 0.5});
scene.material('deck', {color: '#9aa3ad', metalness: 0.3, roughness: 0.6});
scene.material('carton', {color: '#c8a26b', roughness: 0.9});
const top = first + (levels - 1) * pitch + 0.25;
scene.group('rack', {position: {x: -bays * bayW / 2}}, () => {
  for (let b = 0; b <= bays; b++) {
    for (const z of [-depth / 2 + post / 2, depth / 2 - post / 2]) {
      scene.box({w: post, h: top, d: post}, {x: b * bayW, y: top / 2, z}, {name: 'upright-' + b + (z < 0 ? '-back' : '-front'), material: 'upright'});
    }
  }
  for (let b = 0; b < bays; b++) {
    scene.group('bay-' + (b + 1), {position: {x: b * bayW + bayW / 2}}, () => {
      for (let l = 0; l < levels; l++) {
        const y = first + l * pitch;               // top of the shelf deck
        const span = bayW - post;
        for (const z of [-depth / 2 + post / 2, depth / 2 - post / 2]) {
          scene.box({w: span, h: beam, d: post * 0.6}, {y: y - deck - beam / 2, z}, {name: 'beam-' + (l + 1), material: 'beam'});
        }
        scene.box({w: span, h: deck, d: depth - 2 * post}, {y: y - deck / 2}, {name: 'shelf-' + (l + 1), material: 'deck'});
        scene.box({w: 0.5, h: 0.3, d: 0.5}, {y: y + 0.15}, {name: 'box-' + (l + 1), material: 'carton'});
      }
    });
  }
});
console.log('nodes', scene.count());`

func main() {
	goal := flag.String("goal", defaultGoal, "what to build")
	offline := flag.Bool("offline", false, "run the built-in program instead of asking a model")
	outDir := flag.String("out", filepath.Join(os.TempDir(), "agentgo-code-mode-scene"), "output folder")
	attempts := flag.Int("attempts", 6, "most programs the model may submit")
	serve := flag.Bool("serve", false, "serve the output folder on a local port after building")
	port := flag.Int("port", 43781, "port for -serve")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	mode := scene.Scene3D(scene.MaxSize(60))
	var (
		artifact json.RawMessage
		program  string
		title    = "Scene"
	)
	if *offline {
		res := codemode.Run(ctx, mode, offlineProgram)
		fmt.Println(res.Report())
		if !res.OK() {
			log.Fatal("the built-in program failed")
		}
		artifact, program, title = res.Artifact, offlineProgram, "Pallet rack (offline)"
	} else {
		out, err := runWithModel(ctx, mode, *goal, *attempts)
		if err != nil {
			log.Fatal(err)
		}
		for _, a := range out.History {
			fmt.Printf("\n--- attempt %d (%d lines) ---\n%s\n", a.Attempt, strings.Count(a.Code, "\n")+1, a.Report())
		}
		fmt.Printf("\nkept attempt: %d of %d, passed: %v\nanswer: %s\n", attemptNo(out), out.Attempts, out.OK, strings.TrimSpace(out.Answer))
		if len(out.Artifact) == 0 {
			log.Fatal("no attempt produced a scene")
		}
		artifact, program, title = out.Artifact, out.Code, firstLine(*goal)
	}

	if err := writeOutput(*outDir, artifact, program, title); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\nwrote %s\n", filepath.Join(*outDir, "scene.html"))
	if *serve {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("serving http://%s/scene.html (ctrl-c to stop)\n", ln.Addr())
		log.Fatal(http.Serve(ln, http.FileServer(http.Dir(*outDir))))
	}
}

func runWithModel(ctx context.Context, mode *codemode.Mode, goal string, attempts int) (*agent.CodeOutcome, error) {
	baseURL, key, model := os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL")
	if baseURL == "" || model == "" {
		return nil, fmt.Errorf("set LLM_BASE_URL, LLM_API_KEY and LLM_MODEL, or pass -offline")
	}
	// Keep the example's agent state out of the user's own AgentGo home.
	home, err := os.MkdirTemp("", "agentgo-code-mode-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	os.Setenv("AGENTGO_HOME", home)

	llm, err := pool.NewClient("main", baseURL, key, model)
	if err != nil {
		return nil, err
	}
	svc, err := agent.New("scene-builder").
		WithLLM(llm).
		WithSystemPrompt("You build 3D scenes by writing programs for the build_scene tool. "+
			"Read each report carefully and check the measurements against the request before you stop.").
		WithObserver(agent.NewActivityLog(os.Stderr)).
		WithCodeMode(mode, agent.CodeModeAttempts(attempts)).
		Build()
	if err != nil {
		return nil, err
	}
	defer svc.Close()
	return svc.RunCode(ctx, goal)
}

func attemptNo(o *agent.CodeOutcome) int {
	if o.Best == nil {
		return 0
	}
	return o.Best.Attempt
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ":\n"); i > 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

func writeOutput(dir string, artifact json.RawMessage, program, title string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var pretty strings.Builder
	var v any
	if err := json.Unmarshal(artifact, &v); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	pretty.Write(b)
	if err := os.WriteFile(filepath.Join(dir, "scene.json"), []byte(pretty.String()+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "program.js"), []byte(program+"\n"), 0o644); err != nil {
		return err
	}
	t, _ := json.Marshal(title)
	html := strings.Replace(viewer, "/*SCENE_JSON*/null", scriptSafe(string(artifact)), 1)
	html = strings.Replace(html, `/*SCENE_TITLE*/"Scene"`, scriptSafe(string(t)), 1)
	return os.WriteFile(filepath.Join(dir, "scene.html"), []byte(html), 0o644)
}

// scriptSafe keeps JSON from closing the <script> it is inlined into.
func scriptSafe(s string) string {
	return strings.NewReplacer("</", `<\/`, " ", ` `, " ", ` `).Replace(s)
}
