// Command docpipeline demonstrates the extended document tooling in pkg/fileproc
// and pkg/agent:
//
//   - fileproc.Extract with WithPages (page/slide selection) and per-page text
//     (Document.PageTexts)
//   - the export_document agent tool: extract selected pages and write them to a
//     Markdown file in one call
//   - the ocr_image agent tool against a local ollama glm-ocr endpoint — probed
//     first and cleanly SKIPPED (with a note) when ollama isn't reachable
//
// It runs fully offline for the fileproc + export parts; OCR is the only
// network-dependent step and never fails the program.
//
//	go run ./examples/docpipeline
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/fileproc"
)

const ocrEndpoint = "http://localhost:11434"

func main() {
	dir, err := os.MkdirTemp("", "docpipeline-*")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(dir)

	ctx := context.Background()

	// --- generate sample files -------------------------------------------
	pdfPath := filepath.Join(dir, "course.pdf")
	if err := os.WriteFile(pdfPath, buildMultiPagePDF([]string{
		"Chapter 1 Introduction",
		"Chapter 2 Fundamentals",
		"Chapter 3 Advanced Topics",
		"Chapter 4 Conclusion",
	}), 0o644); err != nil {
		fatal(err)
	}
	docxPath := filepath.Join(dir, "notes.docx")
	if err := writeDocx(docxPath, "Meeting notes", "Ship the document pipeline.", "Pure Go, no CGO."); err != nil {
		fatal(err)
	}
	pngPath := filepath.Join(dir, "scan.png")
	if err := writePNG(pngPath, 96, 48); err != nil {
		fatal(err)
	}

	// --- 1. fileproc.Extract with WithPages + per-page text --------------
	fmt.Println("=== 1. fileproc.Extract with WithPages(\"2-3\") ===")
	doc, err := fileproc.Extract(ctx, pdfPath, fileproc.WithPages("2-3"))
	if err != nil {
		fatal(err)
	}
	fmt.Printf("format=%s pages(total)=%v pages_extracted=%v\n",
		doc.Format, doc.Metadata["pages"], doc.Metadata["pages_extracted"])
	for i, pt := range doc.PageTexts {
		fmt.Printf("  page[%d]: %s\n", i, firstLines(pt, 1))
	}

	// Full extract to show PageTexts covers every page.
	full, err := fileproc.Extract(ctx, pdfPath)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("full extract: %d page texts\n", len(full.PageTexts))

	// --- 2. export_document tool: PDF pages 1-3 -> Markdown --------------
	fmt.Println("\n=== 2. export_document tool (PDF pages 1-3 -> out.md) ===")
	svc, err := agent.New("docpipeline").WithFileTools().Build()
	if err != nil {
		fatal(err)
	}
	outPath := filepath.Join(dir, "out.md")
	res := agent.ExportDocument(ctx, svc, agent.ExportDocumentOpts{
		Path:    pdfPath,
		OutPath: outPath,
		Pages:   "1-3",
		Format:  "md",
	})
	fmt.Printf("export -> ok=%v pages_written=%v chars=%v bytes=%v\n",
		res["ok"], res["pages_written"], res["chars"], res["bytes"])
	if md, err := os.ReadFile(outPath); err == nil {
		fmt.Printf("out.md (head):\n%s\n", indent(firstLines(string(md), 4)))
	}

	// read_document with per_page on the docx (no pages -> single logical page)
	rd := agent.ReadDocumentWith(ctx, svc, agent.ReadDocumentOpts{Path: docxPath, PerPage: true})
	fmt.Printf("read_document(notes.docx) ok=%v format=%v chars=%v\n", rd["ok"], rd["format"], rd["chars"])

	// --- 3. ocr_image tool (network-dependent, probed then skipped) ------
	fmt.Println("\n=== 3. ocr_image tool (local ollama glm-ocr) ===")
	if !endpointReachable(ocrEndpoint) {
		fmt.Printf("SKIPPED: %s not reachable — start ollama with glm-ocr to run OCR.\n", ocrEndpoint)
	} else {
		ocrSvc, err := agent.New("ocr").WithOCR(agent.WithOCREndpoint(ocrEndpoint)).Build()
		if err != nil {
			fatal(err)
		}
		out := agent.OCRImage(ctx, ocrSvc, pngPath, "", agent.WithOCREndpoint(ocrEndpoint))
		if ok, _ := out["ok"].(bool); ok {
			fmt.Printf("ocr_image(scan.png) -> %s\n", firstLines(fmt.Sprint(out["text"]), 3))
		} else {
			fmt.Printf("ocr_image reachable but failed (model glm-ocr present?): %v\n", out["error"])
		}
	}

	fmt.Println("\nOK")
}

// endpointReachable does a fast TCP dial to host:port parsed from a URL prefix.
func endpointReachable(url string) bool {
	host := strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://")
	host = strings.TrimSuffix(host, "/")
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := net.DialTimeout("tcp", host, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
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

func writePNG(path string, w, h int) error {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 2), G: uint8(y * 4), B: 180, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// buildMultiPagePDF builds a valid multi-page PDF where page i shows texts[i],
// computing xref offsets so github.com/dslipak/pdf parses it cleanly.
func buildMultiPagePDF(texts []string) []byte {
	n := len(texts)
	kids := make([]string, n)
	for i := 0; i < n; i++ {
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	objs := []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		fmt.Sprintf("<</Type/Pages/Kids[%s]/Count %d>>", strings.Join(kids, " "), n),
		"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
	}
	for i := 0; i < n; i++ {
		objs = append(objs, fmt.Sprintf(
			"<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Resources<</Font<</F1 3 0 R>>>>/Contents %d 0 R>>",
			5+2*i))
		content := fmt.Sprintf("BT\n/F1 24 Tf\n72 720 Td\n(%s) Tj\nET\n", texts[i])
		objs = append(objs, fmt.Sprintf("<</Length %d>>\nstream\n%sendstream", len(content), content))
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
