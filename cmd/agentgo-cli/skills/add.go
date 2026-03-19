package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/liliang-cn/agent-go/cmd/agentgo-cli/rag"
	"github.com/spf13/cobra"
)

// addCmd adds a new skill
var addCmd = &cobra.Command{
	Use:   "add <skill-name>",
	Short: "Create a new skill",
	Long: `Create a new skill with a SKILL.md template.

The skill will be created in one of the configured skills directories.

Examples:
  # Create a new skill
  agentgo skills add my-skill

  # Create with custom path
  agentgo skills add my-skill --path ./custom-skills`,
	Args: cobra.ExactArgs(1),
	RunE: runAdd,
}

// removeCmd removes a skill
var removeCmd = &cobra.Command{
	Use:   "remove <skill-name>",
	Short: "Remove a skill",
	Long: `Remove a skill by deleting its directory.

Examples:
  agentgo skills remove my-skill`,
	Args: cobra.ExactArgs(1),
	RunE: runRemove,
}

var (
	skillPath string
	skillDesc string
)

func init() {
	Cmd.AddCommand(addCmd)
	Cmd.AddCommand(removeCmd)

	addCmd.Flags().StringVarP(&skillPath, "path", "p", "", "Custom path for the skill")
	addCmd.Flags().StringVarP(&skillDesc, "description", "D", "", "Skill description")
}

func runAdd(cmd *cobra.Command, args []string) error {
	skillName := args[0]

	// Sanitize skill name
	skillName = sanitizeSkillName(skillName)

	// Determine output directory
	outputDir := skillPath
	if outputDir == "" {
		// Use default skills paths
		paths := getDefaultSkillsPaths()
		for _, p := range paths {
			if isWritablePath(p) {
				outputDir = filepath.Join(p, skillName)
				break
			}
		}
		if outputDir == "" {
			// Fallback to .skills
			outputDir = filepath.Join(".skills", skillName)
		}
	} else {
		outputDir = filepath.Join(outputDir, skillName)
	}

	// Check if skill already exists
	if _, err := os.Stat(outputDir); err == nil {
		return fmt.Errorf("skill already exists at: %s", outputDir)
	}

	// Create skill directory
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create skill directory: %w", err)
	}

	// Create SKILL.md
	skillFile := filepath.Join(outputDir, "SKILL.md")
	description := skillDesc
	if description == "" {
		description = "A useful skill that helps with specific tasks"
	}

	skillContent := fmt.Sprintf("---\nname: %s\ndescription: \"%s\"\nuser-invocable: true\n---\n\n# %s\n\n%s\n\n## Usage\n\nThis skill is automatically invoked when relevant to the conversation.\n\n## Variables\n\n| Name | Type | Required | Description |\n|------|------|----------|-------------|\n|      |      |          |             |\n\n## Steps\n\n1.\n2.\n3.\n\n## Examples\n\n### Example 1\n\n**Input:**\n```\n\n```\n\n**Output:**\n```\n\n```\n", skillName, description, strings.Title(skillName),
		"Describe what this skill does and how it works.")

	if err := os.WriteFile(skillFile, []byte(skillContent), 0644); err != nil {
		return fmt.Errorf("failed to write SKILL.md: %w", err)
	}

	fmt.Printf("✅ Created skill: %s\n", skillName)
	fmt.Printf("   Path: %s\n", outputDir)
	fmt.Printf("   File: %s\n\n", skillFile)
	fmt.Printf("💡 Edit the skill file, then run: agentgo skills load\n")

	return nil
}

func runRemove(cmd *cobra.Command, args []string) error {
	skillName := args[0]

	// Find the skill directory
	skillDir, err := findSkillDirectory(skillName)
	if err != nil {
		return err
	}

	// Confirm removal
	fmt.Printf("Remove skill '%s' at: %s\n", skillName, skillDir)
	fmt.Print("Confirm? [y/N]: ")
	var confirm string
	fmt.Scanln(&confirm)
	if strings.ToLower(confirm) != "y" {
		fmt.Println("Cancelled")
		return nil
	}

	// Remove the directory
	if err := os.RemoveAll(skillDir); err != nil {
		return fmt.Errorf("failed to remove skill directory: %w", err)
	}

	fmt.Printf("✅ Removed skill: %s\n", skillName)

	return nil
}

// getDefaultSkillsPaths returns the default skills paths
func getDefaultSkillsPaths() []string {
	paths := []string{".skills"}

	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		paths = append(paths, filepath.Join(homeDir, ".agents", "skills"))
	}

	if cfg := rag.GetConfig(); cfg != nil {
		paths = append(paths, cfg.SkillsDir())
	} else {
		paths = append(paths, filepath.Join(".agentgo", "skills"))
		paths = append(paths, filepath.Join(homeDir, ".agentgo", "skills"))
	}

	return paths
}

// sanitizeSkillName sanitizes the skill name
func sanitizeSkillName(name string) string {
	// Convert to lowercase and replace spaces with hyphens
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ReplaceAll(name, "_", "-")

	// Remove any character that's not alphanumeric or hyphen
	var result strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}

	resultStr := result.String()
	if resultStr == "" {
		return "new-skill"
	}
	return resultStr
}

// isWritablePath checks if a path is writable
func isWritablePath(path string) bool {
	// Expand ~
	if strings.HasPrefix(path, "~") {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(homeDir, path[1:])
		}
	}

	// Check if directory exists or can be created
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Try to create
		if err := os.MkdirAll(path, 0755); err != nil {
			return false
		}
		// Remove it since we were just testing
		os.RemoveAll(path)
		return true
	}

	// Check if writable
	file, err := os.CreateTemp(path, ".agentgo-write-test")
	if err != nil {
		return false
	}
	file.Close()
	os.Remove(file.Name())
	return true
}

// findSkillDirectory finds the directory for a given skill name
func findSkillDirectory(skillName string) (string, error) {
	// Check common paths
	paths := []string{
		filepath.Join(".skills", skillName),
	}

	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		paths = append(paths, filepath.Join(homeDir, ".agents", "skills", skillName))
	}

	if cfg := rag.GetConfig(); cfg != nil {
		paths = append(paths, filepath.Join(cfg.SkillsDir(), skillName))
	} else {
		paths = append(paths, filepath.Join("./.agentgo/skills", skillName))
		paths = append(paths, filepath.Join(homeDir, ".agentgo/skills", skillName))
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("skill not found: %s (checked in %s)", skillName, strings.Join(paths, ", "))
}
