// Pictures in, pictures out.
//
//	go run ./examples/multimodal path/to/photo.png
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func main() {
	svc, err := agent.New("looker").WithSystemPrompt("You answer in one short sentence.").Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()
	ctx := context.Background()

	// --- In -------------------------------------------------------------
	//
	// The file is read and encoded at send time. Audio and documents attach
	// the same way, with WithInputAudio and WithInputFiles.
	if len(os.Args) > 1 {
		res, err := svc.Run(ctx, "What is in this image? Answer in a few words.",
			agent.WithInputImages(os.Args[1]))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("about your image:", res.Text())
	}

	// --- Out ------------------------------------------------------------
	//
	// A model asked to draw returns no text at all: the picture arrives in
	// its own field, and the run carries it back on OutputParts. Reading the
	// text alone would find an empty answer.
	res, err := svc.Run(ctx, "Draw a simple green triangle on a white background.")
	if err != nil {
		log.Fatal(err)
	}
	if len(res.OutputParts) == 0 {
		fmt.Println("this model answered in words only:", res.Text())
		return
	}
	for i, part := range res.OutputParts {
		if part.Type != domain.MessagePartTypeImage || part.Image == nil {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(part.Image.Base64)
		if err != nil {
			continue
		}
		name := fmt.Sprintf("drawn-%d.jpg", i)
		if err := os.WriteFile(name, raw, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("wrote %s (%s, %d bytes)\n", name, part.Image.MIMEType, len(raw))
	}
}
