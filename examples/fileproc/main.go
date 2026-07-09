// Command fileproc demonstrates the document file-processing capability:
// pkg/fileproc extracts plain text + metadata from Word/Excel/PowerPoint/PDF/
// image/text files (CGO-free, no OCR), and pkg/agent's read_document tool
// surfaces it to an agent.
//
// It runs fully offline: it generates sample files in a temp dir, extracts each
// one, and finally invokes the read_document tool handler directly on one file.
//
//	go run ./examples/fileproc
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/fileproc"
	"github.com/xuri/excelize/v2"
)

func main() {
	dir, err := os.MkdirTemp("", "fileproc-demo-*")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(dir)

	ctx := context.Background()

	// --- generate sample files -------------------------------------------
	docx := filepath.Join(dir, "notes.docx")
	if err := writeDocx(docx, "AgentGo file processing", "Reads docx, xlsx, pptx, pdf and images.", "All in pure Go, no CGO."); err != nil {
		fatal(err)
	}
	xlsx := filepath.Join(dir, "data.xlsx")
	if err := writeXlsx(xlsx); err != nil {
		fatal(err)
	}
	pngPath := filepath.Join(dir, "logo.png")
	if err := writePNG(pngPath, 64, 32); err != nil {
		fatal(err)
	}
	pdf := filepath.Join(dir, "hello.pdf")
	if err := os.WriteFile(pdf, buildHelloPDF(), 0o644); err != nil {
		fatal(err)
	}

	// --- extract each via the library ------------------------------------
	fmt.Println("=== fileproc.Extract on generated samples ===")
	for _, path := range []string{docx, xlsx, pngPath, pdf} {
		doc, err := fileproc.Extract(ctx, path)
		if err != nil {
			fmt.Printf("- %s: ERROR %v\n", filepath.Base(path), err)
			continue
		}
		fmt.Printf("\n- %s  [format=%s]\n", filepath.Base(path), doc.Format)
		fmt.Printf("  metadata: %v\n", doc.Metadata)
		if doc.Text != "" {
			fmt.Printf("  text (first lines):\n%s\n", indent(firstLines(doc.Text, 3)))
		} else {
			fmt.Printf("  text: <empty — images carry no text, no OCR>\n")
		}
	}

	// --- exercise the read_document agent tool ---------------------------
	// WithFileTools() registers the tool on the service; agent.ReadDocument is
	// the same logic the tool handler runs, invoked here directly (no LLM).
	fmt.Println("\n=== read_document tool (offline, handler logic invoked directly) ===")
	svc, err := agent.New("demo").WithFileTools().Build()
	if err != nil {
		fatal(err)
	}
	res := agent.ReadDocument(ctx, svc, docx, 0)
	fmt.Printf("read_document(%s) ->\n", filepath.Base(docx))
	fmt.Printf("  ok=%v format=%v chars=%v truncated=%v\n", res["ok"], res["format"], res["chars"], res["truncated"])
	if txt, _ := res["text"].(string); txt != "" {
		fmt.Printf("  text (first line): %s\n", firstLines(txt, 1))
	}

	fmt.Println("\nOK")
}

// --- sample generators ------------------------------------------------------

func writeDocx(path string, paragraphs ...string) error {
	var body strings.Builder
	for _, p := range paragraphs {
		body.WriteString(`<w:p><w:r><w:t xml:space="preserve">`)
		body.WriteString(p)
		body.WriteString(`</w:t></w:r></w:p>`)
	}
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:body>` + body.String() + `</w:body></w:document>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(doc)); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func writeXlsx(path string) error {
	f := excelize.NewFile()
	defer f.Close()
	f.SetCellValue("Sheet1", "A1", "Product")
	f.SetCellValue("Sheet1", "B1", "Units")
	f.SetCellValue("Sheet1", "A2", "Widget")
	f.SetCellValue("Sheet1", "B2", 128)
	f.NewSheet("Summary")
	f.SetCellValue("Summary", "A1", "Total")
	f.SetCellValue("Summary", "B1", 128)
	return f.SaveAs(path)
}

func writePNG(path string, w, h int) error {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 8), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// buildHelloPDF builds a minimal valid single-page PDF that reads "Hello PDF",
// computing xref offsets so github.com/dslipak/pdf parses it cleanly.
func buildHelloPDF() []byte {
	content := "BT\n/F1 24 Tf\n72 720 Td\n(Hello PDF from AgentGo) Tj\nET\n"
	objs := []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Resources<</Font<</F1 4 0 R>>>>/Contents 5 0 R>>",
		"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
		fmt.Sprintf("<</Length %d>>\nstream\n%sendstream", len(content), content),
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, b := range objs {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, b)
	}
	xrefStart := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xrefStart)
	return buf.Bytes()
}

// --- small helpers ----------------------------------------------------------

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
