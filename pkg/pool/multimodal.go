package pool

import (
	"encoding/base64"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// Pictures in, pictures out.
//
// `domain.MessagePart` and `WithInputImages` have existed for a long time,
// and `pkg/providers/openai.go` serialises them. This package did not: it
// built every outgoing message as `{"role": …, "content": <string>}` and
// dropped `msg.Parts` on the floor.
//
// That is not a small gap, because the pool is what a configured install
// uses. Every consumer built through `WithConfig` — superai included — was
// attaching images to a run and sending a request with no image in it, and
// nothing said so: the model answered the text, and answered it plausibly.
// A feature that silently does nothing is worse than one that is missing.
//
// The other direction had no plumbing at all. A model asked to draw returns
// `message.content: null` with the picture in a sibling field, so the loop
// read an empty answer and the lint layer treated it as a refusal.

// messageContentForWire renders one message's content for the request body.
//
// A message with no parts stays a plain string, which is what almost every
// message is and what every endpoint understands. Only a message that
// actually carries parts becomes an array — a content array where a string
// would do is a difference some servers notice.
func messageContentForWire(msg domain.Message) interface{} {
	if len(msg.Parts) == 0 {
		return msg.Content
	}
	parts := make([]map[string]interface{}, 0, len(msg.Parts)+1)
	if strings.TrimSpace(msg.Content) != "" {
		parts = append(parts, map[string]interface{}{"type": "text", "text": msg.Content})
	}
	for _, part := range msg.Parts {
		block, ok := partForWire(part)
		if !ok {
			continue
		}
		parts = append(parts, block)
	}
	if len(parts) == 0 {
		return msg.Content
	}
	return parts
}

// partForWire renders one part, or reports that it could not.
//
// A part it cannot render is skipped rather than sent as something else: an
// unreadable file is a missing attachment, and a missing attachment the
// model is told about in the wrong format is a confused turn.
func partForWire(part domain.MessagePart) (map[string]interface{}, bool) {
	switch part.Type {
	case domain.MessagePartTypeText:
		if strings.TrimSpace(part.Text) == "" {
			return nil, false
		}
		return map[string]interface{}{"type": "text", "text": part.Text}, true

	case domain.MessagePartTypeImage:
		url, ok := imageURLForWire(part.Image)
		if !ok {
			return nil, false
		}
		image := map[string]interface{}{"url": url}
		if part.Image != nil && strings.TrimSpace(part.Image.Detail) != "" {
			image["detail"] = part.Image.Detail
		}
		return map[string]interface{}{"type": "image_url", "image_url": image}, true

	case domain.MessagePartTypeAudio:
		if part.Audio == nil {
			return nil, false
		}
		data, format := part.Audio.Base64, strings.TrimSpace(part.Audio.Format)
		if data == "" && part.Audio.LocalPath != "" {
			raw, err := os.ReadFile(part.Audio.LocalPath)
			if err != nil {
				return nil, false
			}
			data = base64.StdEncoding.EncodeToString(raw)
			if format == "" {
				format = strings.TrimPrefix(strings.ToLower(filepath.Ext(part.Audio.LocalPath)), ".")
			}
		}
		if data == "" {
			return nil, false
		}
		if format == "" {
			format = "wav"
		}
		return map[string]interface{}{
			"type":        "input_audio",
			"input_audio": map[string]interface{}{"data": data, "format": format},
		}, true

	case domain.MessagePartTypeFile:
		if part.File == nil {
			return nil, false
		}
		data, mimeType, name := part.File.Base64, part.File.MIMEType, part.File.Filename
		if data == "" && part.File.LocalPath != "" {
			raw, err := os.ReadFile(part.File.LocalPath)
			if err != nil {
				return nil, false
			}
			data = base64.StdEncoding.EncodeToString(raw)
			if mimeType == "" {
				mimeType = mime.TypeByExtension(filepath.Ext(part.File.LocalPath))
			}
			if name == "" {
				name = filepath.Base(part.File.LocalPath)
			}
		}
		if data == "" {
			return nil, false
		}
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		file := map[string]interface{}{"file_data": "data:" + mimeType + ";base64," + data}
		if name != "" {
			file["filename"] = name
		}
		return map[string]interface{}{"type": "file", "file": file}, true
	}
	return nil, false
}

// imageURLForWire turns an image part into the data URI or URL to send.
func imageURLForWire(img *domain.MessageImage) (string, bool) {
	if img == nil {
		return "", false
	}
	if data := strings.TrimSpace(img.Base64); data != "" {
		// Already a data URI or an http(s) URL: pass it through. A caller
		// that built one itself should not have it wrapped in another.
		if strings.HasPrefix(data, "data:") || strings.HasPrefix(data, "http://") || strings.HasPrefix(data, "https://") {
			return data, true
		}
		mimeType := strings.TrimSpace(img.MIMEType)
		if mimeType == "" {
			mimeType = "image/png"
		}
		return "data:" + mimeType + ";base64," + data, true
	}
	path := strings.TrimSpace(img.LocalPath)
	if path == "" {
		return "", false
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path, true
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	mimeType := strings.TrimSpace(img.MIMEType)
	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(path))
	}
	if mimeType == "" {
		mimeType = "image/png"
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(raw), true
}

// outputPartsFromMessage reads non-text output off a chat completion's
// message.
//
// The shape is the one OpenAI-compatible gateways actually return, confirmed
// against a live image model: message.images is an array of
// {"type":"image_url","image_url":{"url":"data:image/…;base64,…"}}. Anything
// else is ignored rather than guessed at.
func outputPartsFromMessage(images []wireOutputImage) []domain.MessagePart {
	if len(images) == 0 {
		return nil
	}
	out := make([]domain.MessagePart, 0, len(images))
	for _, img := range images {
		url := strings.TrimSpace(img.ImageURL.URL)
		if url == "" {
			continue
		}
		data, mimeType := splitDataURI(url)
		out = append(out, domain.MessagePart{
			Type:  domain.MessagePartTypeImage,
			Image: &domain.MessageImage{Base64: data, MIMEType: mimeType},
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// wireOutputImage is one entry of a response's message.images.
type wireOutputImage struct {
	Type     string `json:"type"`
	Index    int    `json:"index"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

// splitDataURI separates a data URI into its payload and MIME type. A URL
// that is not a data URI is returned whole, with no type — a caller storing
// it keeps something it can still fetch.
func splitDataURI(url string) (data, mimeType string) {
	if !strings.HasPrefix(url, "data:") {
		return url, ""
	}
	rest := url[len("data:"):]
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return url, ""
	}
	meta := rest[:comma]
	data = rest[comma+1:]
	mimeType = strings.TrimSuffix(meta, ";base64")
	return data, mimeType
}

// describeOutputParts renders a one-line summary of non-text output, for a
// log or an error message.
func describeOutputParts(parts []domain.MessagePart) string {
	if len(parts) == 0 {
		return ""
	}
	counts := map[domain.MessagePartType]int{}
	for _, p := range parts {
		counts[p.Type]++
	}
	pieces := make([]string, 0, len(counts))
	for kind, n := range counts {
		pieces = append(pieces, fmt.Sprintf("%d %s", n, kind))
	}
	return strings.Join(pieces, ", ")
}
