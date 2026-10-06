// Package main shows a host fanning work out to agents it reaches itself —
// workers over HTTP — without losing what the framework knows about
// sub-agents it runs itself.
//
// Three things a fan-out host needs, and where each comes from:
//
//   - ToolMetadata.OutputLimit: a fan-out tool's result is one report per
//     worker. Under the uniform cap the middle reports are cut and the model
//     is told to call again asking for less — which for work that took
//     minutes means doing it twice. The tool declares its own limit instead.
//   - Service.SubAgentBracket: each worker is announced to every observer as
//     a sub-agent of kind "remote", with the tokens it used, so ActivityLog,
//     the trace and a usage extension see ten workers rather than one opaque
//     tool call.
//   - WithoutMemoryAutoStore: on the worker side, an order from the host runs
//     without the automatic memory write, so N workers given the same order
//     do not write N extractions of it into a memory they share.
//
// The "workers" here are one in-process HTTP server that answers with a fixed
// text, so the example needs a model only for the host:
//
//	LLM_BASE_URL=https://api.deepseek.com/v1 LLM_API_KEY=$DEEPSEEK_API_KEY \
//	LLM_MODEL=deepseek-v4-flash go run ./examples/remote-workers
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
)

// workerReply is what a worker answers: the text, and the tokens it used, in
// the shape RemoteAgentRunResult wants. A real worker reports its own
// ExecutionResult's Usage here.
type workerReply struct {
	Text  string             `json:"text"`
	Usage *domain.TokenUsage `json:"usage,omitempty"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	baseURL, key, model := os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL")
	if baseURL == "" || model == "" {
		log.Fatal("set LLM_BASE_URL, LLM_API_KEY and LLM_MODEL")
	}
	home, err := os.MkdirTemp("", "agentgo-remote-workers-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(home)
	os.Setenv("AGENTGO_HOME", home)

	// The workers: one server standing in for three, each reporting usage.
	workers := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		name := r.URL.Query().Get("worker")
		time.Sleep(200 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(workerReply{
			Text:  fmt.Sprintf("%s here: I checked %q and found no problems.", name, strings.TrimSpace(string(body))),
			Usage: &domain.TokenUsage{PromptTokens: 900, CompletionTokens: 120},
		})
	}))
	defer workers.Close()

	llm, err := pool.NewClient("main", baseURL, key, model)
	if err != nil {
		log.Fatal(err)
	}
	svc, err := agent.New("queen").
		WithLLM(llm).
		WithObserver(agent.NewActivityLog(os.Stderr)). // the sub> / sub< lines below come from here
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	names := []string{"worker-0", "worker-1", "worker-2"}
	svc.AddToolWithMetadata("workers_run",
		"Give every worker the same order, in parallel, and collect one report per worker.",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"order": map[string]interface{}{"type": "string"}},
			"required":   []string{"order"},
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			order, _ := args["order"].(string)
			reports := make([]string, len(names))
			var wg sync.WaitGroup
			for i, name := range names {
				wg.Add(1)
				go func() {
					defer wg.Done()
					// Announce the worker; everything watching the run hears it.
					end := svc.SubAgentBracket(ctx, agent.SubAgentInfo{Name: name, Goal: order, Provider: "example-workers"})
					started := time.Now()
					reply, err := ask(ctx, workers.URL, name, order)
					res := agent.RemoteAgentRunResult{
						Agent: name, Provider: "example-workers", Endpoint: workers.URL,
						Duration: time.Since(started).Milliseconds(),
						Summary:  reply.Text, Usage: reply.Usage,
						Failed: err != nil,
					}
					if err != nil {
						res.Reason = err.Error()
						reports[i] = fmt.Sprintf("## %s\nFAILED: %v", name, err)
					} else {
						reports[i] = fmt.Sprintf("## %s\n%s", name, reply.Text)
					}
					end(res, err)
				}()
			}
			wg.Wait()
			return strings.Join(reports, "\n\n"), nil
		},
		// The result is one report per worker; the tool keeps them all and
		// pages nothing, so it takes the cap off for itself.
		agent.ToolMetadata{Destructive: true, OutputLimit: -1},
	)

	result, err := svc.Run(ctx,
		"Have every worker check the billing service's health, then tell me in one line per worker what each reported.",
		// This run is an order being carried out, not something to remember.
		agent.WithoutMemoryAutoStore(),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("\n" + strings.TrimSpace(result.Text()))
	fmt.Printf("\nthe queen's own tokens: %d; the workers' usage reached the observer separately\n",
		result.EstimatedTokens)
}

func ask(ctx context.Context, base, worker, order string) (workerReply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"?worker="+worker, strings.NewReader(order))
	if err != nil {
		return workerReply{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return workerReply{}, err
	}
	defer resp.Body.Close()
	var out workerReply
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return workerReply{}, err
	}
	return out, nil
}
