package agent

import (
	"context"
	"path"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
)

// Built-in deliverables tooling: at (or near) the end of a long task, scan the
// sandbox workspace and report the files the agent produced, so the caller knows
// what was actually created. Type is inferred from the file extension.

// Deliverable is a single produced file in the sandbox workspace.
type Deliverable struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Type string `json:"type"`
}

// deliverableSnapshotName is the snapshot tarball Snapshot() may leave around; we
// skip it so it never shows up as a "deliverable".
const deliverableSnapshotName = "snapshot.tar.gz"

// deliverableType infers a coarse type from a filename's extension.
func deliverableType(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	switch ext {
	case "":
		return "file"
	default:
		return ext
	}
}

// ScanDeliverables walks the sandbox workspace recursively and returns every file
// (not directory) found, with an inferred type. The snapshot tarball, if present,
// is skipped. Returns an empty slice (not nil) when the workspace is empty.
func ScanDeliverables(ctx context.Context, sb sandbox.Sandbox) ([]Deliverable, error) {
	if sb == nil {
		return []Deliverable{}, nil
	}
	out := make([]Deliverable, 0, 8)
	// Iterative DFS over directories using sb.List so we honor the sandbox jail.
	dirs := []string{"."}
	for len(dirs) > 0 {
		dir := dirs[len(dirs)-1]
		dirs = dirs[:len(dirs)-1]

		infos, err := sb.List(ctx, dir)
		if err != nil {
			return nil, err
		}
		for _, fi := range infos {
			if fi.IsDir {
				dirs = append(dirs, fi.Path)
				continue
			}
			if path.Base(fi.Path) == deliverableSnapshotName {
				continue
			}
			out = append(out, Deliverable{
				Path: fi.Path,
				Size: fi.Size,
				Type: deliverableType(fi.Name),
			})
		}
	}
	return out, nil
}

// RegisterDeliverableTools registers the `list_deliverables` tool, which reports
// the files produced in the sandbox workspace. No-op if svc or sb is nil.
//
//	svc, _ := agent.New("assistant").Build()
//	agent.RegisterDeliverableTools(svc, sb)
func RegisterDeliverableTools(svc *Service, sb sandbox.Sandbox) {
	if svc == nil || sb == nil {
		return
	}
	if svc.toolRegistry != nil && svc.toolRegistry.Has("list_deliverables") {
		return
	}
	svc.AddToolWithMetadata(
		"list_deliverables",
		"扫描沙箱工作区,列出已产出的文件及其 {path,size,type}(type 按扩展名推断,如 md/txt/json/png)。用于在任务结束时汇总交付物。",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			items, err := ScanDeliverables(ctx, sb)
			if err != nil {
				return toolErr(err.Error()), nil
			}
			payload := make([]map[string]interface{}, 0, len(items))
			for _, d := range items {
				payload = append(payload, map[string]interface{}{
					"path": d.Path, "size": d.Size, "type": d.Type,
				})
			}
			return toolOK(map[string]interface{}{"deliverables": payload}), nil
		},
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel},
	)
}
