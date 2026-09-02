// Package logging is the activity log as an extension.
//
// It adds nothing to agent.NewActivityLog; it exists so that "log this run"
// is one entry in WithExtensions next to the other concerns a run carries,
// rather than a separate WithObserver call the reader has to connect.
package logging

import (
	"io"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

// Extension narrates a run to a writer: one flat, greppable line per model
// turn, tool call, sub-agent, retry, compaction and checkpoint.
type Extension struct {
	*agent.ActivityLog
}

// New returns the extension writing to w.
func New(w io.Writer) *Extension {
	return &Extension{ActivityLog: agent.NewActivityLog(w)}
}

// Name implements agent.Extension.
func (e *Extension) Name() string { return "logging" }
