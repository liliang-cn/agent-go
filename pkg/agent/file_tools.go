package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/fileproc"
)

// Built-in document tools. They let an agent read the *content* of Word, Excel,
// PowerPoint, PDF, image, and plain-text files — CGO-free, no OCR (OCR lives in
// ocr_tools.go). The heavy parsing lives in pkg/fileproc; this file keeps
// pkg/agent's dependency on it thin. Registration mirrors RegisterDateTimeTool /
// RegisterGraphRecallTool: opt-in, guarded against double registration, and a
// stable {ok,data-fields,error} return shape.

const readDocumentToolDescription = "读取本地文档文件的内容并返回纯文本，支持 Word(.docx)、Excel(.xlsx)、" +
	"PowerPoint(.pptx)、PDF、图片(png/jpg/gif/webp/bmp/tiff)以及纯文本。图片不做 OCR，只返回尺寸等元数据。" +
	"当用户让你\"看/读/总结/提取某个文件里的内容\"时调用。path 为文件路径（若挂载了沙箱工作区则是工作区相对路径）。" +
	"可用 pages 只取部分页(如 \"1-20\")，per_page 为 true 时额外返回每页文本数组 pages_text。大 PDF 会流式读取，不受大小上限限制。"

func readDocumentToolSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "要读取的文件路径（挂载沙箱时为工作区相对路径，否则为主机路径）",
			},
			"max_chars": map[string]interface{}{
				"type":        "integer",
				"description": "返回文本的最大字符数，超出则截断并置 truncated:true，默认 20000",
			},
			"pages": map[string]interface{}{
				"type":        "string",
				"description": "可选：1 起的页码选择器，如 \"1-8,50,100-120\"，仅对 pdf/pptx 生效，留空为全部",
			},
			"per_page": map[string]interface{}{
				"type":        "boolean",
				"description": "可选：为 true 时在结果中额外返回 pages_text（每页/每张幻灯片的文本数组）",
			},
		},
		"required": []string{"path"},
	}
}

const exportDocumentToolDescription = "从一个文档(path)中提取文本(可用 pages 只取部分页)，并写入到工作区内的 out_path 文件。" +
	"format=md 时每页前加 \"## Page N\" 标题；format=txt 时各页以空行分隔。适合\"把这份 PDF 第 1-50 页导成 md\"这类一步到位的需求。"

func exportDocumentToolSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "源文件路径（挂载沙箱时为工作区相对路径，否则为主机路径）",
			},
			"out_path": map[string]interface{}{
				"type":        "string",
				"description": "输出文件路径，工作区相对路径（未挂载沙箱时为主机路径）",
			},
			"pages": map[string]interface{}{
				"type":        "string",
				"description": "可选：1 起的页码选择器，如 \"1-50\"，仅对 pdf/pptx 生效，留空为全部",
			},
			"format": map[string]interface{}{
				"type":        "string",
				"description": "输出格式：\"md\"(默认，含 ## Page N 标题) 或 \"txt\"",
			},
		},
		"required": []string{"path", "out_path"},
	}
}

// RegisterFileTools registers the built-in `read_document` and `export_document`
// tools on a service. `read_document` is read-only; `export_document` writes a
// file. Both are concurrency-safe. Opt in per agent that needs document access:
//
//	svc, _ := agent.New("assistant").Build()
//	agent.RegisterFileTools(svc)
//
// When a sandbox is attached (WithSandbox), paths are resolved through the
// sandbox workspace (jailed); otherwise they are treated as host paths.
func RegisterFileTools(svc *Service) {
	if svc == nil {
		return
	}
	if svc.toolRegistry != nil && svc.toolRegistry.Has("read_document") {
		return
	}
	svc.AddToolWithMetadata(
		"read_document",
		readDocumentToolDescription,
		readDocumentToolSchema(),
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return ReadDocumentWith(ctx, svc, ReadDocumentOpts{
				Path:     toolArgString(args, "path"),
				MaxChars: toolArgInt(args, "max_chars"),
				Pages:    toolArgString(args, "pages"),
				PerPage:  toolArgBool(args, "per_page"),
			}), nil
		},
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel},
	)
	svc.AddToolWithMetadata(
		"export_document",
		exportDocumentToolDescription,
		exportDocumentToolSchema(),
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return ExportDocument(ctx, svc, ExportDocumentOpts{
				Path:    toolArgString(args, "path"),
				OutPath: toolArgString(args, "out_path"),
				Pages:   toolArgString(args, "pages"),
				Format:  toolArgString(args, "format"),
			}), nil
		},
		ToolMetadata{ReadOnly: false, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel},
	)
}

// ReadDocumentOpts configures ReadDocumentWith.
type ReadDocumentOpts struct {
	Path     string // file path (workspace-relative when a sandbox is attached, else host path)
	MaxChars int    // cap on returned Text runes; <= 0 falls back to 20000
	Pages    string // optional 1-indexed page selector ("1-20"); empty = all
	PerPage  bool   // when true, include a pages_text array in the result
}

// ReadDocument is the back-compatible entry point behind the read_document tool.
// It reads the whole document (all pages) and returns the stable result map. For
// page selection or per-page output use ReadDocumentWith.
func ReadDocument(ctx context.Context, svc *Service, path string, maxChars int) map[string]interface{} {
	return ReadDocumentWith(ctx, svc, ReadDocumentOpts{Path: path, MaxChars: maxChars})
}

// ReadDocumentWith is the logic behind the read_document tool, exposed so callers
// can invoke it directly (e.g. offline, without an LLM).
//
// Path resolution:
//   - No sandbox: fileproc.Extract on the host path (streaming for PDFs).
//   - Sandbox with a workspace root: the path is resolved to an absolute path
//     under the workspace (rejecting "../" escapes) and fileproc.Extract is
//     called on it — this streams large PDFs from disk instead of loading every
//     byte through ReadFile, so files above the 50MB cap still work.
//   - Sandbox without a resolvable workspace path (e.g. remote/container backends
//     whose files aren't on the host FS): fall back to Sandbox().ReadFile +
//     fileproc.ExtractBytes, which still honors the byte cap.
//
// It never returns an error — failures surface as {ok:false, error:...}. Success:
// {ok, format, text, chars, truncated, metadata, [pages_text]}.
func ReadDocumentWith(ctx context.Context, svc *Service, opts ReadDocumentOpts) map[string]interface{} {
	if opts.Path == "" {
		return map[string]interface{}{"ok": false, "error": "path is required"}
	}
	maxChars := opts.MaxChars
	if maxChars <= 0 {
		maxChars = 20000
	}
	extractOpts := []fileproc.Option{fileproc.WithMaxChars(maxChars)}
	if opts.Pages != "" {
		extractOpts = append(extractOpts, fileproc.WithPages(opts.Pages))
	}

	doc, err := extractDocument(ctx, svc, opts.Path, extractOpts...)
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error()}
	}
	res := map[string]interface{}{
		"ok":        true,
		"format":    doc.Format,
		"text":      doc.Text,
		"chars":     len([]rune(doc.Text)),
		"truncated": doc.Truncated,
		"metadata":  doc.Metadata,
	}
	if opts.PerPage {
		if doc.PageTexts == nil {
			res["pages_text"] = []string{}
		} else {
			res["pages_text"] = doc.PageTexts
		}
	}
	return res
}

// ExportDocumentOpts configures ExportDocument.
type ExportDocumentOpts struct {
	Path    string // source file path (workspace-relative under a sandbox, else host)
	OutPath string // destination path (workspace-relative under a sandbox, else host)
	Pages   string // optional 1-indexed page selector; empty = all
	Format  string // "md" (default) or "txt"
}

// ExportDocument extracts text from a document (honoring page selection) and
// writes it to OutPath. With a sandbox attached the write goes through
// Sandbox().WriteFile (jailed to the workspace); otherwise it writes the host
// path. It never returns an error — result: {ok, out_path, pages_written, chars,
// bytes, [error]}.
func ExportDocument(ctx context.Context, svc *Service, opts ExportDocumentOpts) map[string]interface{} {
	if opts.Path == "" {
		return map[string]interface{}{"ok": false, "error": "path is required"}
	}
	if opts.OutPath == "" {
		return map[string]interface{}{"ok": false, "error": "out_path is required"}
	}
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "md"
	}
	if format != "md" && format != "txt" {
		return map[string]interface{}{"ok": false, "error": fmt.Sprintf("unsupported format %q (want md or txt)", opts.Format)}
	}

	var extractOpts []fileproc.Option
	if opts.Pages != "" {
		extractOpts = append(extractOpts, fileproc.WithPages(opts.Pages))
	}
	doc, err := extractDocument(ctx, svc, opts.Path, extractOpts...)
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error()}
	}

	// Prefer per-page rendering when available (pdf/pptx); otherwise fall back to
	// the joined full text as a single "page".
	pages := doc.PageTexts
	if len(pages) == 0 {
		pages = []string{doc.Text}
	}
	out := renderExport(pages, format)

	if err := writeDocumentOut(ctx, svc, opts.OutPath, []byte(out)); err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error()}
	}
	return map[string]interface{}{
		"ok":            true,
		"out_path":      opts.OutPath,
		"pages_written": len(pages),
		"chars":         len([]rune(out)),
		"bytes":         len([]byte(out)),
	}
}

func renderExport(pages []string, format string) string {
	var b strings.Builder
	for i, p := range pages {
		if format == "md" {
			if i > 0 {
				b.WriteString("\n\n")
			}
			fmt.Fprintf(&b, "## Page %d\n\n", i+1)
			b.WriteString(p)
		} else { // txt
			if i > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(p)
		}
	}
	return b.String()
}

// extractDocument resolves path (sandbox-aware) and extracts a Document. It
// streams from disk (no byte cap) whenever it can resolve a host path — either
// no sandbox, or a sandbox whose workspace root maps the file onto the host FS.
func extractDocument(ctx context.Context, svc *Service, path string, opts ...fileproc.Option) (*fileproc.Document, error) {
	if svc != nil && svc.Sandbox() != nil {
		sb := svc.Sandbox()
		if abs, ok := resolveWorkspacePath(sb.Workspace(), path); ok {
			return fileproc.Extract(ctx, abs, opts...)
		}
		// Can't map onto the host FS: read bytes through the sandbox (byte-capped).
		data, err := sb.ReadFile(ctx, path)
		if err != nil {
			return nil, err
		}
		return fileproc.ExtractBytes(ctx, path, data, opts...)
	}
	return fileproc.Extract(ctx, path, opts...)
}

// writeDocumentOut writes data to outPath, through the sandbox when attached.
func writeDocumentOut(ctx context.Context, svc *Service, outPath string, data []byte) error {
	if svc != nil && svc.Sandbox() != nil {
		return svc.Sandbox().WriteFile(ctx, outPath, data, 0o644)
	}
	return writeHostFile(outPath, data)
}

// resolveWorkspacePath maps a (possibly workspace-relative) path to an absolute
// host path under workspace, returning false if workspace is empty, doesn't
// exist on the host FS, or the path escapes it (via "../"). Absolute paths that
// already sit inside the workspace are accepted as-is.
func resolveWorkspacePath(workspace, path string) (string, bool) {
	if workspace == "" {
		return "", false
	}
	wsAbs, err := filepath.Abs(workspace)
	if err != nil {
		return "", false
	}
	// A workspace root that isn't a real host directory (e.g. a container path)
	// can't be used for streaming host reads.
	if !isHostDir(wsAbs) {
		return "", false
	}
	var candidate string
	if filepath.IsAbs(path) {
		candidate = filepath.Clean(path)
	} else {
		candidate = filepath.Clean(filepath.Join(wsAbs, path))
	}
	rel, err := filepath.Rel(wsAbs, candidate)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return candidate, true
}

func isHostDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// writeHostFile writes data to a host path, creating parent directories.
func writeHostFile(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}
