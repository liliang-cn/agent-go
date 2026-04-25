package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// PipelineStep configures one agent stage in a pipeline.
type PipelineStep struct {
	AgentName string
	// Prompt for this step. Use {input} to embed upstream output.
	// If empty, the upstream output is used as the prompt directly.
	Prompt string
	// TriggerPattern: when non-empty, the NEXT step starts as soon as this
	// step's accumulated output contains the pattern — without waiting for
	// this step to finish. The remaining output continues to stream.
	TriggerPattern string
}

// PipelineEvent wraps an agent event with its originating step index.
type PipelineEvent struct {
	StepIndex int
	AgentName string
	*Event
}

// buildStepPrompt formats the input prompt for a pipeline step.
func buildStepPrompt(step PipelineStep, upstreamOutput string) string {
	if step.Prompt == "" {
		return upstreamOutput
	}
	return strings.ReplaceAll(step.Prompt, "{input}", upstreamOutput)
}

// RunPipeline runs steps in sequence, piping each step's output into the next.
// Events from all steps are tagged with StepIndex and sent on the returned channel.
// If step[i].TriggerPattern is set, step[i+1] starts as soon as the pattern appears
// in step[i]'s streaming output, without waiting for step[i] to finish.
func (m *TeamManager) RunPipeline(ctx context.Context, steps []PipelineStep, initialInput string) (<-chan *PipelineEvent, error) {
	if len(steps) == 0 {
		return nil, fmt.Errorf("pipeline requires at least one step")
	}
	out := make(chan *PipelineEvent, 64)
	go func() {
		defer close(out)
		m.executePipeline(ctx, steps, initialInput, out)
	}()
	return out, nil
}

func (m *TeamManager) executePipeline(ctx context.Context, steps []PipelineStep, initialInput string, out chan<- *PipelineEvent) {
	var wg sync.WaitGroup
	currentInput := initialInput

	for i, step := range steps {
		prompt := buildStepPrompt(step, currentInput)
		isLast := i == len(steps)-1

		eventStream, err := m.dispatchTaskStream(ctx, step.AgentName, prompt, "", nil)
		if err != nil {
			out <- pipelineErrorEvent(i, step.AgentName, err)
			wg.Wait()
			return
		}

		if !isLast && step.TriggerPattern != "" {
			// Trigger mode: next step starts as soon as pattern matches in upstream output.
			// The upstream step continues draining in a goroutine.
			triggerCh := make(chan string, 1)

			wg.Add(1)
			go func(idx int, agentName, pattern string, evts <-chan *Event) {
				defer wg.Done()
				var accumulated strings.Builder
				sent := false
				for evt := range evts {
					if ctx.Err() != nil {
						return
					}
					select {
					case out <- &PipelineEvent{StepIndex: idx, AgentName: agentName, Event: evt}:
					case <-ctx.Done():
						return
					}
					if !sent {
						if evt.Type == EventTypePartial {
							accumulated.WriteString(evt.Content)
						}
						if strings.Contains(accumulated.String(), pattern) {
							sent = true
							select {
							case triggerCh <- accumulated.String():
							default:
							}
							continue
						}
						if evt.Type == EventTypeComplete || evt.Type == EventTypeBlocked {
							content := strings.TrimSpace(evt.Content)
							if content == "" {
								content = strings.TrimSpace(accumulated.String())
							}
							sent = true
							select {
							case triggerCh <- content:
							default:
							}
						}
						if evt.Type == EventTypeError && !sent {
							sent = true
							select {
							case triggerCh <- strings.TrimSpace(accumulated.String()):
							default:
							}
						}
					}
				}
				if !sent {
					select {
					case triggerCh <- strings.TrimSpace(accumulated.String()):
					default:
					}
				}
			}(i, step.AgentName, step.TriggerPattern, eventStream)

			select {
			case <-ctx.Done():
				wg.Wait()
				return
			case triggered := <-triggerCh:
				currentInput = triggered
			}
		} else {
			// Sequential mode: consume all events, collect output, then start next step.
			finalText, stepErr := consumePipelineStep(ctx, i, step.AgentName, eventStream, out)
			if stepErr != nil {
				wg.Wait()
				return
			}
			currentInput = finalText
		}
	}

	wg.Wait()
}

// consumePipelineStep drains the event stream, forwards tagged events, returns the final text.
func consumePipelineStep(ctx context.Context, stepIdx int, agentName string, events <-chan *Event, out chan<- *PipelineEvent) (string, error) {
	var text strings.Builder
	for evt := range events {
		if ctx.Err() != nil {
			return strings.TrimSpace(text.String()), ctx.Err()
		}
		select {
		case out <- &PipelineEvent{StepIndex: stepIdx, AgentName: agentName, Event: evt}:
		case <-ctx.Done():
			return strings.TrimSpace(text.String()), ctx.Err()
		}
		switch evt.Type {
		case EventTypePartial:
			text.WriteString(evt.Content)
		case EventTypeComplete, EventTypeBlocked:
			if c := strings.TrimSpace(evt.Content); c != "" {
				text.Reset()
				text.WriteString(c)
			}
		case EventTypeError:
			msg := strings.TrimSpace(evt.Content)
			if msg == "" {
				msg = fmt.Sprintf("step %d (%s) failed", stepIdx, agentName)
			}
			return strings.TrimSpace(text.String()), errors.New(msg)
		}
	}
	return strings.TrimSpace(text.String()), nil
}

func pipelineErrorEvent(stepIdx int, agentName string, err error) *PipelineEvent {
	return &PipelineEvent{
		StepIndex: stepIdx,
		AgentName: agentName,
		Event: &Event{
			Type:      EventTypeError,
			AgentName: agentName,
			Content:   err.Error(),
		},
	}
}

// CollectPipelineResult drains a PipelineEvent channel and returns the final output per step.
// Events from concurrently-running steps (trigger mode) are handled correctly.
func CollectPipelineResult(events <-chan *PipelineEvent) ([]string, error) {
	type stepState struct {
		text    strings.Builder
		hasText bool
	}
	collectors := make(map[int]*stepState)
	var firstErr error

	for pEvt := range events {
		if pEvt.Event == nil {
			continue
		}
		idx := pEvt.StepIndex
		if collectors[idx] == nil {
			collectors[idx] = &stepState{}
		}
		c := collectors[idx]
		switch pEvt.Type {
		case EventTypePartial:
			c.text.WriteString(pEvt.Content)
			c.hasText = true
		case EventTypeComplete, EventTypeBlocked:
			if t := strings.TrimSpace(pEvt.Content); t != "" {
				c.text.Reset()
				c.text.WriteString(t)
				c.hasText = true
			}
		case EventTypeError:
			if firstErr == nil && pEvt.Content != "" {
				firstErr = errors.New(pEvt.Content)
			}
		}
	}

	maxIdx := -1
	for idx := range collectors {
		if idx > maxIdx {
			maxIdx = idx
		}
	}
	if maxIdx < 0 {
		return nil, firstErr
	}

	results := make([]string, maxIdx+1)
	for idx, c := range collectors {
		results[idx] = strings.TrimSpace(c.text.String())
	}
	return results, firstErr
}
