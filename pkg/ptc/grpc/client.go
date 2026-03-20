package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
	pb "github.com/liliang-cn/agent-go/v2/pkg/ptc/grpc/pb"
)

// Client is a gRPC client for the PTC service
type Client struct {
	conn   *grpc.ClientConn
	client pb.PTCServiceClient
}

// NewClient creates a new gRPC client
func NewClient(address string) (*Client, error) {
	var conn *grpc.ClientConn
	var err error

	if isUDS(address) {
		// Unix domain socket
		socketPath := parseUDSPath(address)
		conn, err = grpc.Dial(
			socketPath,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", addr)
			}),
		)
	} else {
		// TCP
		conn, err = grpc.Dial(
			address,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	return &Client{
		conn:   conn,
		client: pb.NewPTCServiceClient(conn),
	}, nil
}

// Execute executes code in the sandbox
func (c *Client) Execute(ctx context.Context, req *ptc.ExecutionRequest) (*ptc.ExecutionResult, error) {
	// Build gRPC request
	grpcReq := &pb.ExecuteRequest{
		Code:        req.Code,
		Language:    string(req.Language),
		Tools:       req.Tools,
		TimeoutMs:   int32(req.Timeout.Milliseconds()),
		MaxMemoryMb: int32(req.MaxMemoryMB),
		Context:     make(map[string]string),
	}

	// Serialize context
	for k, v := range req.Context {
		b, err := json.Marshal(v)
		if err != nil {
			grpcReq.Context[k] = fmt.Sprintf("%v", v)
		} else {
			grpcReq.Context[k] = string(b)
		}
	}

	// Call gRPC
	resp, err := c.client.Execute(ctx, grpcReq)
	if err != nil {
		return nil, fmt.Errorf("execute failed: %w", err)
	}

	// Build result
	result := &ptc.ExecutionResult{
		ID:        resp.ExecutionId,
		Success:   resp.Success,
		Error:     resp.Error,
		Logs:      resp.Logs,
		Duration:  time.Duration(resp.DurationMs) * time.Millisecond,
		ToolCalls: make([]ptc.ToolCallRecord, 0),
	}

	// Parse output
	if resp.Output != "" {
		var output interface{}
		if err := json.Unmarshal([]byte(resp.Output), &output); err == nil {
			result.Output = output
		} else {
			result.Output = resp.Output
		}
	}

	// Parse return value
	if resp.ReturnValue != "" {
		var returnValue interface{}
		if err := json.Unmarshal([]byte(resp.ReturnValue), &returnValue); err == nil {
			result.ReturnValue = returnValue
		}
	}

	// Parse tool calls
	for _, tc := range resp.ToolCalls {
		toolCall := ptc.ToolCallRecord{
			ToolName: tc.ToolName,
			Error:    tc.Error,
			Duration: time.Duration(tc.DurationMs) * time.Millisecond,
		}

		if tc.Arguments != "" {
			_ = json.Unmarshal([]byte(tc.Arguments), &toolCall.Arguments)
		}
		if tc.Result != "" {
			_ = json.Unmarshal([]byte(tc.Result), &toolCall.Result)
		}

		result.ToolCalls = append(result.ToolCalls, toolCall)
	}

	return result, nil
}

// ExecuteSimple executes simple JavaScript code
func (c *Client) ExecuteSimple(ctx context.Context, code string) (*ptc.ExecutionResult, error) {
	return c.Execute(ctx, &ptc.ExecutionRequest{
		Code:     code,
		Language: ptc.LanguageJavaScript,
	})
}

// ListTools lists available tools
func (c *Client) ListTools(ctx context.Context, category string) ([]ptc.ToolInfo, error) {
	resp, err := c.client.ListTools(ctx, &pb.ListToolsRequest{
		Category: category,
	})
	if err != nil {
		return nil, fmt.Errorf("list tools failed: %w", err)
	}

	tools := make([]ptc.ToolInfo, 0, len(resp.Tools))
	for _, t := range resp.Tools {
		tool := ptc.ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			Category:    t.Category,
		}

		if t.Parameters != "" {
			_ = json.Unmarshal([]byte(t.Parameters), &tool.Parameters)
		}

		tools = append(tools, tool)
	}

	return tools, nil
}

// GetToolInfo gets information about a specific tool
func (c *Client) GetToolInfo(ctx context.Context, name string) (*ptc.ToolInfo, error) {
	resp, err := c.client.GetToolInfo(ctx, &pb.GetToolInfoRequest{
		Name: name,
	})
	if err != nil {
		return nil, fmt.Errorf("get tool info failed: %w", err)
	}

	if !resp.Exists {
		return nil, ptc.ErrToolNotFound
	}

	info := &ptc.ToolInfo{
		Name:        resp.Tool.Name,
		Description: resp.Tool.Description,
		Category:    resp.Tool.Category,
	}

	if resp.Tool.Parameters != "" {
		_ = json.Unmarshal([]byte(resp.Tool.Parameters), &info.Parameters)
	}

	return info, nil
}

// Close closes the client connection
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
