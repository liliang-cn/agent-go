package agent

import (
	"context"

	"github.com/liliang-cn/agent-go/v2/pkg/fileproc"
)

// Built-in document-reading tool. It lets an agent read the *content* of Word,
// Excel, PowerPoint, PDF, image, and plain-text files — CGO-free, no OCR. The
// heavy parsing lives in pkg/fileproc; this file keeps pkg/agent's dependency on
// it thin (one tool registration). Registration mirrors RegisterDateTimeTool /
// RegisterGraphRecallTool: opt-in, guarded against double registration, and a
// stable {ok,data-fields,error} return shape.

const readDocumentToolDescription = "读取本地文档文件的内容并返回纯文本，支持 Word(.docx)、Excel(.xlsx)、" +
	"PowerPoint(.pptx)、PDF、图片(png/jpg/gif/webp/bmp/tiff)以及纯文本。图片不做 OCR，只返回尺寸等元数据。" +
	"当用户让你\"看/读/总结/提取某个文件里的内容\"时调用。path 为文件路径（若挂载了沙箱工作区则是工作区相对路径）。"

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
		},
		"required": []string{"path"},
	}
}

// RegisterFileTools registers the built-in `read_document` tool on a service.
// It is read-only and concurrency-safe. Opt in per agent that needs to read
// document files:
//
//	svc, _ := agent.New("assistant").Build()
//	agent.RegisterFileTools(svc)
//
// When a sandbox is attached (WithSandbox), path is resolved through the
// sandbox workspace (jailed reads); otherwise path is treated as a host path.
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
			return ReadDocument(ctx, svc, toolArgString(args, "path"), toolArgInt(args, "max_chars")), nil
		},
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel},
	)
}

// ReadDocument is the logic behind the read_document tool, exposed so callers
// can invoke it directly (e.g. offline, without an LLM). It resolves path
// through the service's sandbox workspace when one is attached (jailed reads,
// works for local and container backends), otherwise it reads the host path.
// maxChars <= 0 falls back to the 20000-char default. It never returns an
// error — failures surface as {ok:false, error:...} in the stable result map:
// {ok, format, text, chars, truncated, metadata} on success.
func ReadDocument(ctx context.Context, svc *Service, path string, maxChars int) map[string]interface{} {
	if path == "" {
		return map[string]interface{}{"ok": false, "error": "path is required"}
	}
	if maxChars <= 0 {
		maxChars = 20000
	}

	var (
		doc *fileproc.Document
		err error
	)
	// If a sandbox is attached, read the bytes through it so the path is jailed
	// to the workspace and works for both local and container backends; the file
	// name still drives format detection.
	if svc != nil && svc.Sandbox() != nil {
		var data []byte
		data, err = svc.Sandbox().ReadFile(ctx, path)
		if err == nil {
			doc, err = fileproc.ExtractBytes(ctx, path, data, fileproc.WithMaxChars(maxChars))
		}
	} else {
		doc, err = fileproc.Extract(ctx, path, fileproc.WithMaxChars(maxChars))
	}
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error()}
	}
	return map[string]interface{}{
		"ok":        true,
		"format":    doc.Format,
		"text":      doc.Text,
		"chars":     len([]rune(doc.Text)),
		"truncated": doc.Truncated,
		"metadata":  doc.Metadata,
	}
}
