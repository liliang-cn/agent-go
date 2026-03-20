package agent

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/google/uuid"
)

const (
	operatorSessionOutputLimit = 256 * 1024
	operatorSessionTailDefault = 4000
)

type operatorSessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*operatorSession
}

type operatorSession struct {
	id         string
	provider   string
	command    string
	args       []string
	workdir    string
	cmd        *exec.Cmd
	ptyFile    *os.File
	output     []byte
	createdAt  time.Time
	updatedAt  time.Time
	finishedAt *time.Time
	exitCode   *int
	errText    string
	mu         sync.RWMutex
}

var globalOperatorSessions = &operatorSessionManager{
	sessions: make(map[string]*operatorSession),
}

func (m *operatorSessionManager) start(provider, command string, args []string, workdir string, env map[string]string, prompt string, wait time.Duration) (map[string]interface{}, error) {
	cmd := exec.Command(command, args...)
	if strings.TrimSpace(workdir) != "" {
		cmd.Dir = strings.TrimSpace(workdir)
	}
	cmd.Env = mergeCommandEnv(env)

	ptyFile, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start session: %w", err)
	}

	now := time.Now()
	session := &operatorSession{
		id:        uuid.NewString(),
		provider:  provider,
		command:   command,
		args:      append([]string(nil), args...),
		workdir:   strings.TrimSpace(workdir),
		cmd:       cmd,
		ptyFile:   ptyFile,
		createdAt: now,
		updatedAt: now,
	}

	m.mu.Lock()
	m.sessions[session.id] = session
	m.mu.Unlock()

	go session.captureOutput()
	go session.waitProcess()

	if strings.TrimSpace(prompt) != "" {
		if _, err := m.sendPrompt(session.id, prompt, wait); err != nil {
			return nil, err
		}
	}

	if wait > 0 {
		time.Sleep(wait)
	}
	return session.snapshot(operatorSessionTailDefault), nil
}

func runCommandOnce(command string, args []string, workdir string, env map[string]string, stdin string) (map[string]interface{}, error) {
	cmd := exec.Command(command, args...)
	if strings.TrimSpace(workdir) != "" {
		cmd.Dir = strings.TrimSpace(workdir)
	}
	cmd.Env = mergeCommandEnv(env)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	startedAt := time.Now()
	err := cmd.Run()
	finishedAt := time.Now()
	result := map[string]interface{}{
		"command":     command,
		"args":        append([]string(nil), args...),
		"workdir":     strings.TrimSpace(workdir),
		"created_at":  startedAt,
		"updated_at":  finishedAt,
		"finished_at": finishedAt,
		"stdout":      stdout.String(),
		"stderr":      stderr.String(),
	}
	if cmd.ProcessState != nil {
		result["exit_code"] = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		result["error"] = err.Error()
		result["status"] = "failed"
		return result, fmt.Errorf("run command once: %w", err)
	}
	result["status"] = "completed"
	return result, nil
}

func (m *operatorSessionManager) sendPrompt(sessionID, prompt string, wait time.Duration) (map[string]interface{}, error) {
	session, err := m.get(sessionID)
	if err != nil {
		return nil, err
	}
	if err := session.writePrompt(prompt); err != nil {
		return nil, err
	}
	if wait > 0 {
		time.Sleep(wait)
	}
	return session.snapshot(operatorSessionTailDefault), nil
}

func (m *operatorSessionManager) interrupt(sessionID string, wait time.Duration) (map[string]interface{}, error) {
	session, err := m.get(sessionID)
	if err != nil {
		return nil, err
	}
	if err := session.signal(os.Interrupt); err != nil {
		return nil, err
	}
	if wait > 0 {
		time.Sleep(wait)
	}
	return session.snapshot(operatorSessionTailDefault), nil
}

func (m *operatorSessionManager) stop(sessionID string, force bool, wait time.Duration) (map[string]interface{}, error) {
	session, err := m.get(sessionID)
	if err != nil {
		return nil, err
	}
	sig := os.Interrupt
	if force {
		sig = os.Kill
	}
	if err := session.signal(sig); err != nil {
		return nil, err
	}
	if wait > 0 {
		time.Sleep(wait)
	}
	return session.snapshot(operatorSessionTailDefault), nil
}

func (m *operatorSessionManager) getSnapshot(sessionID string, tailChars int) (map[string]interface{}, error) {
	session, err := m.get(sessionID)
	if err != nil {
		return nil, err
	}
	return session.snapshot(tailChars), nil
}

func (m *operatorSessionManager) list(tailChars int) []map[string]interface{} {
	m.mu.RLock()
	sessions := make([]*operatorSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.RUnlock()

	out := make([]map[string]interface{}, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, session.snapshot(tailChars))
	}
	return out
}

func (m *operatorSessionManager) get(sessionID string) (*operatorSession, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	m.mu.RLock()
	session := m.sessions[sessionID]
	m.mu.RUnlock()
	if session == nil {
		return nil, fmt.Errorf("operator session %s not found", sessionID)
	}
	return session, nil
}

func (s *operatorSession) captureOutput() {
	buf := make([]byte, 4096)
	for {
		n, err := s.ptyFile.Read(buf)
		if n > 0 {
			s.appendOutput(string(buf[:n]))
		}
		if err != nil {
			if err != io.EOF {
				s.setError(err.Error())
			}
			return
		}
	}
}

func (s *operatorSession) waitProcess() {
	err := s.cmd.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.updatedAt = now
	s.finishedAt = &now
	if s.cmd.ProcessState != nil {
		exitCode := s.cmd.ProcessState.ExitCode()
		s.exitCode = &exitCode
	}
	if err != nil && s.errText == "" {
		s.errText = err.Error()
	}
	if s.ptyFile != nil {
		_ = s.ptyFile.Close()
		s.ptyFile = nil
	}
}

func (s *operatorSession) appendOutput(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.output = append(s.output, []byte(text)...)
	if len(s.output) > operatorSessionOutputLimit {
		s.output = append([]byte(nil), s.output[len(s.output)-operatorSessionOutputLimit:]...)
	}
	s.updatedAt = time.Now()
}

func (s *operatorSession) setError(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(message) != "" {
		s.errText = strings.TrimSpace(message)
	}
	s.updatedAt = time.Now()
}

func (s *operatorSession) writePrompt(prompt string) error {
	s.mu.RLock()
	ptyFile := s.ptyFile
	finished := s.finishedAt != nil
	s.mu.RUnlock()

	if finished {
		return fmt.Errorf("operator session %s is already finished", s.id)
	}
	if ptyFile == nil {
		return fmt.Errorf("operator session %s has no active terminal", s.id)
	}

	prompt = strings.TrimRight(prompt, "\n")
	if prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	if _, err := io.WriteString(ptyFile, prompt+"\n"); err != nil {
		return fmt.Errorf("write prompt: %w", err)
	}
	s.mu.Lock()
	s.updatedAt = time.Now()
	s.mu.Unlock()
	return nil
}

func (s *operatorSession) signal(sig os.Signal) error {
	s.mu.RLock()
	finished := s.finishedAt != nil
	process := (*os.Process)(nil)
	if s.cmd != nil {
		process = s.cmd.Process
	}
	s.mu.RUnlock()

	if finished {
		return nil
	}
	if process == nil {
		return fmt.Errorf("operator session %s has no active process", s.id)
	}
	if err := process.Signal(sig); err != nil {
		return fmt.Errorf("signal session: %w", err)
	}
	return nil
}

func (s *operatorSession) snapshot(tailChars int) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if tailChars <= 0 {
		tailChars = operatorSessionTailDefault
	}
	output := string(s.output)
	runes := []rune(output)
	if len(runes) > tailChars {
		output = string(runes[len(runes)-tailChars:])
	}

	status := "running"
	if s.finishedAt != nil {
		if s.errText != "" {
			status = "failed"
		} else {
			status = "completed"
		}
	}

	out := map[string]interface{}{
		"session_id":  s.id,
		"provider":    s.provider,
		"command":     s.command,
		"args":        append([]string(nil), s.args...),
		"workdir":     s.workdir,
		"status":      status,
		"output":      output,
		"created_at":  s.createdAt,
		"updated_at":  s.updatedAt,
		"finished_at": s.finishedAt,
	}
	if s.cmd != nil && s.cmd.Process != nil {
		out["pid"] = s.cmd.Process.Pid
	}
	if s.exitCode != nil {
		out["exit_code"] = *s.exitCode
	}
	if s.errText != "" {
		out["error"] = s.errText
	}
	return out
}

func mergeCommandEnv(extra map[string]string) []string {
	env := os.Environ()
	if len(extra) == 0 {
		return env
	}
	for key, value := range extra {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		env = append(env, key+"="+value)
	}
	return env
}

func resolveCodingAgentCommand(provider string, command string, args []string) (string, []string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "claude":
		return "claude", append([]string(nil), args...), nil
	case "gemini":
		return "gemini", append([]string(nil), args...), nil
	case "codex":
		return "codex", append([]string(nil), args...), nil
	case "opencode":
		return "opencode", append([]string(nil), args...), nil
	case "custom":
		command = strings.TrimSpace(command)
		if command == "" {
			return "", nil, fmt.Errorf("command is required when provider=custom")
		}
		return command, append([]string(nil), args...), nil
	default:
		return "", nil, fmt.Errorf("unsupported coding agent provider %q", provider)
	}
}
