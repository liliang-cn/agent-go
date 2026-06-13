package agent

import "github.com/liliang-cn/agent-go/v2/pkg/domain"

// extractToolImageParts inspects a structured tool result for embedded image
// data (the {ok, data:{image_base64, mime}} shape produced by browser_screenshot
// and image fs_read) and, when found, returns the image as multimodal message
// parts plus a copy of the result with the base64 payload replaced by a short
// placeholder. Returning a cleaned result keeps the huge base64 string out of
// the text "tool" message (providers also reject images in tool-role messages),
// while the parts are attached to a follow-up user message the vision model can
// actually see.
//
// When no image is present the original result is returned unchanged and parts
// is nil, so the text-only path is completely unaffected.
func extractToolImageParts(result interface{}) ([]domain.MessagePart, interface{}) {
	top, ok := result.(map[string]interface{})
	if !ok {
		return nil, result
	}
	data, ok := top["data"].(map[string]interface{})
	if !ok {
		return nil, result
	}
	b64, ok := data["image_base64"].(string)
	if !ok || b64 == "" {
		return nil, result
	}
	mime, _ := data["mime"].(string)
	if mime == "" {
		mime, _ = data["mime_type"].(string)
	}
	if mime == "" {
		mime = "image/png"
	}

	// Build a shallow copy with the base64 blob elided so it doesn't bloat the
	// text tool message / history.
	cleanedData := make(map[string]interface{}, len(data))
	for k, v := range data {
		cleanedData[k] = v
	}
	cleanedData["image_base64"] = "<image attached to following message>"
	cleanedTop := make(map[string]interface{}, len(top))
	for k, v := range top {
		cleanedTop[k] = v
	}
	cleanedTop["data"] = cleanedData

	parts := []domain.MessagePart{domain.ImageBase64Part(b64, mime)}
	return parts, cleanedTop
}
