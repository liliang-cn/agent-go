package sandbox

import (
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
	sessionOutputLimit = 256 * 1024
	sessionTailDefault = 4000
)

// localSession is a PTY-backed Session for the LocalSandbox. It mirrors the
// concurrency pattern used by pkg/agent/operator_sessions.go: a mutex guards the
// output buffer and lifecycle fields, with background goroutines capturing
// output and waiting on the process.
type localSession struct {
	id         string
	cmd        *exec.Cmd
	ptyFile    *os.File
	mu         sync.RWMutex
	output     []byte
	finishedAt *time.Time
	exitCode   *int
	errText    string
}

// newLocalSession starts cmd attached to a PTY and begins capturing output.
func newLocalSession(cmd *exec.Cmd) (*localSession, error) {
	ptyFile, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start shell session: %w", err)
	}
	s := &localSession{
		id:      uuid.NewString(),
		cmd:     cmd,
		ptyFile: ptyFile,
	}
	go s.captureOutput()
	go s.waitProcess()
	return s, nil
}

func (s *localSession) ID() string { return s.id }

func (s *localSession) Done() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.finishedAt != nil
}

func (s *localSession) Send(input string) error {
	s.mu.RLock()
	ptyFile := s.ptyFile
	finished := s.finishedAt != nil
	s.mu.RUnlock()

	if finished {
		return fmt.Errorf("session %s is already finished", s.id)
	}
	if ptyFile == nil {
		return fmt.Errorf("session %s has no active terminal", s.id)
	}
	payload := strings.TrimRight(input, "\n") + "\n"
	if _, err := io.WriteString(ptyFile, payload); err != nil {
		return fmt.Errorf("send to session: %w", err)
	}
	return nil
}

func (s *localSession) Read(tailChars int) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if tailChars <= 0 {
		tailChars = sessionTailDefault
	}
	runes := []rune(string(s.output))
	if len(runes) > tailChars {
		runes = runes[len(runes)-tailChars:]
	}
	return string(runes)
}

func (s *localSession) Interrupt() error {
	return s.signal(os.Interrupt)
}

func (s *localSession) Stop(force bool) error {
	sig := os.Signal(os.Interrupt)
	if force {
		sig = os.Kill
	}
	return s.signal(sig)
}

func (s *localSession) signal(sig os.Signal) error {
	s.mu.RLock()
	finished := s.finishedAt != nil
	var process *os.Process
	if s.cmd != nil {
		process = s.cmd.Process
	}
	s.mu.RUnlock()

	if finished {
		return nil
	}
	if process == nil {
		return fmt.Errorf("session %s has no active process", s.id)
	}
	if err := process.Signal(sig); err != nil {
		return fmt.Errorf("signal session: %w", err)
	}
	return nil
}

func (s *localSession) captureOutput() {
	buf := make([]byte, 4096)
	for {
		n, err := s.ptyFile.Read(buf)
		if n > 0 {
			s.appendOutput(buf[:n])
		}
		if err != nil {
			if err != io.EOF {
				s.setError(err.Error())
			}
			return
		}
	}
}

func (s *localSession) waitProcess() {
	err := s.cmd.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.finishedAt = &now
	if s.cmd.ProcessState != nil {
		code := s.cmd.ProcessState.ExitCode()
		s.exitCode = &code
	}
	if err != nil && s.errText == "" {
		s.errText = err.Error()
	}
	if s.ptyFile != nil {
		_ = s.ptyFile.Close()
		s.ptyFile = nil
	}
}

func (s *localSession) appendOutput(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.output = append(s.output, b...)
	if len(s.output) > sessionOutputLimit {
		s.output = append([]byte(nil), s.output[len(s.output)-sessionOutputLimit:]...)
	}
}

func (s *localSession) setError(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(msg) != "" {
		s.errText = strings.TrimSpace(msg)
	}
}
