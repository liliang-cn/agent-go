package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

func main() {
	ctx := context.Background()

	homeDir, _ := os.UserHomeDir()
	agentDBPath := filepath.Join(homeDir, ".agentgo", "data", "skills_integration.db")
	os.MkdirAll(filepath.Dir(agentDBPath), 0755)

	// Get absolute path to skills directory
	// Try ~/.agents/skills first, fallback to examples/.skills
	skillsPath := filepath.Join(homeDir, ".agents", "skills")
	if _, err := os.Stat(skillsPath); os.IsNotExist(err) {
		cwd, _ := os.Getwd()
		skillsPath = filepath.Join(cwd, "examples", ".skills")
	}

	svc, err := agent.New("skills-integration-agent").
		WithDBPath(agentDBPath).
		WithSkills(agent.WithSkillsPaths(skillsPath)).
		Build()
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}
	defer svc.Close()

	fmt.Println("--- Skills Integration Demo ---")
	if svc.Skills != nil {
		skillsList, _ := svc.Skills.ListSkills(ctx, skills.SkillFilter{})
		fmt.Printf("Found %d skills:\n", len(skillsList))
		for _, sk := range skillsList {
			fmt.Printf("- %s: %s\n", sk.ID, sk.Description)
		}
	}
	fmt.Println()

	fmt.Println("--- Running Goal: What can you help me with? ---")
	res, err := svc.Run(ctx, "What skills do you have available?")
	if err != nil {
		log.Fatalf("Run failed: %v", err)
	}

	fmt.Printf("\nFinal Result: %s\n", res.Text())

	fmt.Println("\nSkills integration example completed successfully!")
}
