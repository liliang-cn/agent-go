package exec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	osexec "os/exec"
	"strings"
	"sync"
	"time"
)

// ErrNotRunning is returned by every seam once the plugin process is gone —
// it never started, it crashed, or a transport failure retired it. Every seam
// turns it into its own fail-closed outcome; none of them ignores it.
var ErrNotRunning = errors.New("plugin process is not running")

// worker is one plugin process and the single pipe pair to it. Requests are
// serialised by mu: the protocol is one line out, one line back, so two
// concurrent requests on one process would read each other's replies.
type worker struct {
	name   string
	logger *slog.Logger

	cmd    *osexec.Cmd
	stdin  *os.File
	stdout *os.File
	reader *bufio.Reader

	exited  chan struct{}
	stderrs chan struct{}

	mu     sync.Mutex
	nextID uint64
	dead   error
}

// startWorker launches the process with its own three pipes. The pipes are
// ours (plain *os.File) rather than os/exec's, for two reasons: os.File on a
// pipe honours SetReadDeadline, which is how a per-request timeout is
// enforced without a goroutine per read; and Wait then closes nothing we are
// reading, so a Stop racing an in-flight request cannot read a closed fd.
func startWorker(name string, command []string, env []string, dir string, logger *slog.Logger) (*worker, error) {
	if len(command) == 0 {
		return nil, errors.New("command is empty")
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	cmd := osexec.Command(command[0], command[1:]...)
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = errW
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	if err := cmd.Start(); err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW, errR, errW} {
			f.Close()
		}
		return nil, fmt.Errorf("start %s: %w", command[0], err)
	}

	// The child holds its own copies now; ours would keep the reader from
	// ever seeing EOF.
	inR.Close()
	outW.Close()
	errW.Close()

	w := &worker{
		name:    name,
		logger:  logger,
		cmd:     cmd,
		stdin:   inW,
		stdout:  outR,
		reader:  bufio.NewReader(outR),
		exited:  make(chan struct{}),
		stderrs: make(chan struct{}),
	}
	go w.forwardStderr(errR)
	go func() {
		_ = cmd.Wait()
		close(w.exited)
	}()
	return w, nil
}

// forwardStderr copies the plugin's diagnostics into the framework logger,
// line by line and named, so a plugin's own complaints land where the rest of
// the run's logs are instead of in a terminal nobody is reading.
func (w *worker) forwardStderr(r *os.File) {
	defer close(w.stderrs)
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 8192), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		w.logger.Info("plugin stderr", "plugin", w.name, "line", line)
	}
}

// roundTrip writes one request and reads its reply, under the worker lock.
//
// A transport failure — timeout, broken pipe, unparseable line, a reply
// answering the wrong id — retires the process: the reply we gave up on may
// still arrive and would be read as the answer to the next request, and a
// desynchronised pipe is worse than a dead one. Every later request then
// fails immediately with ErrNotRunning, which each seam turns into its own
// fail-closed outcome.
func (w *worker) roundTrip(ctx context.Context, req request, timeout time.Duration) (*reply, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dead != nil {
		return nil, w.dead
	}

	w.nextID++
	req.ID = w.nextID
	line, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode %s request: %w", req.Type, err)
	}
	line = append(line, '\n')

	stop := w.unblockOnCancel(ctx)
	defer func() {
		stop()
		w.clearDeadlines()
	}()

	deadline := time.Now().Add(timeout)
	_ = w.stdin.SetWriteDeadline(deadline)
	if _, err := w.stdin.Write(line); err != nil {
		return nil, w.retire(req.Type, timeout, err)
	}

	_ = w.stdout.SetReadDeadline(deadline)
	data, err := w.reader.ReadBytes('\n')
	if err != nil {
		return nil, w.retire(req.Type, timeout, err)
	}

	var rep reply
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, w.retire(req.Type, timeout, fmt.Errorf("undecodable reply: %w", err))
	}
	// The handshake is the one reply allowed to omit the id: a plugin that
	// has not yet agreed on the protocol cannot be held to its framing.
	if rep.ID != req.ID && !(req.Type == typeHello && rep.ID == 0) {
		return nil, w.retire(req.Type, timeout, fmt.Errorf("reply id %d does not answer request %d", rep.ID, req.ID))
	}
	if rep.Error != "" {
		// The plugin answered and said no. That is a verdict, not a
		// transport failure: the process stays.
		return nil, fmt.Errorf("plugin %q: %s", w.name, rep.Error)
	}
	return &rep, nil
}

// retire marks the process untrusted, kills it, and returns the error the
// caller should report.
func (w *worker) retire(reqType string, timeout time.Duration, cause error) error {
	if errors.Is(cause, os.ErrDeadlineExceeded) {
		cause = fmt.Errorf("timed out after %s", timeout)
	} else if errors.Is(cause, os.ErrClosed) || errors.Is(cause, io.EOF) {
		cause = errors.New("plugin closed the pipe")
	}
	err := fmt.Errorf("plugin %q: %s: %w", w.name, reqType, cause)
	w.dead = fmt.Errorf("%w: %s", ErrNotRunning, err)
	w.logger.Error("plugin retired after a transport failure",
		"plugin", w.name, "request", reqType, "error", err)
	w.kill()
	return err
}

// unblockOnCancel makes a cancelled context interrupt a blocked read or
// write: an os.File deadline in the past does what closing the file would,
// without closing it. The returned func waits for the watcher, so it can
// never set a deadline that outlives its own request.
func (w *worker) unblockOnCancel(ctx context.Context) func() {
	if ctx == nil || ctx.Done() == nil {
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			past := time.Now().Add(-time.Second)
			_ = w.stdin.SetWriteDeadline(past)
			_ = w.stdout.SetReadDeadline(past)
		case <-done:
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

func (w *worker) clearDeadlines() {
	_ = w.stdin.SetWriteDeadline(time.Time{})
	_ = w.stdout.SetReadDeadline(time.Time{})
}

func (w *worker) kill() {
	if w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
	}
}

// shutdown asks the process to leave, then makes it. It takes the worker lock
// first, so an in-flight request finishes before the pipes go.
func (w *worker) shutdown(grace time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dead == nil {
		w.dead = fmt.Errorf("%w: stopped", ErrNotRunning)
		deadline := time.Now().Add(grace)
		_ = w.stdin.SetWriteDeadline(deadline)
		if line, err := json.Marshal(request{ID: w.nextID + 1, Type: typeShutdown}); err == nil {
			_, _ = w.stdin.Write(append(line, '\n'))
		}
	}
	// Closing stdin is the second, wordless way of saying the same thing: a
	// plugin that reads until EOF needs no shutdown message.
	_ = w.stdin.Close()

	select {
	case <-w.exited:
	case <-time.After(grace):
		w.logger.Warn("plugin ignored shutdown; killing",
			"plugin", w.name, "grace", grace)
		w.kill()
		<-w.exited
	}
	_ = w.stdout.Close()
	<-w.stderrs
}
