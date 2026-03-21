package a2a

import (
	"context"
	"fmt"
	"iter"
	"net/url"
	"strings"

	a2aproto "github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2aclient"
	agentcard "github.com/a2aproject/a2a-go/a2aclient/agentcard"
)

type ClientConfig struct {
	AcceptedOutputModes []string
	PreferredTransports []a2aproto.TransportProtocol
	Polling             bool
}

type Client struct {
	card    *a2aproto.AgentCard
	client  *a2aclient.Client
	cardURL string
}

type TextEvent struct {
	Event a2aproto.Event
	Text  string
}

func NewTextMessage(prompt string) *a2aproto.MessageSendParams {
	return &a2aproto.MessageSendParams{
		Message: a2aproto.NewMessage(a2aproto.MessageRoleUser, a2aproto.TextPart{Text: strings.TrimSpace(prompt)}),
	}
}

func ResolveCardURL(ctx context.Context, cardURL string) (*a2aproto.AgentCard, error) {
	baseURL, path, err := splitCardURL(cardURL)
	if err != nil {
		return nil, err
	}
	return agentcard.DefaultResolver.Resolve(ctx, baseURL, agentcard.WithPath(path))
}

func Connect(ctx context.Context, cardURL string, cfg ClientConfig, opts ...a2aclient.FactoryOption) (*Client, error) {
	card, err := ResolveCardURL(ctx, cardURL)
	if err != nil {
		return nil, err
	}

	factoryOpts := make([]a2aclient.FactoryOption, 0, len(opts)+1)
	factoryOpts = append(factoryOpts, a2aclient.WithConfig(a2aclient.Config{
		AcceptedOutputModes: append([]string(nil), cfg.AcceptedOutputModes...),
		PreferredTransports: append([]a2aproto.TransportProtocol(nil), cfg.PreferredTransports...),
		Polling:             cfg.Polling,
	}))
	factoryOpts = append(factoryOpts, opts...)

	client, err := a2aclient.NewFromCard(ctx, card, factoryOpts...)
	if err != nil {
		return nil, err
	}
	return &Client{
		card:    card,
		client:  client,
		cardURL: strings.TrimSpace(cardURL),
	}, nil
}

func (c *Client) Card() *a2aproto.AgentCard {
	return c.card
}

func (c *Client) CardURL() string {
	return c.cardURL
}

func (c *Client) Send(ctx context.Context, params *a2aproto.MessageSendParams) (a2aproto.SendMessageResult, error) {
	if c == nil || c.client == nil {
		return nil, fmt.Errorf("a2a client is not connected")
	}
	return c.client.SendMessage(ctx, params)
}

func (c *Client) SendText(ctx context.Context, prompt string) (string, a2aproto.SendMessageResult, error) {
	result, err := c.Send(ctx, NewTextMessage(prompt))
	if err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(ExtractTextFromSendResult(result)), result, nil
}

func (c *Client) Stream(ctx context.Context, params *a2aproto.MessageSendParams) iter.Seq2[TextEvent, error] {
	return func(yield func(TextEvent, error) bool) {
		if c == nil || c.client == nil {
			yield(TextEvent{}, fmt.Errorf("a2a client is not connected"))
			return
		}
		for event, err := range c.client.SendStreamingMessage(ctx, params) {
			if err != nil {
				yield(TextEvent{}, err)
				return
			}
			if !yield(TextEvent{
				Event: event,
				Text:  strings.TrimSpace(ExtractTextFromEvent(event)),
			}, nil) {
				return
			}
		}
	}
}

func (c *Client) StreamText(ctx context.Context, prompt string) iter.Seq2[TextEvent, error] {
	return c.Stream(ctx, NewTextMessage(prompt))
}

func (c *Client) GetTask(ctx context.Context, taskID string, historyLength int) (*a2aproto.Task, error) {
	if c == nil || c.client == nil {
		return nil, fmt.Errorf("a2a client is not connected")
	}
	params := &a2aproto.TaskQueryParams{
		ID: a2aproto.TaskID(strings.TrimSpace(taskID)),
	}
	if historyLength > 0 {
		params.HistoryLength = &historyLength
	}
	return c.client.GetTask(ctx, params)
}

func (c *Client) CancelTask(ctx context.Context, taskID string) (*a2aproto.Task, error) {
	if c == nil || c.client == nil {
		return nil, fmt.Errorf("a2a client is not connected")
	}
	return c.client.CancelTask(ctx, &a2aproto.TaskIDParams{ID: a2aproto.TaskID(strings.TrimSpace(taskID))})
}

func (c *Client) ListTasks(ctx context.Context, req *a2aproto.ListTasksRequest) (*a2aproto.ListTasksResponse, error) {
	if c == nil || c.client == nil {
		return nil, fmt.Errorf("a2a client is not connected")
	}
	if req == nil {
		req = &a2aproto.ListTasksRequest{}
	}
	return c.client.ListTasks(ctx, req)
}

func (c *Client) Resubscribe(ctx context.Context, taskID string) iter.Seq2[TextEvent, error] {
	return func(yield func(TextEvent, error) bool) {
		if c == nil || c.client == nil {
			yield(TextEvent{}, fmt.Errorf("a2a client is not connected"))
			return
		}
		for event, err := range c.client.ResubscribeToTask(ctx, &a2aproto.TaskIDParams{ID: a2aproto.TaskID(strings.TrimSpace(taskID))}) {
			if err != nil {
				yield(TextEvent{}, err)
				return
			}
			if !yield(TextEvent{
				Event: event,
				Text:  strings.TrimSpace(ExtractTextFromEvent(event)),
			}, nil) {
				return
			}
		}
	}
}

func ExtractTextFromParts(parts a2aproto.ContentParts) string {
	var chunks []string
	for _, raw := range parts {
		switch part := raw.(type) {
		case a2aproto.TextPart:
			if text := strings.TrimSpace(part.Text); text != "" {
				chunks = append(chunks, text)
			}
		case a2aproto.DataPart:
			if len(part.Data) > 0 {
				chunks = append(chunks, fmt.Sprintf("%v", part.Data))
			}
		}
	}
	return strings.TrimSpace(strings.Join(chunks, "\n"))
}

func ExtractTextFromMessage(msg *a2aproto.Message) string {
	if msg == nil {
		return ""
	}
	return ExtractTextFromParts(msg.Parts)
}

func ExtractTextFromTask(task *a2aproto.Task) string {
	if task == nil {
		return ""
	}
	for i := len(task.Artifacts) - 1; i >= 0; i-- {
		if text := ExtractTextFromParts(task.Artifacts[i].Parts); text != "" {
			return text
		}
	}
	if text := ExtractTextFromMessage(task.Status.Message); text != "" {
		return text
	}
	for i := len(task.History) - 1; i >= 0; i-- {
		if task.History[i] != nil && task.History[i].Role == a2aproto.MessageRoleAgent {
			if text := ExtractTextFromMessage(task.History[i]); text != "" {
				return text
			}
		}
	}
	return ""
}

func ExtractTextFromSendResult(result a2aproto.SendMessageResult) string {
	switch typed := result.(type) {
	case *a2aproto.Message:
		return ExtractTextFromMessage(typed)
	case *a2aproto.Task:
		return ExtractTextFromTask(typed)
	default:
		return ""
	}
}

func ExtractTextFromEvent(event a2aproto.Event) string {
	switch typed := event.(type) {
	case *a2aproto.Message:
		return ExtractTextFromMessage(typed)
	case *a2aproto.Task:
		return ExtractTextFromTask(typed)
	case *a2aproto.TaskArtifactUpdateEvent:
		if typed != nil && typed.Artifact != nil {
			return ExtractTextFromParts(typed.Artifact.Parts)
		}
	case *a2aproto.TaskStatusUpdateEvent:
		if typed != nil {
			return ExtractTextFromMessage(typed.Status.Message)
		}
	}
	return ""
}

func splitCardURL(cardURL string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(cardURL))
	if err != nil {
		return "", "", fmt.Errorf("parse card url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", "", fmt.Errorf("card url must be absolute: %s", cardURL)
	}
	baseURL := fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}
	return baseURL, path, nil
}
