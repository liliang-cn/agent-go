package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/liliang-cn/agent-go/v3/pkg/trace"
)

// Command availability cache
var (
	commandCache   = make(map[string]bool)
	commandCacheMu sync.RWMutex
)

// CheckCommandAvailable checks if a command is available in PATH
func CheckCommandAvailable(cmd string) bool {
	commandCacheMu.RLock()
	if available, exists := commandCache[cmd]; exists {
		commandCacheMu.RUnlock()
		return available
	}
	commandCacheMu.RUnlock()

	// Check if command exists
	_, err := exec.LookPath(cmd)
	available := err == nil

	commandCacheMu.Lock()
	commandCache[cmd] = available
	commandCacheMu.Unlock()

	return available
}

// CheckRequiredCommands checks if npx and uvx are available
// Returns a map of command name to availability
func CheckRequiredCommands() map[string]bool {
	return map[string]bool{
		"npx": CheckCommandAvailable("npx"),
		"uvx": CheckCommandAvailable("uvx"),
	}
}

// GetCommandAvailabilityError returns an error message if a required command is not available
func GetCommandAvailabilityError(cmd string) error {
	available := CheckCommandAvailable(cmd)
	if available {
		return nil
	}

	switch cmd {
	case "npx":
		return fmt.Errorf("npx is not available. Please install Node.js (https://nodejs.org/) which includes npx")
	case "uvx":
		return fmt.Errorf("uvx is not available. Please install uv (https://docs.astral.sh/uv/) which includes uvx")
	default:
		return fmt.Errorf("%s is not available in PATH", cmd)
	}
}

// NewClient creates a new MCP client for the given server configuration
func NewClient(config *ServerConfig, opts *ClientOptions) (*Client, error) {
	if config == nil {
		return nil, fmt.Errorf("server config cannot be nil")
	}

	// An explicit transport replaces the ServerConfig-driven dial entirely, so
	// there is nothing in the config left to validate.
	if opts != nil && opts.Transport != nil {
		return &Client{
			config:            config,
			tools:             make(map[string]*mcp.Tool),
			resources:         make(map[string]*mcp.Resource),
			resourceTemplates: make(map[string]*mcp.ResourceTemplate),
			prompts:           make(map[string]*mcp.Prompt),
			connected:         false,
			options:           opts,
		}, nil
	}

	// Validate based on server type
	switch config.Type {
	case ServerTypeHTTP:
		if config.URL == "" {
			return nil, fmt.Errorf("URL is required for HTTP server")
		}
	case ServerTypeSSE:
		if config.URL == "" {
			return nil, fmt.Errorf("URL is required for SSE server")
		}
	case ServerTypeInProcess:
		// No specific validation needed, handled in Connect
	case ServerTypeStdio, "":
		if len(config.Command) == 0 {
			return nil, fmt.Errorf("command is required for stdio server")
		}
	default:
		return nil, fmt.Errorf("unsupported server type: %s", config.Type)
	}

	if opts == nil {
		opts = &ClientOptions{}
	}

	return &Client{
		config:            config,
		tools:             make(map[string]*mcp.Tool),
		resources:         make(map[string]*mcp.Resource),
		resourceTemplates: make(map[string]*mcp.ResourceTemplate),
		prompts:           make(map[string]*mcp.Prompt),
		connected:         false,
		options:           opts,
	}, nil
}

// Connect establishes connection to the MCP server
// It follows the MCP protocol: create transport -> initialize handshake -> ready
// The server is considered "ready" only after a successful initialize handshake.
// For stdio servers: starts the subprocess and monitors process exit
// For HTTP servers: initiates SSE connection and starts ping heartbeat
func (c *Client) Connect(ctx context.Context) error {
	if c.IsConnected() {
		return nil
	}

	// Create transport based on server type
	var transport mcp.Transport
	var err error

	if c.options != nil && c.options.Transport != nil {
		transport = c.options.Transport
	}

	switch {
	case transport != nil:
		// Caller supplied the transport; nothing to build.
	default:
		transport, err = c.createConfiguredTransport(ctx)
		if err != nil {
			return err
		}
	}

	return c.connectOver(ctx, transport)
}

// createConfiguredTransport builds the transport described by the ServerConfig.
func (c *Client) createConfiguredTransport(ctx context.Context) (mcp.Transport, error) {
	var transport mcp.Transport
	var err error

	switch c.config.Type {
	case ServerTypeHTTP:
		// Create HTTP transport for HTTP-based MCP servers
		if c.config.URL == "" {
			return nil, fmt.Errorf("URL is required for HTTP MCP server %s", c.config.Name)
		}
		transport, err = c.createHTTPTransport()
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP transport for %s: %w", c.config.Name, err)
		}
	case ServerTypeSSE:
		if c.config.URL == "" {
			return nil, fmt.Errorf("URL is required for SSE MCP server %s", c.config.Name)
		}
		transport, err = c.createSSETransport()
		if err != nil {
			return nil, fmt.Errorf("failed to create SSE transport for %s: %w", c.config.Name, err)
		}

	case ServerTypeInProcess:
		transport, err = c.createInProcessTransport(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create in-process transport for %s: %w", c.config.Name, err)
		}

	case ServerTypeStdio, "":
		// Default to stdio for backward compatibility
		if len(c.config.Command) == 0 {
			return nil, fmt.Errorf("command is required for stdio MCP server %s", c.config.Name)
		}
		transport, err = c.createStdioTransport(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create stdio transport for %s: %w", c.config.Name, err)
		}

	default:
		return nil, fmt.Errorf("unsupported server type: %s", c.config.Type)
	}

	return transport, nil
}

// connectOver performs the initialize handshake on a ready transport and loads
// the server's tools, resources and prompts.
func (c *Client) connectOver(ctx context.Context, transport mcp.Transport) error {
	session, err := c.handshake(ctx, transport)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.session = session
	c.connected = true
	c.mu.Unlock()

	// Start health monitoring based on server type
	c.startHealthMonitoring()

	c.loadAll(ctx, session)
	return nil
}

// handshake creates (or reuses) the SDK client and performs the initialize
// handshake over transport. The server is only "ready" after it succeeds.
func (c *Client) handshake(ctx context.Context, transport mcp.Transport) (*mcp.ClientSession, error) {
	c.mu.Lock()
	client := c.mcpClient
	if client == nil {
		// Create MCP client with implementation info
		clientImpl := &mcp.Implementation{
			Name:    "agentgo",
			Version: "1.0.0",
		}

		// Build SDK client options from our ClientOptions
		sdkOpts := &mcp.ClientOptions{}
		if c.options != nil {
			if c.options.CreateMessageHandler != nil {
				sdkOpts.CreateMessageHandler = c.options.CreateMessageHandler
			}
			if c.options.ElicitationHandler != nil {
				sdkOpts.ElicitationHandler = c.options.ElicitationHandler
			}
			if c.options.LoggingMessageHandler != nil {
				sdkOpts.LoggingMessageHandler = c.options.LoggingMessageHandler
			}
		}

		client = mcp.NewClient(clientImpl, sdkOpts)

		// Add roots if provided
		if c.options != nil && len(c.options.Roots) > 0 {
			client.AddRoots(c.options.Roots...)
		}

		// Store client reference for dynamic root additions. A reconnect reuses
		// it, so roots added after the first connect survive.
		c.mcpClient = client
	}
	c.mu.Unlock()

	connectCtx, cancel := context.WithTimeout(ctx, c.setupTimeout())
	defer cancel()

	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MCP server %s (initialize handshake failed): %w", c.config.Name, err)
	}
	return session, nil
}

// setupTimeout bounds each step of bringing a server up: the initialize
// handshake, and listing what it offers afterwards.
func (c *Client) setupTimeout() time.Duration {
	if c.config.DefaultTimeout > 0 {
		return c.config.DefaultTimeout
	}
	return 30 * time.Second
}

// loadAll loads tools, resources and prompts over session, logging failures.
//
// Bounded like the handshake: a server that completed it and then never
// answers tools/list would otherwise hold its caller forever, and the
// caller's ctx usually has no deadline at all.
func (c *Client) loadAll(ctx context.Context, session *mcp.ClientSession) {
	ctx, cancel := context.WithTimeout(ctx, c.setupTimeout())
	defer cancel()

	if err := c.loadToolsFrom(ctx, session); err != nil {
		log.Printf("[WARN] Failed to load tools from %s: %v", c.config.Name, err)
	}

	// Resources and prompts are optional; "method not found" is expected for
	// servers that do not support them.
	if err := c.loadResourcesFrom(ctx, session); err != nil && !isOptionalCapabilityError(err) {
		log.Printf("[DEBUG] Failed to load resources from %s: %v", c.config.Name, err)
	}
	if err := c.loadPromptsFrom(ctx, session); err != nil && !isOptionalCapabilityError(err) {
		log.Printf("[DEBUG] Failed to load prompts from %s: %v", c.config.Name, err)
	}
}

func isOptionalCapabilityError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Method not found") || strings.Contains(msg, "not supported")
}

// createStdioTransport creates a command transport for stdio-based servers
// It validates command availability for known package runners (npx, uvx) but does NOT
// pre-check if the server is running. The actual readiness is determined by the
// initialize handshake in Connect().
func (c *Client) createStdioTransport(ctx context.Context) (mcp.Transport, error) {
	// Parse command and arguments
	execPath := ""
	var args []string

	if len(c.config.Command) > 0 {
		// If command contains spaces and args is empty, try to split it
		cmdStr := c.config.Command[0]
		if strings.Contains(cmdStr, " ") && len(c.config.Args) == 0 {
			parts := strings.Fields(cmdStr)
			execPath = parts[0]
			args = parts[1:]
		} else {
			execPath = cmdStr
			args = c.config.Args
		}
	}

	if execPath == "" {
		return nil, fmt.Errorf("no executable command found for MCP server %s", c.config.Name)
	}

	// Check availability for known package runners (npx, uvx)
	// This provides better error messages before attempting to start
	if execPath == "npx" || execPath == "uvx" {
		if err := GetCommandAvailabilityError(execPath); err != nil {
			return nil, err
		}
	}

	// Create command for the MCP server.
	// IMPORTANT: bind the subprocess to a client-owned context, NOT the caller's
	// connect ctx. Callers commonly wrap Connect in a WithTimeout ctx and cancel
	// it once the initialize handshake returns; if the process were bound to that
	// ctx it would be killed immediately after tools/list, and every later
	// tools/call would fail with "connection closed: EOF". The process now lives
	// until Close() (which calls procCancel).
	procCtx, procCancel := context.WithCancel(context.Background())
	c.procCancel = procCancel
	cmd := exec.CommandContext(procCtx, execPath, args...)

	// Set working directory if specified
	if c.config.WorkingDir != "" {
		cmd.Dir = c.config.WorkingDir
	}

	// Set environment variables - inherit parent environment and add custom ones
	cmd.Env = os.Environ()
	if len(c.config.Env) > 0 {
		for key, value := range c.config.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", key, value))
		}
	}

	// Save cmd reference for process monitoring
	c.cmd = cmd

	// Create command transport
	// The actual server readiness will be determined by the initialize handshake
	return &mcp.CommandTransport{Command: cmd}, nil
}

// createHTTPTransport creates an HTTP transport for HTTP-based servers
func (c *Client) createHTTPTransport() (mcp.Transport, error) {
	// MCP SDK provides StreamableClientTransport for HTTP connections
	// This uses Server-Sent Events (SSE) for bidirectional communication

	// Create HTTP client with custom headers if needed
	httpClient := &http.Client{Transport: trace.HTTPTransport(nil)}

	// If headers are specified, we'll need to wrap the HTTP client
	// to add them to each request (this would need custom RoundTripper)
	if len(c.config.Headers) > 0 {
		httpClient.Transport = &headerTransport{
			headers: c.config.Headers,
			base:    trace.HTTPTransport(nil),
		}
	}

	transport := &mcp.StreamableClientTransport{
		Endpoint:   c.config.URL,
		HTTPClient: httpClient,
		MaxRetries: 5,
	}

	return transport, nil
}

// createSSETransport creates an SSE transport for legacy SSE-based MCP servers.
func (c *Client) createSSETransport() (mcp.Transport, error) {
	httpClient := &http.Client{Transport: trace.HTTPTransport(nil)}
	if len(c.config.Headers) > 0 {
		httpClient.Transport = &headerTransport{
			headers: c.config.Headers,
			base:    trace.HTTPTransport(nil),
		}
	}

	transport := &mcp.SSEClientTransport{
		Endpoint:   c.config.URL,
		HTTPClient: httpClient,
	}

	return transport, nil
}

// headerTransport adds custom headers to all HTTP requests
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid modifying the original
	req = req.Clone(req.Context())

	// Add custom headers
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	return t.base.RoundTrip(req)
}

// ========================================
// Health Monitoring
// ========================================

// startHealthMonitoring starts the appropriate health monitoring based on server type
// - For Stdio servers: monitors process exit
// - For HTTP servers: starts ping heartbeat
func (c *Client) startHealthMonitoring() {
	c.mu.Lock()
	if c.stopHealthCheck != nil {
		close(c.stopHealthCheck)
	}
	stop := make(chan struct{})
	c.stopHealthCheck = stop
	c.mu.Unlock()

	switch c.config.Type {
	case ServerTypeHTTP:
		go c.startPingHeartbeat(stop)
	}
}

const (
	defaultPingInterval     = 30 * time.Second
	defaultReconnectBackoff = 2 * time.Second
	defaultReconnectMax     = 60 * time.Second
)

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// canReconnect reports whether the client can rebuild its own transport. A
// caller-supplied transport is single-use, so it cannot be redialled.
func (c *Client) canReconnect() bool {
	if c.config.Type != ServerTypeHTTP {
		return false
	}
	return c.options == nil || c.options.Transport == nil
}

// startPingHeartbeat pings an HTTP server periodically. A failed ping marks the
// client disconnected and keeps retrying a reconnect, with exponential backoff,
// until it succeeds or stop is closed (Close).
func (c *Client) startPingHeartbeat(stop <-chan struct{}) {
	interval := orDefault(c.pingInterval, defaultPingInterval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if c.IsConnected() && c.doPing() {
				continue
			}
			if c.IsConnected() {
				log.Printf("[WARN] MCP server %s ping failed, marking as disconnected", c.config.Name)
				c.markDisconnected()
			}
			if !c.canReconnect() {
				return
			}
			if !c.reconnectLoop(stop) {
				return
			}
			ticker.Reset(interval)
		}
	}
}

// markDisconnected drops the dead session and flags the client as disconnected.
func (c *Client) markDisconnected() {
	c.mu.Lock()
	old := c.session
	c.connected = false
	c.session = nil
	c.mu.Unlock()
	if old != nil {
		go old.Close()
	}
}

// reconnectLoop retries reconnectOnce with backoff. It returns true once
// reconnected and false when stop was closed first.
func (c *Client) reconnectLoop(stop <-chan struct{}) bool {
	delay := orDefault(c.reconnectBackoff, defaultReconnectBackoff)
	maxDelay := orDefault(c.reconnectMax, defaultReconnectMax)
	if maxDelay < delay {
		maxDelay = delay
	}
	for {
		timer := time.NewTimer(delay)
		select {
		case <-stop:
			timer.Stop()
			return false
		case <-timer.C:
		}

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		err := c.reconnectOnce(ctx)
		cancel()
		if err == nil {
			log.Printf("[INFO] MCP server %s reconnected", c.config.Name)
			return true
		}
		select {
		case <-stop:
			return false
		default:
		}
		log.Printf("[DEBUG] MCP server %s reconnect failed (retry in %s): %v", c.config.Name, delay*2, err)
		if delay *= 2; delay > maxDelay {
			delay = maxDelay
		}
	}
}

// reconnectOnce redials an HTTP server: new transport, initialize handshake,
// fresh tool/resource/prompt lists, then swaps the session in. Only one attempt
// runs at a time, and a client that was closed meanwhile discards the result.
func (c *Client) reconnectOnce(ctx context.Context) error {
	c.mu.Lock()
	if c.connected {
		c.mu.Unlock()
		return nil
	}
	if c.stopHealthCheck == nil {
		c.mu.Unlock()
		return fmt.Errorf("client closed")
	}
	if c.reconnecting {
		c.mu.Unlock()
		return fmt.Errorf("reconnect already in progress")
	}
	c.reconnecting = true
	stop := c.stopHealthCheck
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.reconnecting = false
		c.mu.Unlock()
	}()

	transport, err := c.createConfiguredTransport(ctx)
	if err != nil {
		return err
	}
	session, err := c.handshake(ctx, transport)
	if err != nil {
		return err
	}
	// Refresh the caches from the new session before it is visible as connected.
	c.loadAll(ctx, session)

	c.mu.Lock()
	if c.stopHealthCheck != stop {
		// Closed (or restarted) while we were dialling.
		c.mu.Unlock()
		session.Close()
		return fmt.Errorf("client closed")
	}
	c.session = session
	c.connected = true
	c.mu.Unlock()
	return nil
}

// doPing sends a ping request to the server
func (c *Client) doPing() bool {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session == nil {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use the SDK's Ping method
	err := session.Ping(ctx, &mcp.PingParams{})
	return err == nil
}

// activeSession returns the live session. For a dropped HTTP server it makes one
// immediate reconnect attempt before failing with "client not connected".
func (c *Client) activeSession(ctx context.Context) (*mcp.ClientSession, error) {
	c.mu.Lock()
	session, connected := c.session, c.connected
	c.mu.Unlock()
	if connected && session != nil {
		return session, nil
	}
	if c.canReconnect() {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.reconnectOnce(rctx)
		cancel()
		if err == nil {
			c.mu.Lock()
			session, connected = c.session, c.connected
			c.mu.Unlock()
			if connected && session != nil {
				return session, nil
			}
		}
	}
	return nil, fmt.Errorf("client not connected")
}

// Ping sends a ping request to the server (public method)
func (c *Client) Ping(ctx context.Context) error {
	c.mu.Lock()
	session, connected := c.session, c.connected
	c.mu.Unlock()
	if !connected || session == nil {
		return fmt.Errorf("client not connected")
	}

	return session.Ping(ctx, &mcp.PingParams{})
}

// AddRoots adds filesystem roots that the server should focus on
// Roots define filesystem boundaries for server operations
// This can be called before or after Connect
func (c *Client) AddRoots(roots ...*mcp.Root) {
	c.mu.Lock()
	client := c.mcpClient
	c.mu.Unlock()
	if client != nil {
		client.AddRoots(roots...)
	}
}

// loadTools fetches and caches the available tools from the server
func (c *Client) loadTools(ctx context.Context) error {
	c.mu.Lock()
	session, connected := c.session, c.connected
	c.mu.Unlock()
	if !connected || session == nil {
		return fmt.Errorf("client not connected")
	}
	return c.loadToolsFrom(ctx, session)
}

func (c *Client) loadToolsFrom(ctx context.Context, session *mcp.ClientSession) error {
	toolsResponse, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return fmt.Errorf("failed to list tools: %w", err)
	}

	// Replace the cache wholesale so tools the server dropped disappear
	tools := make(map[string]*mcp.Tool, len(toolsResponse.Tools))
	for _, tool := range toolsResponse.Tools {
		tools[tool.Name] = tool
	}
	c.mu.Lock()
	c.tools = tools
	c.mu.Unlock()

	return nil
}

// loadResources fetches and caches the available resources from the server
func (c *Client) loadResourcesFrom(ctx context.Context, session *mcp.ClientSession) error {
	resourcesResponse, err := session.ListResources(ctx, &mcp.ListResourcesParams{})
	if err != nil {
		return fmt.Errorf("failed to list resources: %w", err)
	}

	resources := make(map[string]*mcp.Resource, len(resourcesResponse.Resources))
	for _, resource := range resourcesResponse.Resources {
		resources[resource.URI] = resource
	}
	c.mu.Lock()
	c.resources = resources
	c.mu.Unlock()

	// Load resource templates
	templatesResponse, err := session.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{})
	if err != nil {
		// Templates are optional, don't fail
		return nil
	}

	templates := make(map[string]*mcp.ResourceTemplate, len(templatesResponse.ResourceTemplates))
	for _, template := range templatesResponse.ResourceTemplates {
		templates[template.URITemplate] = template
	}
	c.mu.Lock()
	c.resourceTemplates = templates
	c.mu.Unlock()

	return nil
}

// loadPrompts fetches and caches the available prompts from the server
func (c *Client) loadPromptsFrom(ctx context.Context, session *mcp.ClientSession) error {
	promptsResponse, err := session.ListPrompts(ctx, &mcp.ListPromptsParams{})
	if err != nil {
		return fmt.Errorf("failed to list prompts: %w", err)
	}

	prompts := make(map[string]*mcp.Prompt, len(promptsResponse.Prompts))
	for _, prompt := range promptsResponse.Prompts {
		prompts[prompt.Name] = prompt
	}
	c.mu.Lock()
	c.prompts = prompts
	c.mu.Unlock()

	return nil
}

// toolByName returns the cached definition of one tool, or nil.
func (c *Client) toolByName(name string) *mcp.Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tools[name]
}

// GetTools returns the available tools from this server
func (c *Client) GetTools() map[string]*mcp.Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tools
}

// GetResources returns the available resources from this server
func (c *Client) GetResources() map[string]*mcp.Resource {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resources
}

// GetResourceTemplates returns the available resource templates from this server
func (c *Client) GetResourceTemplates() map[string]*mcp.ResourceTemplate {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resourceTemplates
}

// GetPrompts returns the available prompts from this server
func (c *Client) GetPrompts() map[string]*mcp.Prompt {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.prompts
}

// CallTool calls a tool on the MCP server
func (c *Client) CallTool(ctx context.Context, toolName string, arguments map[string]interface{}) (*ToolResult, error) {
	session, err := c.activeSession(ctx)
	if err != nil {
		return nil, err
	}

	// Check if tool exists
	tool := c.toolByName(toolName)
	if tool == nil {
		return nil, fmt.Errorf("tool '%s' not found on server '%s'", toolName, c.config.Name)
	}

	// Call the tool. The flow goes with it: the traceparent (and tracestate)
	// of the calling span in params._meta, which a stdio server can read as
	// well as an HTTP one, and — on HTTP — in the request header too.
	params := &mcp.CallToolParams{
		Name:      tool.Name,
		Arguments: arguments,
	}
	trace.Inject(ctx, func(k, v string) {
		if params.Meta == nil {
			params.Meta = mcp.Meta{}
		}
		params.Meta[k] = v
	})
	response, err := session.CallTool(ctx, params)
	if err == nil && response != nil {
		// A server that answers with its own span says where the work went.
		if tp, ok := response.GetMeta()[trace.TraceparentHeader].(string); ok {
			trace.NoteChild(ctx, tp)
		}
	}
	if err != nil {
		return &ToolResult{
			Success: false,
			Error:   fmt.Sprintf("tool call failed: %v", err),
		}, nil
	}

	// Process response - handle different content types
	var data interface{}
	if len(response.Content) > 0 {
		content := response.Content[0]
		// Try to cast to TextContent
		if textContent, ok := content.(*mcp.TextContent); ok {
			data = textContent.Text
		} else {
			// For other content types, return as-is
			data = content
		}
	}

	return &ToolResult{
		Success: true,
		Data:    data,
		IsError: response.IsError,
	}, nil
}

// ReadResource reads a resource from the MCP server
func (c *Client) ReadResource(ctx context.Context, uri string) (*ResourceContent, error) {
	session, err := c.activeSession(ctx)
	if err != nil {
		return nil, err
	}

	response, err := session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: uri,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read resource %s: %w", uri, err)
	}

	// Process response contents
	var content interface{}
	var mimeType string
	if len(response.Contents) > 0 {
		contents := response.Contents[0]
		// Check if it's text or blob
		if contents.Text != "" {
			content = contents.Text
		} else if len(contents.Blob) > 0 {
			content = contents.Blob
		} else {
			content = contents
		}
		mimeType = contents.MIMEType
	}

	return &ResourceContent{
		URI:      uri,
		MIMEType: mimeType,
		Content:  content,
	}, nil
}

// GetPrompt retrieves a prompt from the MCP server
func (c *Client) GetPrompt(ctx context.Context, name string, arguments map[string]string) (*PromptContent, error) {
	session, err := c.activeSession(ctx)
	if err != nil {
		return nil, err
	}

	response, err := session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get prompt %s: %w", name, err)
	}

	// Convert messages
	messages := make([]PromptMessageInfo, 0, len(response.Messages))
	for _, msg := range response.Messages {
		messages = append(messages, PromptMessageInfo{
			Role:    string(msg.Role),
			Content: msg.Content,
		})
	}

	return &PromptContent{
		Name:        name,
		Description: response.Description,
		Messages:    messages,
	}, nil
}

// Close closes the connection to the MCP server
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected && c.stopHealthCheck == nil {
		return nil
	}

	// Stop health monitoring (also ends a reconnect loop of a dropped client)
	if c.stopHealthCheck != nil {
		close(c.stopHealthCheck)
		c.stopHealthCheck = nil
	}

	var err error
	if c.session != nil {
		// Close the session
		err = c.session.Close()
		if isBenignMCPCloseError(err) {
			err = nil
		}
	}

	// Tear down the stdio subprocess (bound to a client-owned context in
	// createStdioTransport, so it outlives the connect ctx).
	if c.procCancel != nil {
		c.procCancel()
		c.procCancel = nil
	}

	c.connected = false
	c.session = nil
	c.cmd = nil

	return err
}

func isBenignMCPCloseError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "wait: no child processes") ||
		strings.Contains(msg, "os: process already finished")
}

// IsConnected returns whether the client is connected
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// GetServerInfo returns information about the connected server
func (c *Client) GetServerInfo() *mcp.Implementation {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session != nil {
		return session.InitializeResult().ServerInfo
	}
	return nil
}

// Manager manages multiple MCP clients
//
// mutex guards the maps only. Nothing slow — connecting, listing, closing —
// happens while it is held: every model round of every run reads the tool list
// under it, so one server taking its time would stall them all.
type Manager struct {
	clients map[string]*Client
	config  *Config
	mutex   sync.RWMutex

	// starting holds one channel per server being connected, closed when the
	// attempt ends, so concurrent starts of the same server share one connect.
	starting map[string]chan struct{}

	// closed is set by Close. A connect still in flight then — one started in
	// the background — closes its own client instead of leaving a child
	// process nobody owns.
	closed bool
}

// NewManager creates a new MCP manager
func NewManager(config *Config) *Manager {
	if config == nil {
		defaultConfig := DefaultConfig()
		config = &defaultConfig
	}

	return &Manager{
		clients:  make(map[string]*Client),
		config:   config,
		starting: make(map[string]chan struct{}),
	}
}

// StartServer starts an MCP server and creates a client connection
func (m *Manager) StartServer(ctx context.Context, serverName string) (*Client, error) {
	for {
		m.mutex.Lock()
		if client, exists := m.clients[serverName]; exists && client.IsConnected() {
			m.mutex.Unlock()
			return client, nil
		}
		wait, busy := m.starting[serverName]
		if !busy {
			break // still locked: this call does the connecting
		}
		m.mutex.Unlock()
		// Someone else is connecting it; take their result.
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	done := make(chan struct{})
	if m.starting == nil {
		m.starting = make(map[string]chan struct{})
	}
	m.starting[serverName] = done
	stale := m.clients[serverName]
	delete(m.clients, serverName)
	serverConfig := m.findServerConfig(serverName)
	m.mutex.Unlock()

	defer func() {
		m.mutex.Lock()
		delete(m.starting, serverName)
		m.mutex.Unlock()
		close(done)
	}()

	// Remove disconnected client (Close also stops its reconnect loop)
	if stale != nil {
		_ = stale.Close()
	}

	if serverConfig == nil {
		return nil, fmt.Errorf("server configuration not found: %s", serverName)
	}

	// Create and connect client
	client, err := NewClient(serverConfig, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create client for %s: %w", serverName, err)
	}

	if err := client.Connect(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", serverName, err)
	}

	m.mutex.Lock()
	if m.closed {
		m.mutex.Unlock()
		_ = client.Close()
		return nil, fmt.Errorf("manager closed while %s was connecting", serverName)
	}
	m.clients[serverName] = client
	m.mutex.Unlock()
	return client, nil
}

// findServerConfig is the loaded config named serverName, or nil. Called with
// m.mutex held.
func (m *Manager) findServerConfig(serverName string) *ServerConfig {
	for _, config := range m.config.GetLoadedServers() {
		if config.Name == serverName {
			cfg := config // Create a copy to get a stable address
			return &cfg
		}
	}
	return nil
}

// GetClient returns an existing client by server name
func (m *Manager) GetClient(serverName string) (*Client, bool) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	client, exists := m.clients[serverName]
	return client, exists
}

// ListClients returns all active clients
func (m *Manager) ListClients() map[string]*Client {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	clients := make(map[string]*Client)
	for name, client := range m.clients {
		clients[name] = client
	}
	return clients
}

// StopServer stops an MCP server and closes its client connection
func (m *Manager) StopServer(serverName string) error {
	m.mutex.Lock()
	client, exists := m.clients[serverName]
	delete(m.clients, serverName)
	m.mutex.Unlock()

	if !exists {
		return fmt.Errorf("server not found: %s", serverName)
	}
	return client.Close()
}

// Close closes all MCP clients
func (m *Manager) Close() error {
	m.mutex.Lock()
	m.closed = true
	clients := m.clients
	m.clients = make(map[string]*Client)
	m.mutex.Unlock()

	var lastError error
	for name, client := range clients {
		if err := client.Close(); err != nil {
			lastError = fmt.Errorf("failed to close client %s: %w", name, err)
		}
	}
	return lastError
}

// ========================================
// Agent Layer Helper Methods
// ========================================

// AgentToolInfo represents tool information for agent layer
type AgentToolInfo struct {
	Name        string                 `json:"name"`                   // Full prefixed name (mcp_server_tool)
	ServerName  string                 `json:"server_name"`            // Server that provides this tool
	ActualName  string                 `json:"actual_name"`            // Actual tool name on server (without prefix)
	Description string                 `json:"description"`            // Tool description
	Parameters  []string               `json:"parameters,omitempty"`   // List of parameter names (for backward compatibility)
	InputSchema map[string]interface{} `json:"input_schema,omitempty"` // Full parameter schema for LLM
}

// GetAvailableTools returns structured information about all available tools
func (m *Manager) GetAvailableTools(ctx context.Context) []AgentToolInfo {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	var tools []AgentToolInfo

	for serverName, client := range m.clients {
		if client == nil || !client.IsConnected() {
			continue
		}

		serverTools := client.GetTools()
		for toolName, tool := range serverTools {
			// Extract parameters from InputSchema
			var params []string
			var inputSchema map[string]interface{}

			if tool.InputSchema != nil {
				// Convert InputSchema to map for easier access
				if schemaBytes, err := json.Marshal(tool.InputSchema); err == nil {
					var schemaMap map[string]interface{}
					if err := json.Unmarshal(schemaBytes, &schemaMap); err == nil {
						inputSchema = schemaMap
						if props, ok := schemaMap["properties"].(map[string]interface{}); ok {
							for paramName := range props {
								params = append(params, paramName)
							}
						}
					}
				}
			}

			tools = append(tools, AgentToolInfo{
				Name:        fmt.Sprintf("mcp_%s_%s", serverName, toolName),
				ServerName:  serverName,
				ActualName:  toolName,
				Description: tool.Description,
				Parameters:  params,
				InputSchema: inputSchema,
			})
		}
	}

	return tools
}

// GetServerCount returns the count of connected MCP servers
func (m *Manager) GetServerCount() int {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	count := 0
	for _, client := range m.clients {
		if client != nil && client.IsConnected() {
			count++
		}
	}
	return count
}

// GetToolsDescription returns a formatted string description of all available tools
func (m *Manager) GetToolsDescription(ctx context.Context) string {
	tools := m.GetAvailableTools(ctx)

	if len(tools) == 0 {
		return "No MCP tools available"
	}

	var descriptions []string
	for _, tool := range tools {
		desc := fmt.Sprintf("- %s: %s", tool.Name, tool.Description)
		if len(tool.Parameters) > 0 {
			desc += fmt.Sprintf(" | Parameters: %s", strings.Join(tool.Parameters, ", "))
		}
		descriptions = append(descriptions, desc)
	}

	return strings.Join(descriptions, "\n")
}

// FindToolProvider finds which server provides a tool and returns the server info
func (m *Manager) FindToolProvider(ctx context.Context, toolName string) (*AgentToolInfo, *Client, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	// Handle both formats: "mcp_server_tool" and "tool"
	for serverName, client := range m.clients {
		if client == nil || !client.IsConnected() {
			continue
		}

		tools := client.GetTools()
		for actualToolName, tool := range tools {
			// Check exact match
			if actualToolName == toolName {
				return &AgentToolInfo{
					Name:        fmt.Sprintf("mcp_%s_%s", serverName, actualToolName),
					ServerName:  serverName,
					ActualName:  actualToolName,
					Description: tool.Description,
				}, client, nil
			}

			// Check prefixed format match (mcp_server_tool)
			prefixedName := fmt.Sprintf("mcp_%s_%s", serverName, actualToolName)
			if prefixedName == toolName {
				return &AgentToolInfo{
					Name:        prefixedName,
					ServerName:  serverName,
					ActualName:  actualToolName,
					Description: tool.Description,
				}, client, nil
			}
		}
	}

	return nil, nil, fmt.Errorf("tool %s not found in any connected MCP server", toolName)
}

// CallTool finds the appropriate server and calls the tool
func (m *Manager) CallTool(ctx context.Context, toolName string, arguments map[string]interface{}) (*ToolResult, error) {
	// First try to find in already connected clients
	toolInfo, client, err := m.FindToolProvider(ctx, toolName)
	if err != nil {
		// Tool not found in connected clients, try to start the server on-demand
		// Extract server name from tool name (format: mcp_server_tool or server_tool)
		serverName := m.extractServerNameFromTool(toolName)
		if serverName == "" {
			return nil, fmt.Errorf("tool %s not found in any connected MCP server", toolName)
		}

		// Try to start the server
		_, startErr := m.StartServer(ctx, serverName)
		if startErr != nil {
			return nil, fmt.Errorf("tool %s not found, failed to start server %s: %w", toolName, serverName, startErr)
		}

		// Try again to find the tool
		toolInfo, client, err = m.FindToolProvider(ctx, toolName)
		if err != nil {
			return nil, fmt.Errorf("tool %s still not found after starting server %s: %w", toolName, serverName, err)
		}
	}

	// Call the tool with its actual name (without prefix)
	return client.CallTool(ctx, toolInfo.ActualName, arguments)
}

// extractServerNameFromTool extracts server name from tool name
// Handles formats: "mcp_server_tool", "server_tool", "tool"
func (m *Manager) extractServerNameFromTool(toolName string) string {
	// Try mcp_server_tool format first
	if strings.HasPrefix(toolName, "mcp_") {
		parts := strings.SplitN(toolName, "_", 3) // mcp_server_tool
		if len(parts) >= 2 {
			return parts[1]
		}
	}

	// Check if it matches any configured server's tools
	m.mutex.RLock()
	loadedServers := m.config.GetLoadedServers()
	m.mutex.RUnlock()

	for _, serverConfig := range loadedServers {
		expectedPrefix := fmt.Sprintf("mcp_%s_", serverConfig.Name)
		if strings.HasPrefix(toolName, expectedPrefix) {
			return serverConfig.Name
		}
	}

	return ""
}
