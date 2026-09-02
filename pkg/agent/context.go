package agent

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/skills"
)

// SystemContext is the ambient information about host and session.
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
	MCPServers []string          // available MCP server names (e.g. mcp_websearch)
	SkillNames []string          // available skill IDs
}

type UserContext struct {
	CurrentDate string
}

// buildSystemContext collects the ambient system information.
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
		Date: now.Format("2006-01-02"),
		// Hour granularity, deliberately: the system prompt is the first thing
		// in every request, and provider prefix caches (DeepSeek, OpenAI,
		// Anthropic breakpoints) stop matching at the first changed byte. A
		// second-level timestamp here invalidated the entire prompt + history
		// cache on every single turn. Within a run (and across runs in the
		// same hour) this string is now byte-stable; tasks that need the exact
		// time should use a tool.
		Time:       now.Format("15:00"),
		Timezone:   now.Location().String(),
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Hostname:   hostname,
		WorkingDir: s.promptWorkingDir(),
		HomeDir:    getHomeDir(),
		User:       user,
		GoVersion:  runtime.Version(),
		EnvInfo:    envInfo,
	}

	// Memory map injection.
	if s.memory() != nil {
		ctx.HasMemory = true
		// Memory entries are injected via semantic search in prepareContext (RetrieveAndInject).
		// Do NOT list memories here to avoid injecting irrelevant entries (List has no goal context).
	}

	// MCP server names (deduplicated prefixes, e.g. mcp_websearch)
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

	// Skill availability
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

func (s *Service) buildUserContext() *UserContext {
	now := time.Now()
	return &UserContext{
		CurrentDate: fmt.Sprintf("Today's date is %s.", now.Format("2006-01-02")),
	}
}

// FormatForPrompt renders the system context as a prompt string.
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
		// Sorted, not map order: this line sits in the prompt prefix, and a
		// byte-unstable ordering breaks provider prefix caching on every turn.
		sort.Strings(parts)
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
		sb.WriteString(fmt.Sprintf("Skills: dynamic skill discovery enabled (%d available)\n", len(c.SkillNames)))
	}

	return sb.String()
}

func (c *UserContext) FormatForMetaMessage() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.CurrentDate)
}

// FormatCompact renders a one-line form suitable for embedding in an existing prompt.
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

// promptWorkingDir is the directory the system prompt tells the model it is
// working in.
//
// It used to be os.Getwd() unconditionally — the *host process's* directory,
// which has nothing to do with where the agent's tools run. When a sandbox is
// configured, every file tool is jailed under its workspace and bash executes
// with that workspace as its cwd, so the prompt was naming a directory the
// agent's own tools could not reach.
//
// A model does what it is told. Given "Dir: /path/to/some/other/repo" as its
// first line of context, a coding agent opens a run with
// `cd /path/to/some/other/repo` — and from there it is reading, building and
// writing in whatever directory the host binary happened to be started from,
// with the sandbox jail bypassed by a single shell builtin. Observed exactly
// that: a soak agent created its project inside the framework's own checkout.
//
// So the sandbox wins when there is one. Without a sandbox the process
// directory is still the honest answer, because then it really is where the
// tools run.
func (s *Service) promptWorkingDir() string {
	if ws := strings.TrimSpace(s.workspaceRoot()); ws != "" {
		return ws
	}
	return getCwd()
}
