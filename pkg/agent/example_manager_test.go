package agent_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/extensiontest"
)

func ExampleManager_Tasks() {
	home, err := os.MkdirTemp("", "agentgo-manager-example")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(home)

	cfg := &config.Config{
		Home:   home,
		RAG:    config.RAGConfig{Enabled: false},
		Memory: config.MemoryConfig{StoreType: "file", MemoryPath: filepath.Join(home, "data", "memories")},
	}
	cfg.ApplyHomeLayout()

	store, err := agent.NewStore(filepath.Join(home, "data", "agentgo.db"))
	if err != nil {
		panic(err)
	}
	manager := agent.NewManager(store)
	defer manager.Close()

	manager.SetConfig(cfg)
	manager.SetLLM(extensiontest.Script(extensiontest.Answer("Filed under Q3.")))
	if err := manager.SeedDefaultAgent(); err != nil {
		panic(err)
	}

	task, err := manager.Tasks().Submit(context.Background(), agent.TaskSubmitOptions{
		AgentName: agent.DefaultAgentName,
		Input:     "file this under Q3",
	})
	if err != nil {
		panic(err)
	}

	done, err := manager.Tasks().Await(context.Background(), task.ID)
	if err != nil {
		panic(err)
	}
	fmt.Println(done.Status, done.Output)
	// Output: completed Filed under Q3.
}
