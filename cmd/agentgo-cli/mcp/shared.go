package mcp

import (
	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

// Variables exported from parent package
var (
	Cfg     *config.Config
	Verbose bool
	Quiet   bool
)

// SetSharedVariables sets the shared variables from the parent package
func SetSharedVariables(config *config.Config, verbose, quiet bool) {
	Cfg = config
	Verbose = verbose
	Quiet = quiet
}
