package mcp

import (
	"context"
	"fmt"
	"io"
	"log"

	websearchserver "github.com/liliang-cn/agent-go/v2/pkg/mcp/builtins/websearch"
	"github.com/mark3labs/mcp-filesystem-server/filesystemserver"
	mcpgo_server "github.com/mark3labs/mcp-go/server"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// createInProcessTransport creates an in-process transport for supported servers
// It uses io.Pipe to bridge the MCP Server and the official SDK MCP Client in-memory.
func (c *Client) createInProcessTransport(ctx context.Context) (mcpsdk.Transport, error) {
	switch c.config.Name {
	case "filesystem", "builtin_filesystem":
		return c.createFilesystemTransport(ctx)
	case "websearch", "builtin_websearch":
		return c.createWebsearchTransport(ctx)
	}

	return nil, fmt.Errorf("unsupported in-process server name: %s", c.config.Name)
}

// createFilesystemTransport creates an in-process transport for the filesystem server
func (c *Client) createFilesystemTransport(ctx context.Context) (mcpsdk.Transport, error) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()

	allowedDirs := []string{"/"}
	if len(c.config.Args) > 0 {
		allowedDirs = c.config.Args
	}

	fss, err := filesystemserver.NewFilesystemServer(allowedDirs)
	if err != nil {
		return nil, fmt.Errorf("failed to create in-process filesystem server: %w", err)
	}

	go func() {
		stdioServer := mcpgo_server.NewStdioServer(fss)
		err := stdioServer.Listen(ctx, serverRead, serverWrite)
		if err != nil && err != io.EOF && err != io.ErrClosedPipe {
			log.Printf("[WARN] In-process filesystem server error: %v", err)
		}
		clientRead.Close()
		clientWrite.Close()
	}()

	transport := &mcpsdk.IOTransport{
		Reader: clientRead,
		Writer: clientWrite,
	}

	return transport, nil
}

// createWebsearchTransport creates an in-process transport for the websearch server
func (c *Client) createWebsearchTransport(ctx context.Context) (mcpsdk.Transport, error) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()

	// Create the embedded websearch MCP server
	websearchServer, err := websearchserver.NewServer()
	if err != nil {
		return nil, fmt.Errorf("failed to create in-process websearch server: %w", err)
	}

	go func() {
		stdioServer := mcpgo_server.NewStdioServer(websearchServer.GetMCPServer())
		err := stdioServer.Listen(ctx, serverRead, serverWrite)
		if err != nil && err != io.EOF && err != io.ErrClosedPipe {
			log.Printf("[WARN] In-process websearch server error: %v", err)
		}
		clientRead.Close()
		clientWrite.Close()
	}()

	transport := &mcpsdk.IOTransport{
		Reader: clientRead,
		Writer: clientWrite,
	}

	return transport, nil
}
