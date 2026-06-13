package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/browser"
	"github.com/liliang-cn/agent-go/v2/pkg/sandbox"
)

// Built-in browser-control tools backed by a browser.Browser. They let an agent
// drive a real browser: navigate, read, click, type, fill, screenshot, etc.
// Downloads land in the sandbox workspace when a sandbox is provided. Mirrors the
// RegisterFetchURLTool pattern: opt-in, double-registration guarded, structured
// {ok,data,error} returns.

// RegisterBrowserTools registers the built-in browser tools on a service, backed
// by br. sb may be nil; when nil, browser_download returns an error explaining no
// workspace is configured. No-op if svc or br is nil.
//
//	svc, _ := agent.New("assistant").Build()
//	br, _ := browser.NewChromedp()
//	agent.RegisterBrowserTools(svc, br, sb)
func RegisterBrowserTools(svc *Service, br browser.Browser, sb sandbox.Sandbox) {
	if svc == nil || br == nil {
		return
	}
	has := func(name string) bool {
		return svc.toolRegistry != nil && svc.toolRegistry.Has(name)
	}
	roMeta := ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}
	destMeta := ToolMetadata{Destructive: true, InterruptBehavior: InterruptBehaviorBlock}

	// --- browser_navigate ---
	if !has("browser_navigate") {
		svc.AddToolWithMetadata(
			"browser_navigate",
			"在浏览器中打开一个 URL,返回结果页面的 {url,title}。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"url": map[string]interface{}{"type": "string", "description": "要打开的完整网址"},
				},
				"required": []string{"url"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				u := toolArgString(args, "url")
				if u == "" {
					return toolErr("url required"), nil
				}
				st, err := br.Navigate(ctx, u)
				if err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"url": st.URL, "title": st.Title}), nil
			},
			destMeta,
		)
	}

	// --- browser_back ---
	if !has("browser_back") {
		svc.AddToolWithMetadata(
			"browser_back",
			"在浏览器历史中后退一步。",
			map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				if err := br.Back(ctx); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"ok": true}), nil
			},
			destMeta,
		)
	}

	// --- browser_read ---
	if !has("browser_read") {
		svc.AddToolWithMetadata(
			"browser_read",
			"读取当前页面(或某 selector)的可见文本。selector 缺省读取 body。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"selector": map[string]interface{}{"type": "string", "description": "可选 CSS 选择器,缺省为整页 body"},
				},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				text, err := br.ReadText(ctx, toolArgString(args, "selector"))
				if err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"text": text}), nil
			},
			roMeta,
		)
	}

	// --- browser_snapshot ---
	if !has("browser_snapshot") {
		svc.AddToolWithMetadata(
			"browser_snapshot",
			"获取页面的可交互元素快照:返回 tree(可读大纲)与 elements(带 ref 的元素列表),供后续 click/type 用 ref 定位。",
			map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				snap, err := br.Snapshot(ctx)
				if err != nil {
					return toolErr(err.Error()), nil
				}
				elems := make([]map[string]interface{}, 0, len(snap.Elements))
				for _, e := range snap.Elements {
					elems = append(elems, map[string]interface{}{
						"ref": e.Ref, "role": e.Role, "name": e.Name, "selector": e.Selector,
					})
				}
				return toolOK(map[string]interface{}{"tree": snap.Tree, "elements": elems}), nil
			},
			roMeta,
		)
	}

	// --- browser_click ---
	if !has("browser_click") {
		svc.AddToolWithMetadata(
			"browser_click",
			"点击元素。ref 可为快照里的 ref(如 e1)或 CSS 选择器。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"ref": map[string]interface{}{"type": "string", "description": "元素 ref 或 CSS 选择器"},
				},
				"required": []string{"ref"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				ref := toolArgString(args, "ref")
				if ref == "" {
					return toolErr("ref required"), nil
				}
				if err := br.Click(ctx, ref); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"ref": ref, "clicked": true}), nil
			},
			destMeta,
		)
	}

	// --- browser_type ---
	if !has("browser_type") {
		svc.AddToolWithMetadata(
			"browser_type",
			"聚焦某元素并输入文本。ref 可为快照 ref 或 CSS 选择器。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"ref":  map[string]interface{}{"type": "string", "description": "元素 ref 或 CSS 选择器"},
					"text": map[string]interface{}{"type": "string", "description": "要输入的文本"},
				},
				"required": []string{"ref", "text"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				ref := toolArgString(args, "ref")
				if ref == "" {
					return toolErr("ref required"), nil
				}
				text, _ := args["text"].(string)
				if err := br.Type(ctx, ref, text); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"ref": ref, "typed": true}), nil
			},
			destMeta,
		)
	}

	// --- browser_fill ---
	if !has("browser_fill") {
		svc.AddToolWithMetadata(
			"browser_fill",
			"一次性填写多个表单字段。fields 是对象,key 为元素 ref 或 CSS 选择器,value 为要填的值。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"fields": map[string]interface{}{"type": "object", "description": "{ref或selector: 值} 的映射"},
				},
				"required": []string{"fields"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				raw, ok := args["fields"].(map[string]interface{})
				if !ok || len(raw) == 0 {
					return toolErr("fields must be a non-empty object"), nil
				}
				fields := make(map[string]string, len(raw))
				for k, v := range raw {
					fields[k] = fmt.Sprintf("%v", v)
				}
				if err := br.Fill(ctx, fields); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"filled": len(fields)}), nil
			},
			destMeta,
		)
	}

	// --- browser_select ---
	if !has("browser_select") {
		svc.AddToolWithMetadata(
			"browser_select",
			"在 <select> 下拉框中选择某个值。ref 为该 select 的 ref 或 CSS 选择器。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"ref":   map[string]interface{}{"type": "string", "description": "select 元素 ref 或 CSS 选择器"},
					"value": map[string]interface{}{"type": "string", "description": "要选择的值"},
				},
				"required": []string{"ref", "value"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				ref := toolArgString(args, "ref")
				if ref == "" {
					return toolErr("ref required"), nil
				}
				value, _ := args["value"].(string)
				if err := br.Select(ctx, ref, value); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"ref": ref, "value": value}), nil
			},
			destMeta,
		)
	}

	// --- browser_scroll ---
	if !has("browser_scroll") {
		svc.AddToolWithMetadata(
			"browser_scroll",
			"按像素滚动窗口。dx 水平、dy 垂直(正为向下/向右)。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"dx": map[string]interface{}{"type": "integer", "description": "水平滚动像素"},
					"dy": map[string]interface{}{"type": "integer", "description": "垂直滚动像素"},
				},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				if err := br.Scroll(ctx, toolArgInt(args, "dx"), toolArgInt(args, "dy")); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"scrolled": true}), nil
			},
			destMeta,
		)
	}

	// --- browser_wait ---
	if !has("browser_wait") {
		svc.AddToolWithMetadata(
			"browser_wait",
			"等待某条件满足:selector 可见、或页面出现某 text、或 network_idle。可选 timeout_seconds。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"selector":        map[string]interface{}{"type": "string", "description": "等待该 CSS 选择器可见"},
					"text":            map[string]interface{}{"type": "string", "description": "等待页面文本出现该子串"},
					"network_idle":    map[string]interface{}{"type": "boolean", "description": "等待网络空闲"},
					"timeout_seconds": map[string]interface{}{"type": "integer", "description": "超时秒数"},
				},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				cond := browser.WaitCond{
					Selector:    toolArgString(args, "selector"),
					Text:        toolArgString(args, "text"),
					NetworkIdle: toolArgBool(args, "network_idle"),
				}
				if t := toolArgInt(args, "timeout_seconds"); t > 0 {
					cond.Timeout = time.Duration(t) * time.Second
				}
				if err := br.WaitFor(ctx, cond); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"waited": true}), nil
			},
			roMeta,
		)
	}

	// --- browser_screenshot ---
	if !has("browser_screenshot") {
		svc.AddToolWithMetadata(
			"browser_screenshot",
			"截取当前页面为 PNG,返回 base64 编码的图像数据。",
			map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				png, err := br.Screenshot(ctx)
				if err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{
					"image_base64": base64.StdEncoding.EncodeToString(png),
					"mime":         "image/png",
				}), nil
			},
			roMeta,
		)
	}

	// --- browser_evaluate ---
	if !has("browser_evaluate") {
		svc.AddToolWithMetadata(
			"browser_evaluate",
			"在页面中执行任意 JavaScript 并返回结果。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"js": map[string]interface{}{"type": "string", "description": "要执行的 JavaScript"},
				},
				"required": []string{"js"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				js := toolArgString(args, "js")
				if js == "" {
					return toolErr("js required"), nil
				}
				result, err := br.Evaluate(ctx, js)
				if err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{"result": result}), nil
			},
			destMeta,
		)
	}

	// --- browser_console ---
	if !has("browser_console") {
		svc.AddToolWithMetadata(
			"browser_console",
			"返回会话开始以来捕获的浏览器 console 消息 [{level,text}]。",
			map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				msgs, err := br.Console(ctx)
				if err != nil {
					return toolErr(err.Error()), nil
				}
				out := make([]map[string]interface{}, 0, len(msgs))
				for _, m := range msgs {
					out = append(out, map[string]interface{}{"level": m.Level, "text": m.Text})
				}
				return toolOK(map[string]interface{}{"messages": out}), nil
			},
			roMeta,
		)
	}

	// --- browser_download ---
	if !has("browser_download") {
		svc.AddToolWithMetadata(
			"browser_download",
			"下载一个 URL 到沙箱工作区。可选 filename(缺省取 URL 末段)。未配置沙箱时返回错误。",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"url":      map[string]interface{}{"type": "string", "description": "要下载的 URL"},
					"filename": map[string]interface{}{"type": "string", "description": "保存到工作区的文件名"},
				},
				"required": []string{"url"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				u := toolArgString(args, "url")
				if u == "" {
					return toolErr("url required"), nil
				}
				if sb == nil {
					return toolErr("no workspace is configured (browser was registered without a sandbox)"), nil
				}
				filename := toolArgString(args, "filename")
				if filename == "" {
					filename = filepath.Base(strings.SplitN(u, "?", 2)[0])
					if filename == "" || filename == "." || filename == "/" {
						filename = "download"
					}
				}
				// Download to a host temp file, then write the bytes into the
				// sandbox workspace so the jailing rules apply uniformly.
				tmp, err := os.CreateTemp("", "agentgo-dl-*")
				if err != nil {
					return toolErr(err.Error()), nil
				}
				tmpPath := tmp.Name()
				_ = tmp.Close()
				defer os.Remove(tmpPath)

				if err := br.Download(ctx, u, tmpPath); err != nil {
					return toolErr(err.Error()), nil
				}
				data, err := os.ReadFile(tmpPath)
				if err != nil {
					return toolErr(err.Error()), nil
				}
				if err := sb.WriteFile(ctx, filename, data, fs.FileMode(0o644)); err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{
					"path":  filepath.Join(sb.Workspace(), filename),
					"bytes": len(data),
				}), nil
			},
			destMeta,
		)
	}
}
