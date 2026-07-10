package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Local-OCR tool. It sends an image to a local ollama-compatible endpoint (the
// glm-ocr model by default) and returns the extracted text. Unlike the
// fileproc-backed read_document tool — which never OCRs — this one requires a
// running vision/OCR model and is therefore opt-in (RegisterOCRTool / WithOCR).
// The rest of AgentGo works without it.

const defaultOCREndpoint = "http://localhost:11434"
const defaultOCRModel = "glm-ocr"
const defaultOCRPrompt = "Extract all text from this image. Output the text exactly as it appears."

// OCROption configures the ocr_image tool.
type OCROption func(*ocrConfig)

type ocrConfig struct {
	endpoint string
	model    string
	prompt   string
}

// WithOCREndpoint sets the base URL of the ollama-compatible server
// (default "http://localhost:11434"). The tool POSTs to {endpoint}/api/generate.
func WithOCREndpoint(url string) OCROption {
	return func(c *ocrConfig) {
		if url != "" {
			c.endpoint = url
		}
	}
}

// WithOCRModel sets the OCR model name (default "glm-ocr").
func WithOCRModel(model string) OCROption {
	return func(c *ocrConfig) {
		if model != "" {
			c.model = model
		}
	}
}

// WithOCRPrompt sets the default extraction prompt.
func WithOCRPrompt(p string) OCROption {
	return func(c *ocrConfig) {
		if p != "" {
			c.prompt = p
		}
	}
}

func newOCRConfig(opts []OCROption) *ocrConfig {
	c := &ocrConfig{endpoint: defaultOCREndpoint, model: defaultOCRModel, prompt: defaultOCRPrompt}
	for _, o := range opts {
		if o != nil {
			o(c)
		}
	}
	return c
}

const ocrImageToolDescription = "对本地图片做 OCR，提取图片中的文字并返回。底层调用本地 ollama 的 glm-ocr 模型（需要本地已运行）。" +
	"当用户要\"识别/读取图片里的文字\"、扫描件、截图内文本时调用。path 为图片路径（挂载沙箱时为工作区相对路径）。"

func ocrImageToolSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "要 OCR 的图片路径（挂载沙箱时为工作区相对路径，否则为主机路径）",
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "可选：覆盖默认提取提示词",
			},
		},
		"required": []string{"path"},
	}
}

// RegisterOCRTool registers the built-in `ocr_image` tool on a service. It is
// read-only and concurrency-safe but network-dependent (a local OCR model must
// be reachable). Opt in per agent that needs image OCR:
//
//	svc, _ := agent.New("assistant").Build()
//	agent.RegisterOCRTool(svc, agent.WithOCRModel("glm-ocr"))
func RegisterOCRTool(svc *Service, opts ...OCROption) {
	if svc == nil {
		return
	}
	if svc.toolRegistry != nil && svc.toolRegistry.Has("ocr_image") {
		return
	}
	cfg := newOCRConfig(opts)
	svc.AddToolWithMetadata(
		"ocr_image",
		ocrImageToolDescription,
		ocrImageToolSchema(),
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			return ocrImage(ctx, svc, cfg, toolArgString(args, "path"), toolArgString(args, "prompt")), nil
		},
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel},
	)
}

// OCRImage is the logic behind the ocr_image tool, exposed so callers can run it
// directly (e.g. offline demos). Configure the endpoint/model/prompt via
// OCROptions; pass a non-empty prompt to override the configured default for
// this call. It never returns an error — result: {ok, text, model, [error]}.
func OCRImage(ctx context.Context, svc *Service, path, prompt string, opts ...OCROption) map[string]interface{} {
	return ocrImage(ctx, svc, newOCRConfig(opts), path, prompt)
}

// ocrImage reads the image bytes (sandbox-aware, same resolution as
// read_document), base64-encodes them, and POSTs to {endpoint}/api/generate.
func ocrImage(ctx context.Context, svc *Service, cfg *ocrConfig, path, prompt string) map[string]interface{} {
	if path == "" {
		return map[string]interface{}{"ok": false, "error": "path is required", "model": cfg.model}
	}
	data, err := readImageBytes(ctx, svc, path)
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error(), "model": cfg.model}
	}
	if prompt == "" {
		prompt = cfg.prompt
	}
	text, err := ocrGenerate(ctx, cfg, prompt, data)
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error(), "model": cfg.model}
	}
	return map[string]interface{}{"ok": true, "text": text, "model": cfg.model}
}

// readImageBytes reads raw file bytes, sandbox-aware: through the sandbox when
// attached, otherwise from the host path.
func readImageBytes(ctx context.Context, svc *Service, path string) ([]byte, error) {
	if svc != nil && svc.Sandbox() != nil {
		return svc.Sandbox().ReadFile(ctx, path)
	}
	return os.ReadFile(path)
}

type ollamaGenerateRequest struct {
	Model  string   `json:"model"`
	Prompt string   `json:"prompt"`
	Images []string `json:"images"`
	Stream bool     `json:"stream"`
}

type ollamaGenerateResponse struct {
	Response string `json:"response"`
	Error    string `json:"error"`
}

func ocrGenerate(ctx context.Context, cfg *ocrConfig, prompt string, image []byte) (string, error) {
	reqBody := ollamaGenerateRequest{
		Model:  cfg.model,
		Prompt: prompt,
		Images: []string{base64.StdEncoding.EncodeToString(image)},
		Stream: false,
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal ocr request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	url := cfg.endpoint + "/api/generate"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return "", fmt.Errorf("build ocr request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ocr request to %s failed: %w", url, err)
	}
	defer resp.Body.Close()

	var out ollamaGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode ocr response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return "", fmt.Errorf("ocr server %s: %s", resp.Status, out.Error)
		}
		return "", fmt.Errorf("ocr server returned %s", resp.Status)
	}
	if out.Error != "" {
		return "", fmt.Errorf("ocr server error: %s", out.Error)
	}
	return out.Response, nil
}
