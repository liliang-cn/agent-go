// Package grpc provides gRPC server and client for PTC service
package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
	pb "github.com/liliang-cn/agent-go/v2/pkg/ptc/grpc/pb"
)

// Server implements the PTC gRPC service
type Server struct {
	pb.UnimplementedPTCServiceServer

	service *ptc.Service
	config  *ptc.GRPCConfig

	mu     sync.RWMutex
	server *grpc.Server
	closed bool
}

// NewServer creates a new gRPC server
func NewServer(service *ptc.Service, config *ptc.GRPCConfig) *Server {
	return &Server{
		service: service,
		config:  config,
	}
}

// Execute executes code in the sandbox
func (s *Server) Execute(ctx context.Context, req *pb.ExecuteRequest) (*pb.ExecuteResponse, error) {
	// Build execution request
	execReq := &ptc.ExecutionRequest{
		Code:        req.Code,
		Language:    ptc.LanguageType(req.Language),
		Context:     make(map[string]interface{}),
		Tools:       req.Tools,
		Timeout:     time.Duration(req.TimeoutMs) * time.Millisecond,
		MaxMemoryMB: int(req.MaxMemoryMb),
	}

	// Parse context
	for k, v := range req.Context {
		var val interface{}
		if err := json.Unmarshal([]byte(v), &val); err != nil {
			execReq.Context[k] = v
		} else {
			execReq.Context[k] = val
		}
	}

	// Execute
	result, err := s.service.Execute(ctx, execReq)

	// Build response
	resp := &pb.ExecuteResponse{
		ExecutionId: execReq.ID,
		DurationMs:  result.Duration.Milliseconds(),
		Success:     result.Success,
		Error:       result.Error,
		Logs:        result.Logs,
	}

	// Serialize output
	if result.Output != nil {
		b, err := json.Marshal(result.Output)
		if err == nil {
			resp.Output = string(b)
		}
	}

	// Serialize return value
	if result.ReturnValue != nil {
		b, err := json.Marshal(result.ReturnValue)
		if err == nil {
			resp.ReturnValue = string(b)
		}
	}

	// Add tool calls
	for _, tc := range result.ToolCalls {
		argsJSON, _ := json.Marshal(tc.Arguments)
		resultJSON, _ := json.Marshal(tc.Result)

		resp.ToolCalls = append(resp.ToolCalls, &pb.ToolCallRecord{
			ToolName:   tc.ToolName,
			Arguments:  string(argsJSON),
			Result:     string(resultJSON),
			Error:      tc.Error,
			DurationMs: tc.Duration.Milliseconds(),
		})
	}

	if err != nil {
		return resp, status.Errorf(codes.Internal, "execution failed: %v", err)
	}

	return resp, nil
}

// ExecuteStream executes code and streams output
func (s *Server) ExecuteStream(req *pb.ExecuteRequest, stream pb.PTCService_ExecuteStreamServer) error {
	ctx := stream.Context()

	// Build execution request
	execReq := &ptc.ExecutionRequest{
		Code:        req.Code,
		Language:    ptc.LanguageType(req.Language),
		Context:     make(map[string]interface{}),
		Tools:       req.Tools,
		Timeout:     time.Duration(req.TimeoutMs) * time.Millisecond,
		MaxMemoryMB: int(req.MaxMemoryMb),
	}

	// Parse context
	for k, v := range req.Context {
		var val interface{}
		if err := json.Unmarshal([]byte(v), &val); err != nil {
			execReq.Context[k] = v
		} else {
			execReq.Context[k] = val
		}
	}

	// Execute
	result, err := s.service.Execute(ctx, execReq)

	// Stream logs
	for _, log := range result.Logs {
		if err := stream.Send(&pb.ExecuteChunk{
			Type:        pb.ExecuteChunk_LOG,
			Content:     log,
			TimestampMs: time.Now().UnixMilli(),
		}); err != nil {
			return err
		}
	}

	// Stream tool calls
	for _, tc := range result.ToolCalls {
		tcJSON, _ := json.Marshal(tc)
		if err := stream.Send(&pb.ExecuteChunk{
			Type:        pb.ExecuteChunk_TOOL_CALL,
			Content:     string(tcJSON),
			TimestampMs: time.Now().UnixMilli(),
		}); err != nil {
			return err
		}
	}

	// Stream output
	if result.Output != nil {
		outputJSON, _ := json.Marshal(result.Output)
		if err := stream.Send(&pb.ExecuteChunk{
			Type:        pb.ExecuteChunk_OUTPUT,
			Content:     string(outputJSON),
			TimestampMs: time.Now().UnixMilli(),
		}); err != nil {
			return err
		}
	}

	// Send error if any
	if result.Error != "" {
		if err := stream.Send(&pb.ExecuteChunk{
			Type:        pb.ExecuteChunk_ERROR,
			Content:     result.Error,
			TimestampMs: time.Now().UnixMilli(),
		}); err != nil {
			return err
		}
	}

	// Send done
	if err := stream.Send(&pb.ExecuteChunk{
		Type:        pb.ExecuteChunk_DONE,
		Content:     "",
		TimestampMs: time.Now().UnixMilli(),
	}); err != nil {
		return err
	}

	if err != nil {
		return status.Errorf(codes.Internal, "execution failed: %v", err)
	}

	return nil
}

// ListTools lists available tools
func (s *Server) ListTools(ctx context.Context, req *pb.ListToolsRequest) (*pb.ListToolsResponse, error) {
	tools, err := s.service.ListTools(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list tools: %v", err)
	}

	resp := &pb.ListToolsResponse{
		Tools: make([]*pb.ToolInfo, 0),
	}

	for _, tool := range tools {
		// Filter by category if specified
		if req.Category != "" && tool.Category != req.Category {
			continue
		}

		paramsJSON, _ := json.Marshal(tool.Parameters)
		resp.Tools = append(resp.Tools, &pb.ToolInfo{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  string(paramsJSON),
			Category:    tool.Category,
		})
	}

	return resp, nil
}

// GetToolInfo gets information about a specific tool
func (s *Server) GetToolInfo(ctx context.Context, req *pb.GetToolInfoRequest) (*pb.GetToolInfoResponse, error) {
	info, err := s.service.GetToolInfo(ctx, req.Name)
	if err != nil {
		return &pb.GetToolInfoResponse{
			Exists: false,
		}, nil
	}

	paramsJSON, _ := json.Marshal(info.Parameters)
	return &pb.GetToolInfoResponse{
		Exists: true,
		Tool: &pb.ToolInfo{
			Name:        info.Name,
			Description: info.Description,
			Parameters:  string(paramsJSON),
			Category:    info.Category,
		},
	}, nil
}

// Start starts the gRPC server
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("server is closed")
	}

	// Create listener
	var lis net.Listener
	var err error

	if s.config.Address != "" {
		if isUDS(s.config.Address) {
			// Unix domain socket
			socketPath := parseUDSPath(s.config.Address)
			lis, err = net.Listen("unix", socketPath)
		} else {
			// TCP
			lis, err = net.Listen("tcp", s.config.Address)
		}
	} else {
		// Default to UDS
		lis, err = net.Listen("unix", "/tmp/ptc.sock")
	}

	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	// Create gRPC server with options
	opts := []grpc.ServerOption{}
	if s.config.MaxRecvMsgSize > 0 {
		opts = append(opts, grpc.MaxRecvMsgSize(s.config.MaxRecvMsgSize))
	}
	if s.config.MaxSendMsgSize > 0 {
		opts = append(opts, grpc.MaxSendMsgSize(s.config.MaxSendMsgSize))
	}

	s.server = grpc.NewServer(opts...)
	pb.RegisterPTCServiceServer(s.server, s)

	// Start serving in a goroutine
	go func() {
		_ = s.server.Serve(lis)
	}()

	return nil
}

// Stop stops the gRPC server
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.server != nil {
		s.server.GracefulStop()
		s.server = nil
	}
	s.closed = true
}

// NewGRPCServer creates a new gRPC server (alias for NewServer)
func NewGRPCServer(service *ptc.Service, config *ptc.GRPCConfig) *Server {
	return NewServer(service, config)
}

// isUDS checks if address is a Unix domain socket
func isUDS(addr string) bool {
	return len(addr) >= 7 && addr[:7] == "unix://"
}

// parseUDSPath extracts the path from a UDS address
func parseUDSPath(addr string) string {
	if len(addr) >= 7 && addr[:7] == "unix://" {
		return addr[7:]
	}
	return addr
}
