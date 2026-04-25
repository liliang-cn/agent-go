package domain

type MessagePartType string

const (
	MessagePartTypeText  MessagePartType = "text"
	MessagePartTypeImage MessagePartType = "image"
)

// MessagePart is an optional structured content block for multimodal providers.
// When Parts is empty, callers can continue using Message.Content as before.
type MessagePart struct {
	Type  MessagePartType `json:"type"`
	Text  string          `json:"text,omitempty"`
	Image *MessageImage   `json:"image,omitempty"`
}

// MessageImage describes an image input for multimodal models.
// Exactly one of Base64 or LocalPath should usually be provided.
type MessageImage struct {
	Base64    string `json:"base64,omitempty"`
	LocalPath string `json:"local_path,omitempty"`
	MIMEType  string `json:"mime_type,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

func TextPart(text string) MessagePart {
	return MessagePart{
		Type: MessagePartTypeText,
		Text: text,
	}
}

func ImageBase64Part(base64Data, mimeType string) MessagePart {
	return MessagePart{
		Type: MessagePartTypeImage,
		Image: &MessageImage{
			Base64:   base64Data,
			MIMEType: mimeType,
		},
	}
}

func ImageLocalPathPart(path string) MessagePart {
	return MessagePart{
		Type: MessagePartTypeImage,
		Image: &MessageImage{
			LocalPath: path,
		},
	}
}
