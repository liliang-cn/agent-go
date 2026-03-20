package agent

import (
	"context"
	"reflect"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// ── schemaFromStruct tests ───────────────────────────────────────────────────

type flatParams struct {
	Location string  `json:"location" desc:"City name" required:"true"`
	Units    string  `json:"units"    desc:"celsius or fahrenheit" enum:"celsius,fahrenheit"`
	Limit    int     `json:"limit"    desc:"Max results" minimum:"1" maximum:"100"`
	Verbose  bool    `json:"verbose"`
	Score    float64 `json:"score"    minimum:"0.0" maximum:"1.0"`
}

func TestSchemaFromStruct_FlatParams(t *testing.T) {
	schema := schemaFromStruct(reflect.TypeOf(flatParams{}))

	if schema["type"] != "object" {
		t.Errorf("expected type=object, got %v", schema["type"])
	}

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("properties is not map[string]interface{}")
	}

	// location
	loc := props["location"].(map[string]interface{})
	if loc["type"] != "string" {
		t.Errorf("location type: expected string, got %v", loc["type"])
	}
	if loc["description"] != "City name" {
		t.Errorf("location description: expected 'City name', got %v", loc["description"])
	}

	// required list
	req, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("required is not []string")
	}
	if len(req) != 1 || req[0] != "location" {
		t.Errorf("expected required=[location], got %v", req)
	}

	// units enum
	units := props["units"].(map[string]interface{})
	enumVals, ok := units["enum"].([]interface{})
	if !ok || len(enumVals) != 2 {
		t.Errorf("units enum: expected 2 values, got %v", units["enum"])
	}

	// limit integer constraints
	limit := props["limit"].(map[string]interface{})
	if limit["type"] != "integer" {
		t.Errorf("limit type: expected integer, got %v", limit["type"])
	}
	if limit["minimum"] != float64(1) {
		t.Errorf("limit minimum: expected 1.0, got %v", limit["minimum"])
	}
	if limit["maximum"] != float64(100) {
		t.Errorf("limit maximum: expected 100.0, got %v", limit["maximum"])
	}

	// verbose bool
	verbose := props["verbose"].(map[string]interface{})
	if verbose["type"] != "boolean" {
		t.Errorf("verbose type: expected boolean, got %v", verbose["type"])
	}

	// score float
	score := props["score"].(map[string]interface{})
	if score["type"] != "number" {
		t.Errorf("score type: expected number, got %v", score["type"])
	}
}

type noTagParams struct {
	Ignored        string
	ExportedNoJSON string `json:"-"`
}

func TestSchemaFromStruct_SkipsUntaggedAndDashFields(t *testing.T) {
	schema := schemaFromStruct(reflect.TypeOf(noTagParams{}))
	props := schema["properties"].(map[string]interface{})
	if len(props) != 0 {
		t.Errorf("expected 0 properties, got %v", props)
	}
	if _, hasRequired := schema["required"]; hasRequired {
		t.Error("expected no required field")
	}
}

// ── NewTool generic constructor ─────────────────────────────────────────────

type weatherParams struct {
	Location string `json:"location" required:"true"`
	Units    string `json:"units"    enum:"celsius,fahrenheit"`
}

func TestNewTool_SchemaGeneration(t *testing.T) {
	tool := NewTool("get_weather", "Get weather",
		func(_ context.Context, p *weatherParams) (any, error) {
			return p.Location + "/" + p.Units, nil
		},
	)

	if tool.Name() != "get_weather" {
		t.Errorf("name: expected get_weather, got %q", tool.Name())
	}
	if tool.Description() != "Get weather" {
		t.Errorf("description mismatch")
	}

	schema := tool.Parameters()
	if schema["type"] != "object" {
		t.Errorf("schema type mismatch")
	}
	props := schema["properties"].(map[string]interface{})
	if _, ok := props["location"]; !ok {
		t.Error("expected location property")
	}
}

func TestNewTool_HandlerInvocation(t *testing.T) {
	tool := NewTool("echo", "Echo location",
		func(_ context.Context, p *weatherParams) (any, error) {
			return p.Location, nil
		},
	)

	result, err := tool.Handler()(context.Background(), map[string]interface{}{
		"location": "Tokyo",
		"units":    "celsius",
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if result != "Tokyo" {
		t.Errorf("expected Tokyo, got %v", result)
	}
}

// ── ToolBuilder tests ────────────────────────────────────────────────────────

func TestToolBuilder_Basic(t *testing.T) {
	tool := BuildTool("search").
		Description("Search for things").
		Param("query", TypeString, "Search query", Required()).
		Param("limit", TypeInteger, "Max results", Min(1), Max(50)).
		Handler(func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			return args["query"], nil
		}).
		Build()

	if tool.Name() != "search" {
		t.Errorf("name mismatch")
	}

	schema := tool.Parameters()
	props := schema["properties"].(map[string]interface{})

	query := props["query"].(map[string]interface{})
	if query["type"] != "string" {
		t.Errorf("query type mismatch")
	}

	limit := props["limit"].(map[string]interface{})
	if limit["minimum"] != float64(1) {
		t.Errorf("limit minimum mismatch: %v", limit["minimum"])
	}
	if limit["maximum"] != float64(50) {
		t.Errorf("limit maximum mismatch: %v", limit["maximum"])
	}

	req := schema["required"].([]string)
	if len(req) != 1 || req[0] != "query" {
		t.Errorf("required mismatch: %v", req)
	}
}

func TestToolBuilder_Enum(t *testing.T) {
	tool := BuildTool("classify").
		Description("Classify sentiment").
		Param("sentiment", TypeString, "Sentiment label", Enum("positive", "neutral", "negative")).
		Handler(func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
			return nil, nil
		}).
		Build()

	props := tool.Parameters()["properties"].(map[string]interface{})
	sent := props["sentiment"].(map[string]interface{})
	enum, ok := sent["enum"].([]interface{})
	if !ok || len(enum) != 3 {
		t.Errorf("enum mismatch: %v", sent["enum"])
	}
}

func TestToolBuilder_Items(t *testing.T) {
	tool := BuildTool("batch").
		Description("Process list").
		Param("ids", TypeArray, "List of IDs", Items(TypeString), Required()).
		Handler(func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
			return nil, nil
		}).
		Build()

	props := tool.Parameters()["properties"].(map[string]interface{})
	ids := props["ids"].(map[string]interface{})
	items, ok := ids["items"].(map[string]interface{})
	if !ok {
		t.Fatal("items missing")
	}
	if items["type"] != "string" {
		t.Errorf("items type mismatch: %v", items["type"])
	}
}

func TestSearchDeferredToolsBM25(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "mcp_filesystem_write_file",
			Description: "Write a file into the workspace",
		},
		DeferLoading: true,
	}, nil, CategoryMCP)
	registry.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "mcp_filesystem_list_directory",
			Description: "List files in a directory",
		},
		DeferLoading: true,
	}, nil, CategoryMCP)

	results := registry.SearchDeferredToolsBM25("write golang file to workspace")
	if len(results) == 0 {
		t.Fatal("expected bm25 tool search results")
	}
	if results[0].Function.Name != "mcp_filesystem_write_file" {
		t.Fatalf("expected write_file first, got %#v", results)
	}
}

func TestExecuteToolSearchBM25(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "mcp_filesystem_write_file",
			Description: "Write a file into the workspace",
		},
		DeferLoading: true,
	}, nil, CategoryMCP)
	registry.Register(domain.ToolDefinition{
		Type: "function",
		Function: domain.ToolFunction{
			Name:        "mcp_filesystem_read_file",
			Description: "Read a file from disk",
		},
		DeferLoading: true,
	}, nil, CategoryMCP)

	results, err := registry.ExecuteToolSearch("save a go file in workspace", "bm25")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected bm25 execute search results")
	}
	if results[0].Function.Name != "mcp_filesystem_write_file" {
		t.Fatalf("expected write_file first, got %#v", results)
	}
}

func TestResolveClosestToolName(t *testing.T) {
	candidates := []string{
		"mcp_filesystem_list_directory",
		"mcp_filesystem_write_file",
		"mcp_filesystem_search_files",
	}

	got := resolveClosestToolName("mcp_filesystem_listFiles", candidates)
	if got != "mcp_filesystem_list_directory" {
		t.Fatalf("resolveClosestToolName() = %q, want %q", got, "mcp_filesystem_list_directory")
	}
}

func TestToolBuilder_PanicOnMissingHandler(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for missing handler")
		}
	}()
	BuildTool("no_handler").Build()
}

func TestToolBuilder_PanicOnEmptyName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for empty name")
		}
	}()
	BuildTool("").
		Handler(func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil }).
		Build()
}

// ── NewTool pointer field handling ───────────────────────────────────────────

type optionalNumericParams struct {
	From        string   `json:"from"          required:"true"`
	To          string   `json:"to"            required:"true"`
	MaxPriceUSD *float64 `json:"max_price_usd" desc:"Optional max price"`
}

func TestNewTool_PointerFieldNilWhenAbsent(t *testing.T) {
	// Verify that *float64 produces "number" in the schema (not "pointer" or similar).
	tool := NewTool("search", "Search",
		func(_ context.Context, p *optionalNumericParams) (any, error) {
			return p, nil
		},
	)
	props := tool.Parameters()["properties"].(map[string]interface{})
	maxPrice := props["max_price_usd"].(map[string]interface{})
	if maxPrice["type"] != "number" {
		t.Errorf("expected *float64 to produce schema type 'number', got %v", maxPrice["type"])
	}

	// When the field is absent from the input args, the pointer should be nil.
	result, err := tool.Handler()(context.Background(), map[string]interface{}{
		"from": "beijing",
		"to":   "tokyo",
		// max_price_usd intentionally omitted
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	p := result.(*optionalNumericParams)
	if p.MaxPriceUSD != nil {
		t.Errorf("expected MaxPriceUSD to be nil when absent, got %v", *p.MaxPriceUSD)
	}

	// When the field is present, the pointer should be non-nil with the correct value.
	result2, err := tool.Handler()(context.Background(), map[string]interface{}{
		"from":          "beijing",
		"to":            "tokyo",
		"max_price_usd": float64(500),
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	p2 := result2.(*optionalNumericParams)
	if p2.MaxPriceUSD == nil {
		t.Fatal("expected MaxPriceUSD to be non-nil when provided")
	}
	if *p2.MaxPriceUSD != 500 {
		t.Errorf("expected MaxPriceUSD=500, got %v", *p2.MaxPriceUSD)
	}
}

// ── toToolDefinition ─────────────────────────────────────────────────────────

func TestTool_ToToolDefinition(t *testing.T) {
	tool := BuildTool("ping").
		Description("Ping the server").
		Handler(func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return "pong", nil }).
		Build()

	def := tool.toToolDefinition()
	if def.Type != "function" {
		t.Errorf("type mismatch: %v", def.Type)
	}
	if def.Function.Name != "ping" {
		t.Errorf("function name mismatch: %v", def.Function.Name)
	}
	if def.Function.Description != "Ping the server" {
		t.Errorf("function description mismatch")
	}
	if def.Function.Parameters == nil {
		t.Error("parameters should not be nil")
	}
}
