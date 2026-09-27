package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// DefaultProjectInstructionFile is the file name looked for when
// WithProjectInstructions is given no names of its own.
const DefaultProjectInstructionFile = "AGENTS.md"

// DefaultProjectInstructionsMaxBytes caps the total file content placed in the
// system prompt. The files ride in every request of every run, so an
// accidentally huge one would be paid for on every turn.
const DefaultProjectInstructionsMaxBytes = 32 * 1024

// ProjectInstructions configures project instruction files (AGENTS.md and the
// like): files a repository keeps for whatever agent works in it.
//
// Discovery starts at the agent's workspace — the sandbox workspace when a
// sandbox is configured, the process directory only when there is none — and
// walks up one directory at a time. It stops after the first directory that
// is a repository root (contains .git), after StopDir, or at the filesystem
// root, whichever comes first.
//
// Every file found is used, not just the nearest: they are concatenated
// nearest first, and the prompt says that a nearer file takes precedence over
// a farther one where they disagree. Nearest first is also what the size cap
// needs — when the budget runs out it is the farthest, most general file that
// loses its tail, never the one written for this directory.
type ProjectInstructions struct {
	// Files are the names looked for in each directory, in order. Empty means
	// just AGENTS.md. Two names in one directory are both used, in this
	// order; a name that is a symlink to a file already included (CLAUDE.md
	// -> AGENTS.md) is included once.
	Files []string
	// StopDir, when set, is the last directory searched. A workspace outside
	// it is searched up to its repository root or the filesystem root.
	StopDir string
	// MaxBytes caps the total file content included. 0 means
	// DefaultProjectInstructionsMaxBytes. Truncation is stated in the prompt,
	// never silent.
	MaxBytes int
}

// WithProjectInstructions places project instruction files in the system
// prompt. Off unless called.
//
// The files are read once at the start of each run and held for the whole
// run, so an agent that edits AGENTS.md mid-run does not change the prompt
// under its own feet; the next run sees the edit. They sit in the static part
// of the system prompt, and the rendered text carries no timestamps or sizes
// that change without the files changing — unchanged files give byte-identical
// prompts across rounds and runs, which is what a provider's prefix cache
// needs.
func (b *Builder) WithProjectInstructions(cfg ProjectInstructions) *Builder {
	c := cfg
	c.Files = append([]string(nil), cfg.Files...)
	b.projectInstructions = &c
	return b
}

// WithProjectInstructionFiles is WithProjectInstructions with only the file
// names set, e.g. WithProjectInstructionFiles("AGENTS.md", "CLAUDE.md").
func (b *Builder) WithProjectInstructionFiles(names ...string) *Builder {
	return b.WithProjectInstructions(ProjectInstructions{Files: names})
}

// ProjectInstructionFile is one discovered instruction file.
type ProjectInstructionFile struct {
	Path    string
	Content string
}

func (c ProjectInstructions) names() []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range c.Files {
		n = strings.TrimSpace(n)
		// A name is a name, not a path: "../x" would read outside the walk.
		if n == "" || n == "." || n == ".." || strings.ContainsAny(n, `/\`) || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) == 0 {
		out = []string{DefaultProjectInstructionFile}
	}
	return out
}

func (c ProjectInstructions) maxBytes() int {
	if c.MaxBytes > 0 {
		return c.MaxBytes
	}
	return DefaultProjectInstructionsMaxBytes
}

// discoverProjectInstructions walks from start upward and returns the
// instruction files found, nearest first.
func discoverProjectInstructions(start string, cfg ProjectInstructions) []ProjectInstructionFile {
	start = strings.TrimSpace(start)
	if start == "" {
		return nil
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil
	}
	dir = filepath.Clean(dir)
	stop := ""
	if s := strings.TrimSpace(cfg.StopDir); s != "" {
		if abs, err := filepath.Abs(s); err == nil {
			stop = filepath.Clean(abs)
		}
	}
	names := cfg.names()

	var files []ProjectInstructionFile
	seen := map[string]bool{}
	for {
		for _, name := range names {
			p := filepath.Join(dir, name)
			info, err := os.Stat(p)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			real := p
			if r, err := filepath.EvalSymlinks(p); err == nil {
				real = r
			}
			if seen[real] {
				continue
			}
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			seen[real] = true
			files = append(files, ProjectInstructionFile{Path: p, Content: string(data)})
		}
		if dir == stop || isRepoRoot(dir) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return files
}

func isRepoRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// formatProjectInstructions renders the files for the system prompt. It is a
// pure function of its inputs: same files, same bytes.
func formatProjectInstructions(files []ProjectInstructionFile, maxBytes int) string {
	if len(files) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Project instructions\n")
	sb.WriteString("Instruction files found from the working directory upward, nearest first. " +
		"Follow them; where two disagree, the nearer file (listed earlier) takes precedence.\n")
	remaining := maxBytes
	for i, f := range files {
		content := strings.TrimSpace(f.Content)
		if remaining <= 0 {
			omitted := make([]string, 0, len(files)-i)
			for _, rest := range files[i:] {
				omitted = append(omitted, rest.Path)
			}
			fmt.Fprintf(&sb, "\n[project instructions truncated: size limit of %d bytes reached; not included: %s]\n",
				maxBytes, strings.Join(omitted, ", "))
			break
		}
		fmt.Fprintf(&sb, "\n### %s\n", f.Path)
		if len(content) <= remaining {
			sb.WriteString(content)
			sb.WriteString("\n")
			remaining -= len(content)
			continue
		}
		cut := remaining
		for cut > 0 && !utf8.RuneStart(content[cut]) {
			cut--
		}
		sb.WriteString(content[:cut])
		fmt.Fprintf(&sb, "\n[truncated: showing %d of %d bytes of %s; size limit of %d bytes reached]\n",
			cut, len(content), f.Path, maxBytes)
		remaining = 0
	}
	return strings.TrimRight(sb.String(), "\n")
}

// projectInstructionsForRun reads and renders the instruction files for one
// run. Empty when the option is off or nothing was found.
func (s *Service) projectInstructionsForRun() string {
	if s == nil || s.projectInstructions == nil {
		return ""
	}
	cfg := *s.projectInstructions
	return formatProjectInstructions(discoverProjectInstructions(s.promptWorkingDir(), cfg), cfg.maxBytes())
}

// resolveProjectInstructions snapshots the run's instruction files onto cfg,
// once. Called wherever the other run-scoped prompt sections are resolved.
func (s *Service) resolveProjectInstructions(cfg *RunConfig) {
	if cfg == nil || cfg.projectInstructionsResolved {
		return
	}
	cfg.projectInstructions = s.projectInstructionsForRun()
	cfg.projectInstructionsResolved = true
}
