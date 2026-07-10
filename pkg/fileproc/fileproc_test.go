package fileproc

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// --- sample generators ------------------------------------------------------

func writeDocx(t *testing.T, dir string, paragraphs ...string) string {
	t.Helper()
	var body strings.Builder
	for _, p := range paragraphs {
		body.WriteString(`<w:p><w:r><w:t xml:space="preserve">`)
		body.WriteString(p)
		body.WriteString(`</w:t></w:r></w:p>`)
	}
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:body>` + body.String() + `</w:body></w:document>`
	return writeZip(t, dir, "sample.docx", map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		"word/document.xml":   doc,
	})
}

func writePptx(t *testing.T, dir string, slides ...string) string {
	t.Helper()
	files := map[string]string{
		"[Content_Types].xml":  `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		"ppt/presentation.xml": `<?xml version="1.0"?><p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`,
	}
	for i, s := range slides {
		xml := `<?xml version="1.0"?>` +
			`<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` +
			`xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">` +
			`<p:cSld><p:spTree><a:p><a:r><a:t>` + s + `</a:t></a:r></a:p></p:spTree></p:cSld></p:sld>`
		files["ppt/slides/slide"+itoa(i+1)+".xml"] = xml
	}
	return writeZip(t, dir, "sample.pptx", files)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func writeZip(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, c := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatalf("zip create %s: %v", n, err)
		}
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatalf("zip write %s: %v", n, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func writeXlsx(t *testing.T, dir string) string {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	// Sheet1
	f.SetCellValue("Sheet1", "A1", "Name")
	f.SetCellValue("Sheet1", "B1", "Score")
	f.SetCellValue("Sheet1", "A2", "Alice")
	f.SetCellValue("Sheet1", "B2", 97)
	// A second sheet
	f.NewSheet("Metrics")
	f.SetCellValue("Metrics", "A1", "Revenue")
	f.SetCellValue("Metrics", "B1", 12345)

	path := filepath.Join(dir, "sample.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatalf("save xlsx: %v", err)
	}
	return path
}

func writePNG(t *testing.T, dir string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, 0, color.RGBA{R: 255, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	path := filepath.Join(dir, "sample.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return path
}

// buildHelloPDF assembles a minimal, valid single-page PDF containing the text
// "Hello PDF", computing the cross-reference table offsets at runtime so the
// file parses cleanly under github.com/dslipak/pdf.
func buildHelloPDF() []byte {
	content := "BT\n/F1 24 Tf\n72 720 Td\n(Hello PDF) Tj\nET\n"
	objs := []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Resources<</Font<</F1 4 0 R>>>>/Contents 5 0 R>>",
		"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
		"<</Length " + itoa(len(content)) + ">>\nstream\n" + content + "endstream",
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, body := range objs {
		offsets[i] = buf.Len()
		buf.WriteString(itoa(i+1) + " 0 obj\n")
		buf.WriteString(body)
		buf.WriteString("\nendobj\n")
	}
	xrefStart := buf.Len()
	buf.WriteString("xref\n")
	buf.WriteString("0 " + itoa(len(objs)+1) + "\n")
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		buf.WriteString(pad10(off) + " 00000 n \n")
	}
	buf.WriteString("trailer\n<</Size " + itoa(len(objs)+1) + "/Root 1 0 R>>\n")
	buf.WriteString("startxref\n" + itoa(xrefStart) + "\n%%EOF\n")
	return buf.Bytes()
}

func pad10(n int) string {
	s := itoa(n)
	for len(s) < 10 {
		s = "0" + s
	}
	return s
}

func writePDF(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "sample.pdf")
	if err := os.WriteFile(path, buildHelloPDF(), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	return path
}

// buildMultiPagePDF assembles a valid multi-page PDF where page i shows texts[i].
func buildMultiPagePDF(texts []string) []byte {
	n := len(texts)
	kids := make([]string, n)
	for i := 0; i < n; i++ {
		kids[i] = itoa(4+2*i) + " 0 R"
	}
	objs := []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[" + strings.Join(kids, " ") + "]/Count " + itoa(n) + ">>",
		"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
	}
	for i := 0; i < n; i++ {
		contentObj := 5 + 2*i
		objs = append(objs, "<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]"+
			"/Resources<</Font<</F1 3 0 R>>>>/Contents "+itoa(contentObj)+" 0 R>>")
		content := "BT\n/F1 24 Tf\n72 720 Td\n(" + texts[i] + ") Tj\nET\n"
		objs = append(objs, "<</Length "+itoa(len(content))+">>\nstream\n"+content+"endstream")
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, body := range objs {
		offsets[i] = buf.Len()
		buf.WriteString(itoa(i+1) + " 0 obj\n")
		buf.WriteString(body)
		buf.WriteString("\nendobj\n")
	}
	xrefStart := buf.Len()
	buf.WriteString("xref\n")
	buf.WriteString("0 " + itoa(len(objs)+1) + "\n")
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		buf.WriteString(pad10(off) + " 00000 n \n")
	}
	buf.WriteString("trailer\n<</Size " + itoa(len(objs)+1) + "/Root 1 0 R>>\n")
	buf.WriteString("startxref\n" + itoa(xrefStart) + "\n%%EOF\n")
	return buf.Bytes()
}

func writeMultiPagePDF(t *testing.T, dir string, texts []string) string {
	t.Helper()
	path := filepath.Join(dir, "multi.pdf")
	if err := os.WriteFile(path, buildMultiPagePDF(texts), 0o644); err != nil {
		t.Fatalf("write multi pdf: %v", err)
	}
	return path
}

// --- table test -------------------------------------------------------------

func TestExtract(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	tests := []struct {
		name        string
		path        string
		wantFormat  string
		wantSubstrs []string
		wantMetaKey []string
	}{
		{
			name:        "docx",
			path:        writeDocx(t, dir, "Hello world", "Second paragraph"),
			wantFormat:  FormatDocx,
			wantSubstrs: []string{"Hello world", "Second paragraph"},
			wantMetaKey: []string{"paragraphs"},
		},
		{
			name:        "pptx",
			path:        writePptx(t, dir, "Slide One Title", "Slide Two Body"),
			wantFormat:  FormatPptx,
			wantSubstrs: []string{"Slide One Title", "Slide Two Body"},
			wantMetaKey: []string{"slides"},
		},
		{
			name:        "xlsx",
			path:        writeXlsx(t, dir),
			wantFormat:  FormatXlsx,
			wantSubstrs: []string{"Alice", "97", "Revenue", "Metrics"},
			wantMetaKey: []string{"sheets", "sheet_names"},
		},
		{
			name:        "pdf",
			path:        writePDF(t, dir),
			wantFormat:  FormatPDF,
			wantSubstrs: []string{"Hello PDF"},
			wantMetaKey: []string{"pages"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Extract(ctx, tc.path)
			if err != nil {
				t.Fatalf("Extract(%s): %v", tc.name, err)
			}
			if doc.Format != tc.wantFormat {
				t.Errorf("format = %q, want %q", doc.Format, tc.wantFormat)
			}
			for _, sub := range tc.wantSubstrs {
				if !strings.Contains(doc.Text, sub) {
					t.Errorf("text missing %q; got:\n%s", sub, doc.Text)
				}
			}
			for _, k := range tc.wantMetaKey {
				if _, ok := doc.Metadata[k]; !ok {
					t.Errorf("metadata missing key %q; got %v", k, doc.Metadata)
				}
			}
		})
	}
}

func TestExtractImage(t *testing.T) {
	dir := t.TempDir()
	path := writePNG(t, dir, 40, 20)
	doc, err := Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("Extract image: %v", err)
	}
	if doc.Format != FormatImage {
		t.Fatalf("format = %q, want image", doc.Format)
	}
	if doc.Text != "" {
		t.Errorf("image text should be empty (no OCR), got %q", doc.Text)
	}
	if doc.Metadata["width"] != 40 || doc.Metadata["height"] != 20 {
		t.Errorf("wrong dimensions: %v", doc.Metadata)
	}
	if doc.Metadata["format"] != "png" {
		t.Errorf("format meta = %v, want png", doc.Metadata["format"])
	}
	// Small image should be inlined as a data URI.
	if uri, ok := doc.Metadata["data_uri"].(string); !ok || !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Errorf("expected png data_uri, got %v", doc.Metadata["data_uri"])
	}
}

func TestExtractText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("plain text content"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("Extract text: %v", err)
	}
	if doc.Format != FormatText || doc.Text != "plain text content" {
		t.Errorf("unexpected text doc: %+v", doc)
	}
}

func TestTruncation(t *testing.T) {
	dir := t.TempDir()
	path := writeDocx(t, dir, strings.Repeat("A", 100), strings.Repeat("B", 100))
	doc, err := Extract(context.Background(), path, WithMaxChars(50))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !doc.Truncated {
		t.Errorf("expected Truncated=true")
	}
	if got := len([]rune(doc.Text)); got != 50 {
		t.Errorf("text length = %d, want 50", got)
	}
}

func TestUnsupportedFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blob.bin")
	// Invalid UTF-8, no recognized magic bytes.
	if err := os.WriteFile(path, []byte{0x00, 0xFF, 0xFE, 0x01, 0x80}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(context.Background(), path); err == nil {
		t.Fatalf("expected error for unsupported format")
	}
}

func TestMaxBytesCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(context.Background(), path, WithMaxBytes(100)); err == nil {
		t.Fatalf("expected error when file exceeds max bytes")
	}
}

func TestParsePageSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		total   int
		want    []int
		wantErr bool
	}{
		{name: "empty=all", spec: "", total: 3, want: []int{1, 2, 3}},
		{name: "single", spec: "2", total: 5, want: []int{2}},
		{name: "range", spec: "2-4", total: 5, want: []int{2, 3, 4}},
		{name: "mixed", spec: "1-2,4", total: 5, want: []int{1, 2, 4}},
		{name: "spaces tolerated", spec: " 1 - 2 , 4 ", total: 5, want: []int{1, 2, 4}},
		{name: "dedupe+sort", spec: "3,1,2-3,1", total: 5, want: []int{1, 2, 3}},
		{name: "clamp out of range", spec: "3-10", total: 5, want: []int{3, 4, 5}},
		{name: "reversed range", spec: "4-2", total: 5, want: []int{2, 3, 4}},
		{name: "all out of range", spec: "8,9", total: 5, want: nil},
		{name: "bad token", spec: "1,abc", total: 5, wantErr: true},
		{name: "bad range", spec: "1-x", total: 5, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePageSpec(tc.spec, tc.total)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.spec)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePageSpec(%q): %v", tc.spec, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestExtractPDFPages(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	path := writeMultiPagePDF(t, dir, []string{"PageOneAlpha", "PageTwoBeta", "PageThreeGamma"})

	// Full extract: PageTexts length == total, metadata pages/pages_extracted.
	doc, err := Extract(ctx, path)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if doc.Format != FormatPDF {
		t.Fatalf("format = %q", doc.Format)
	}
	if len(doc.PageTexts) != 3 {
		t.Fatalf("PageTexts len = %d, want 3", len(doc.PageTexts))
	}
	if doc.Metadata["pages"] != 3 {
		t.Errorf("pages meta = %v, want 3", doc.Metadata["pages"])
	}
	if doc.Metadata["pages_extracted"] != 3 {
		t.Errorf("pages_extracted meta = %v, want 3", doc.Metadata["pages_extracted"])
	}
	if !strings.Contains(doc.PageTexts[1], "PageTwoBeta") {
		t.Errorf("page 2 text = %q, want it to contain PageTwoBeta", doc.PageTexts[1])
	}

	// WithPages("2") returns only page 2's text.
	doc2, err := Extract(ctx, path, WithPages("2"))
	if err != nil {
		t.Fatalf("Extract WithPages: %v", err)
	}
	if len(doc2.PageTexts) != 1 {
		t.Fatalf("selected PageTexts len = %d, want 1", len(doc2.PageTexts))
	}
	if doc2.Metadata["pages"] != 3 {
		t.Errorf("pages meta = %v, want total 3 even when selecting", doc2.Metadata["pages"])
	}
	if doc2.Metadata["pages_extracted"] != 1 {
		t.Errorf("pages_extracted meta = %v, want 1", doc2.Metadata["pages_extracted"])
	}
	if !strings.Contains(doc2.Text, "PageTwoBeta") {
		t.Errorf("text = %q, want PageTwoBeta", doc2.Text)
	}
	if strings.Contains(doc2.Text, "PageOneAlpha") || strings.Contains(doc2.Text, "PageThreeGamma") {
		t.Errorf("text %q leaked non-selected pages", doc2.Text)
	}
}

func TestExtractPDFBytesPages(t *testing.T) {
	// ExtractBytes must honor page selection and expose the same metadata.
	data := buildMultiPagePDF([]string{"AlphaBytes", "BetaBytes", "GammaBytes"})
	doc, err := ExtractBytes(context.Background(), "multi.pdf", data, WithPages("1,3"))
	if err != nil {
		t.Fatalf("ExtractBytes: %v", err)
	}
	if len(doc.PageTexts) != 2 {
		t.Fatalf("PageTexts len = %d, want 2", len(doc.PageTexts))
	}
	if doc.Metadata["pages"] != 3 || doc.Metadata["pages_extracted"] != 2 {
		t.Errorf("meta = %v", doc.Metadata)
	}
	if strings.Contains(doc.Text, "BetaBytes") {
		t.Errorf("page 2 should be excluded: %q", doc.Text)
	}
}

func TestExtractPptxSlideSelection(t *testing.T) {
	dir := t.TempDir()
	path := writePptx(t, dir, "SlideAaa", "SlideBbb", "SlideCcc")

	doc, err := Extract(context.Background(), path)
	if err != nil {
		t.Fatalf("Extract pptx: %v", err)
	}
	if len(doc.PageTexts) != 3 {
		t.Fatalf("PageTexts len = %d, want 3", len(doc.PageTexts))
	}
	if doc.Metadata["slides"] != 3 || doc.Metadata["slides_extracted"] != 3 {
		t.Errorf("meta = %v", doc.Metadata)
	}

	sel, err := Extract(context.Background(), path, WithPages("2-3"))
	if err != nil {
		t.Fatalf("Extract pptx WithPages: %v", err)
	}
	if len(sel.PageTexts) != 2 {
		t.Fatalf("selected slides = %d, want 2", len(sel.PageTexts))
	}
	if sel.Metadata["slides"] != 3 || sel.Metadata["slides_extracted"] != 2 {
		t.Errorf("selected meta = %v", sel.Metadata)
	}
	if strings.Contains(sel.Text, "SlideAaa") {
		t.Errorf("slide 1 should be excluded: %q", sel.Text)
	}
	if !strings.Contains(sel.Text, "SlideBbb") || !strings.Contains(sel.Text, "SlideCcc") {
		t.Errorf("selected text missing slides 2-3: %q", sel.Text)
	}
}

func TestExtractBytesMagicDetection(t *testing.T) {
	// A docx buffer whose name has no extension: must fall back to magic-byte
	// zip sniffing.
	dir := t.TempDir()
	path := writeDocx(t, dir, "sniff me")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ExtractBytes(context.Background(), "noext", data)
	if err != nil {
		t.Fatalf("ExtractBytes: %v", err)
	}
	if doc.Format != FormatDocx || !strings.Contains(doc.Text, "sniff me") {
		t.Errorf("magic detection failed: %+v", doc)
	}
}
