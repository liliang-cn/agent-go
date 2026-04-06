package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// SubconsciousJob represents a background task for the Subconscious Agent
type SubconsciousJob struct {
	SessionID string
	AgentID   string
	Goal      string
	Result    string
	Messages  []domain.Message
}

// SubconsciousWorkerPool manages background memory extraction agents
type SubconsciousWorkerPool struct {
	svc      *Service
	jobs     chan SubconsciousJob
	stopChan chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	running  bool
}

// NewSubconsciousWorkerPool creates a new background worker pool
func NewSubconsciousWorkerPool(svc *Service) *SubconsciousWorkerPool {
	return &SubconsciousWorkerPool{
		svc:      svc,
		jobs:     make(chan SubconsciousJob, 100),
		stopChan: make(chan struct{}),
	}
}

// Start spins up the specified number of background workers
func (p *SubconsciousWorkerPool) Start(workers int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return
	}
	p.running = true
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
	p.svc.logger.Info("Subconscious worker pool started", slog.Int("workers", workers))
}

// Stop gracefully shuts down the worker pool
func (p *SubconsciousWorkerPool) Stop() {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.running = false
	p.mu.Unlock()
	close(p.stopChan)
	p.wg.Wait()
}

// Enqueue submits a job for background processing
func (p *SubconsciousWorkerPool) Enqueue(job SubconsciousJob) {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	select {
	case p.jobs <- job:
	default:
		p.svc.logger.Warn("Subconscious worker pool queue is full, dropping job", slog.String("session_id", job.SessionID))
	}
}

func (p *SubconsciousWorkerPool) worker(id int) {
	defer p.wg.Done()
	for {
		select {
		case <-p.stopChan:
			return
		case job := <-p.jobs:
			p.processJob(job)
		}
	}
}

func (p *SubconsciousWorkerPool) processJob(job SubconsciousJob) {
	// Skip if there's no memory service to save to
	if p.svc.memoryService == nil {
		return
	}

	p.svc.logger.Debug("Subconscious worker starting extraction", slog.String("session_id", job.SessionID))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var history string
	for _, m := range job.Messages {
		if m.Role == "user" || m.Role == "assistant" {
			history += fmt.Sprintf("[%s]: %s\n", m.Role, m.Content)
		}
	}

	// Keep it lightweight - we only want it to extract and save memory
	prompt := fmt.Sprintf(`[BACKGROUND SUBCONSCIOUS PROCESS]
You are a background agent running silently after the user's session ended.
Your sole purpose is to analyze the recent conversation and extract any new project architecture details, user preferences, or useful troubleshooting guides.

Conversation Goal: %s
Conversation Result: %s

Recent History:
%s

If you find anything worth remembering, formulate it clearly and use the memory tools or 'task_complete' tool with your findings. If there is nothing useful, just output 'task_complete' with an empty result. Do NOT interact with the user.`, job.Goal, job.Result, history)

	// Run the agent headlessly using the main agent but in a separate session
	events, err := p.svc.RunStreamWithOptions(ctx, prompt, WithSessionID(job.SessionID+"-subconscious"), WithStoreHistory(false))
	if err != nil {
		p.svc.logger.Error("Subconscious extraction failed to start", slog.String("error", err.Error()))
		return
	}

	// Drain events silently
	for event := range events {
		if event.Type == EventTypeError {
			p.svc.logger.Error("Subconscious extraction error", slog.String("error", event.Content))
		}
	}

	p.svc.logger.Debug("Subconscious worker completed extraction", slog.String("session_id", job.SessionID))
}
