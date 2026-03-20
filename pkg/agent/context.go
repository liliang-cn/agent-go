package agent

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/skills"
)

// SystemContext 系统上下文信息
type SystemContext struct {
	Date       string
	Time       string
	Timezone   string
	OS         string
	Arch       string
	Hostname   string
	WorkingDir string
	HomeDir    string
	User       string
	GoVersion  string
	EnvInfo    map[string]string // selected env vars
	HasMemory  bool              // memory system is enabled
	MCPServers []string          // available MCP server names (e.g. mcp_filesystem)
	SkillNames []string          // available skill IDs
}

// buildSystemContext 收集系统上下文信息
func (s *Service) buildSystemContext() *SystemContext {
	bgCtx := context.Background()
	now := time.Now()
	hostname, _ := os.Hostname()
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("USERNAME")
	}
	if user == "" {
		user = os.Getenv("LOGNAME")
	}

	// Collect selected useful env vars
	envInfo := make(map[string]string)
	usefulEnvKeys := []string{"SHELL", "PATH", "LANG", "TERM", "EDITOR"}
	for _, key := range usefulEnvKeys {
		if val := os.Getenv(key); val != "" {
			// Truncate long values like PATH
			if len(val) > 100 {
				val = val[:97] + "..."
			}
			envInfo[key] = val
		}
	}

	ctx := &SystemContext{
		Date:       now.Format("2006-01-02"),
		Time:       now.Format("15:04:05"),
		Timezone:   now.Location().String(),
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Hostname:   hostname,
		WorkingDir: getCwd(),
		HomeDir:    getHomeDir(),
		User:       user,
		GoVersion:  runtime.Version(),
		EnvInfo:    envInfo,
	}

	// 注入记忆地图
	if s.memoryService != nil {
		ctx.HasMemory = true
		// Memory entries are injected via semantic search in prepareContext (RetrieveAndInject).
		// Do NOT list memories here to avoid injecting irrelevant entries (List has no goal context).
	}

	// MCP server names (deduplicated prefixes, e.g. mcp_filesystem)
	if s.mcpService != nil {
		seen := map[string]bool{}
		for _, t := range s.mcpService.ListTools() {
			parts := strings.SplitN(t.Function.Name, "_", 3)
			if len(parts) >= 3 && parts[0] == "mcp" {
				server := parts[0] + "_" + parts[1]
				if !seen[server] {
					seen[server] = true
					ctx.MCPServers = append(ctx.MCPServers, server)
				}
			}
		}
	}

	// Skill names
	if s.skillsService != nil {
		skillsList, _ := s.skillsService.ListSkills(bgCtx, skills.SkillFilter{})
		for _, sk := range skillsList {
			// Skip if disabled or explicitly hidden from model invocation
			if !sk.Enabled || sk.DisableModelInvocation {
				continue
			}
			ctx.SkillNames = append(ctx.SkillNames, "skill_"+sk.ID)
		}
	}

	return ctx
}

// FormatForPrompt 格式化系统上下文为prompt字符串
func (c *SystemContext) FormatForPrompt() string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Date/Time: %s %s (%s) | OS: %s/%s | Dir: %s | User: %s\n",
		c.Date, c.Time, c.Timezone, c.OS, c.Arch, c.WorkingDir, c.User)

	if len(c.EnvInfo) > 0 {
		parts := make([]string, 0, len(c.EnvInfo))
		for k, v := range c.EnvInfo {
			if k == "PATH" {
				continue // PATH is too long and not useful for the LLM
			}
			parts = append(parts, k+"="+v)
		}
		if len(parts) > 0 {
			sb.WriteString("Env: " + strings.Join(parts, ", ") + "\n")
		}
	}

	// Memory availability hint only — actual recalled memories come via user message (prepareContext)
	if c.HasMemory {
		sb.WriteString("Memory: long-term memory is enabled")
		sb.WriteString("\n")
	}

	if len(c.MCPServers) > 0 {
		sb.WriteString("MCP: " + strings.Join(c.MCPServers, ", ") + "\n")
	}

	if len(c.SkillNames) > 0 {
		sb.WriteString("Skills: " + strings.Join(c.SkillNames, ", ") + "\n")
	}

	return sb.String()
}

// FormatCompact 紧凑格式，适合嵌入到现有prompt中
func (c *SystemContext) FormatCompact() string {
	return fmt.Sprintf("[Context: %s %s, %s/%s, dir=%s]",
		c.Date, c.Time, c.OS, c.Arch, shortPath(c.WorkingDir))
}

func getCwd() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "unknown"
}

func getHomeDir() string {
	// os.UserHomeDir() is available since Go 1.12
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	// Fallback
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	if home := os.Getenv("USERPROFILE"); home != "" {
		return home
	}
	return "unknown"
}

func shortPath(path string) string {
	// Shorten home directory to ~
	home := getHomeDir()
	if home != "unknown" && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	// If path is too long, truncate
	if len(path) > 30 {
		return "..." + path[len(path)-27:]
	}
	return path
}
