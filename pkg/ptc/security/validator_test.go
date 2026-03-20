package security

import (
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
)

func TestValidator_Validate(t *testing.T) {
	config := &ptc.SecurityConfig{
		ValidateCode: true,
		ForbiddenPatterns: []string{
			`eval\s*\(`,
			`Function\s*\(`,
		},
	}

	validator, err := NewValidator(config)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	tests := []struct {
		name    string
		code    string
		wantErr bool
	}{
		{
			name:    "safe code",
			code:    "1 + 1",
			wantErr: false,
		},
		{
			name:    "console.log is safe",
			code:    "console.log('hello')",
			wantErr: false,
		},
		{
			name:    "eval is forbidden",
			code:    "eval('1 + 1')",
			wantErr: true,
		},
		{
			name:    "Function constructor is forbidden",
			code:    "new Function('return 1')",
			wantErr: true,
		},
		{
			name:    "require is forbidden",
			code:    "require('fs')",
			wantErr: true,
		},
		{
			name:    "process is forbidden",
			code:    "process.env",
			wantErr: true,
		},
		{
			name:    "__proto__ is forbidden",
			code:    "obj.__proto__",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.Validate(tt.code)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidator_ValidateToolAccess(t *testing.T) {
	config := &ptc.SecurityConfig{
		AllowedTools: []string{"rag_query", "rag_list"},
		BlockedTools: []string{"dangerous_tool"},
	}

	validator, err := NewValidator(config)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	tests := []struct {
		name     string
		toolName string
		wantErr  bool
	}{
		{
			name:     "allowed tool",
			toolName: "rag_query",
			wantErr:  false,
		},
		{
			name:     "blocked tool",
			toolName: "dangerous_tool",
			wantErr:  true,
		},
		{
			name:     "not in allowed list",
			toolName: "other_tool",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.ValidateToolAccess(tt.toolName)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateToolAccess() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidator_AddPattern(t *testing.T) {
	config := &ptc.SecurityConfig{
		ValidateCode:      true,
		ForbiddenPatterns: []string{},
	}

	validator, err := NewValidator(config)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	// Add a new pattern
	if err := validator.AddPattern(`dangerousFunc`); err != nil {
		t.Fatalf("failed to add pattern: %v", err)
	}

	// Code with the pattern should now be rejected
	if err := validator.Validate("dangerousFunc()"); err == nil {
		t.Error("expected validation error for dangerousFunc")
	}
}

func TestValidator_ValidateDisabled(t *testing.T) {
	config := &ptc.SecurityConfig{
		ValidateCode: false,
		ForbiddenPatterns: []string{
			`eval\s*\(`,
		},
	}

	validator, err := NewValidator(config)
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	// Even dangerous code should pass when validation is disabled
	if err := validator.Validate("eval('1 + 1')"); err != nil {
		t.Errorf("expected no error when validation is disabled, got: %v", err)
	}
}
