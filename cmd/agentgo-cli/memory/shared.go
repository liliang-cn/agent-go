package memory

import (
	"github.com/liliang-cn/agent-go/v2/pkg/config"
)

// Shared variables
var (
	Cfg     *config.Config
	Verbose bool
)

// SetSharedVariables sets the shared variables from the parent package
func SetSharedVariables(cfg *config.Config, verbose bool) {
	Cfg = cfg
	Verbose = verbose
}
