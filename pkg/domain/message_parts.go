package domain

type MessagePartType string

const (
	MessagePartTypeText  MessagePartType = "text"
	MessagePartTypeImage MessagePartType = "image"
	// MessagePartTypeAudio carries recorded audio to a model that accepts it.
	// The wire shape is OpenAI's input_audio block. Unlike the image path,
	// this one has not been exercised against a live endpoint here — it
	// follows the documented format, and a provider that rejects it will say
	// so rather than silently ignore it.
	MessagePartTypeAudio MessagePartType = "audio"
	// MessagePartTypeFile carries a document (a PDF, most often) as OpenAI's
	// file block. Same caveat as audio.
	MessagePartTypeFile MessagePartType = "file"
)

// MessagePart is an optional structured content block for multimodal providers.
// When Parts is empty, callers can continue using Message.Content as before.
type MessagePart struct {
	Type  MessagePartType `json:"type"`
	Text  string          `json:"text,omitempty"`
	Image *MessageImage   `json:"image,omitempty"`
	Audio *MessageAudio   `json:"audio,omitempty"`
	File  *MessageFile    `json:"file,omitempty"`
}

// MessageAudio describes recorded audio for a model that accepts it.
type MessageAudio struct {
	Base64    string `json:"base64,omitempty"`
	LocalPath string `json:"local_path,omitempty"`
	// Format is the container, e.g. "wav" or "mp3". Required by the wire
	// format; derived from the file extension when a LocalPath is given.
	Format string `json:"format,omitempty"`
}

// MessageFile describes a document for a model that accepts one.
type MessageFile struct {
	Base64    string `json:"base64,omitempty"`
	LocalPath string `json:"local_path,omitempty"`
	MIMEType  string `json:"mime_type,omitempty"`
	// Filename is what the model is told the document is called. Some
	// providers require it.
	Filename string `json:"filename,omitempty"`
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

// AudioBase64Part carries recorded audio inline. format is the container,
// e.g. "wav" or "mp3".
func AudioBase64Part(base64Data, format string) MessagePart {
	return MessagePart{
		Type:  MessagePartTypeAudio,
		Audio: &MessageAudio{Base64: base64Data, Format: format},
	}
}

// AudioLocalPathPart carries audio read from disk at send time.
func AudioLocalPathPart(path string) MessagePart {
	return MessagePart{Type: MessagePartTypeAudio, Audio: &MessageAudio{LocalPath: path}}
}

// FileBase64Part carries a document inline.
func FileBase64Part(base64Data, mimeType, filename string) MessagePart {
	return MessagePart{
		Type: MessagePartTypeFile,
		File: &MessageFile{Base64: base64Data, MIMEType: mimeType, Filename: filename},
	}
}

// FileLocalPathPart carries a document read from disk at send time.
func FileLocalPathPart(path string) MessagePart {
	return MessagePart{Type: MessagePartTypeFile, File: &MessageFile{LocalPath: path}}
}
