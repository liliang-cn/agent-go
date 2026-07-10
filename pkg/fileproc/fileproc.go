// Package fileproc extracts plain text and lightweight metadata from common
// document formats — Word (.docx), Excel (.xlsx), PowerPoint (.pptx), PDF, and
// images — so an agent can "read" a file's content without a vision model or
// OCR.
//
// It is deliberately CGO-free and depends only on permissively licensed
// libraries: OOXML formats are parsed with the standard library's archive/zip
// and encoding/xml, spreadsheets with github.com/xuri/excelize/v2 (BSD-3), PDFs
// with github.com/dslipak/pdf (MIT), and images with the standard library plus
// golang.org/x/image (BSD-3). There is no OCR: images yield dimensions and an
// optional inline data URI (for a vision-capable agent), never extracted text.
package fileproc

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	// Image decoders self-register with image.DecodeConfig via their init().
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	pdf "github.com/dslipak/pdf"
	"github.com/xuri/excelize/v2"
)

// Format constants returned in Document.Format.
const (
	FormatDocx  = "docx"
	FormatXlsx  = "xlsx"
	FormatPptx  = "pptx"
	FormatPDF   = "pdf"
	FormatImage = "image"
	FormatText  = "text"
)

// DefaultMaxBytes caps the raw input size to guard against zip bombs and
// runaway files. Override with WithMaxBytes.
const DefaultMaxBytes int64 = 50 << 20 // 50 MB

// imageDataURILimit is the largest image (in bytes) inlined as a base64 data
// URI in Metadata["data_uri"]. Larger images set Metadata["data_uri_omitted"].
const imageDataURILimit = 256 << 10 // 256 KB

// Document is the format-agnostic result of extraction.
type Document struct {
	Format    string         // "docx" | "xlsx" | "pptx" | "pdf" | "image" | "text"
	Text      string         // extracted plain text (empty for images)
	PageTexts []string       // per-page/per-slide text; populated for pdf and pptx, nil otherwise
	Metadata  map[string]any // format-specific: pages, sheets, sheet_names, width, height, ...
	Truncated bool           // true when Text was capped by WithMaxChars
}

type config struct {
	maxChars int
	maxBytes int64
	pages    string
}

// Option configures extraction.
type Option func(*config)

// WithMaxChars caps the length of Document.Text (in runes). When the extracted
// text is longer it is truncated and Document.Truncated is set. 0 (default)
// means no limit.
func WithMaxChars(n int) Option {
	return func(c *config) {
		if n < 0 {
			n = 0
		}
		c.maxChars = n
	}
}

// WithMaxBytes overrides the raw input size cap (default DefaultMaxBytes).
// A non-positive value disables the cap.
func WithMaxBytes(n int64) Option {
	return func(c *config) { c.maxBytes = n }
}

// WithPages restricts extraction to a 1-indexed page/slide selection such as
// "1-8,50,100-120" (ranges and singletons, spaces tolerated). An empty spec
// means all pages. Out-of-range indices are skipped; a malformed (non-numeric)
// token makes extraction fail. Applies to pdf (pages) and pptx (slides); it is
// ignored for formats without pages.
func WithPages(spec string) Option {
	return func(c *config) { c.pages = strings.TrimSpace(spec) }
}

// ParsePageSpec parses a 1-indexed page selector like "1-8,50,100-120" against a
// known total page count and returns the selected page numbers, sorted, de-duped
// and clamped to the range 1..total. An empty spec selects every page. Reversed
// ranges ("8-1") are normalized. Out-of-range numbers are dropped silently; a
// non-numeric token returns an error.
func ParsePageSpec(spec string, total int) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if total < 0 {
		total = 0
	}
	if spec == "" {
		out := make([]int, 0, total)
		for i := 1; i <= total; i++ {
			out = append(out, i)
		}
		return out, nil
	}
	seen := make(map[int]bool)
	var out []int
	add := func(n int) {
		if n >= 1 && n <= total && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if idx := strings.Index(part, "-"); idx >= 0 {
			lo := strings.TrimSpace(part[:idx])
			hi := strings.TrimSpace(part[idx+1:])
			l, err1 := strconv.Atoi(lo)
			h, err2 := strconv.Atoi(hi)
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("fileproc: invalid page range %q", part)
			}
			if l > h {
				l, h = h, l
			}
			for i := l; i <= h; i++ {
				add(i)
			}
		} else {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("fileproc: invalid page number %q", part)
			}
			add(n)
		}
	}
	sort.Ints(out)
	return out, nil
}

func newConfig(opts []Option) *config {
	c := &config{maxBytes: DefaultMaxBytes}
	for _, o := range opts {
		if o != nil {
			o(c)
		}
	}
	return c
}

// Extract reads a file from disk and extracts its content. Format is detected
// from the extension first, then from magic bytes.
//
// PDFs are a special case: they stream from disk page-by-page (via pdf.Open)
// rather than being slurped into memory, so the WithMaxBytes cap does not apply
// to them and multi-hundred-megabyte PDFs extract without exhausting RAM. Every
// other format is read fully into memory and still honors the byte cap.
func Extract(ctx context.Context, path string, opts ...Option) (*Document, error) {
	cfg := newConfig(opts)
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("fileproc: stat %q: %w", path, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("fileproc: %q is a directory, not a file", path)
	}
	name := filepath.Base(path)

	// PDFs stream from disk (no whole-file read, no byte cap) so large course
	// PDFs work; page selection + per-page logic is shared with ExtractBytes.
	if detectFormatPath(name, path) == FormatPDF {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		doc, err := extractPDFPath(path, cfg)
		if err != nil {
			return nil, fmt.Errorf("fileproc: %q (%s): %w", name, FormatPDF, err)
		}
		if doc.Metadata == nil {
			doc.Metadata = map[string]any{}
		}
		applyMaxChars(doc, cfg.maxChars)
		return doc, nil
	}

	if cfg.maxBytes > 0 && fi.Size() > cfg.maxBytes {
		return nil, fmt.Errorf("fileproc: %q is %d bytes, exceeds cap of %d bytes", path, fi.Size(), cfg.maxBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fileproc: read %q: %w", path, err)
	}
	return extract(ctx, name, data, cfg)
}

// ExtractBytes extracts content from an in-memory buffer. name provides the
// extension used for format detection (magic bytes are the fallback).
func ExtractBytes(ctx context.Context, name string, data []byte, opts ...Option) (*Document, error) {
	return extract(ctx, name, data, newConfig(opts))
}

func extract(ctx context.Context, name string, data []byte, cfg *config) (*Document, error) {
	if cfg.maxBytes > 0 && int64(len(data)) > cfg.maxBytes {
		return nil, fmt.Errorf("fileproc: %q is %d bytes, exceeds cap of %d bytes", name, len(data), cfg.maxBytes)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	format := detectFormat(name, data)

	var (
		doc *Document
		err error
	)
	switch format {
	case FormatDocx:
		doc, err = extractDocx(data)
	case FormatXlsx:
		doc, err = extractXlsx(data)
	case FormatPptx:
		doc, err = extractPptx(data, cfg)
	case FormatPDF:
		doc, err = extractPDF(data, cfg)
	case FormatImage:
		doc, err = extractImage(name, data)
	case FormatText:
		doc, err = extractText(data)
	default:
		return nil, fmt.Errorf("fileproc: %q: unsupported format (not docx/xlsx/pptx/pdf/image/text)", name)
	}
	if err != nil {
		return nil, fmt.Errorf("fileproc: %q (%s): %w", name, format, err)
	}
	if doc.Metadata == nil {
		doc.Metadata = map[string]any{}
	}
	applyMaxChars(doc, cfg.maxChars)
	return doc, nil
}

func applyMaxChars(doc *Document, maxChars int) {
	if maxChars <= 0 || doc.Text == "" {
		return
	}
	if utf8.RuneCountInString(doc.Text) <= maxChars {
		return
	}
	runes := []rune(doc.Text)
	doc.Text = string(runes[:maxChars])
	doc.Truncated = true
}

// detectFormat resolves a format by extension first, then magic bytes.
func detectFormat(name string, data []byte) string {
	if f := formatByExt(name); f != "" {
		return f
	}
	return sniffFormat(data)
}

// formatByExt resolves a format from the filename extension alone, returning ""
// when the extension is unknown.
func formatByExt(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".docx":
		return FormatDocx
	case ".xlsx":
		return FormatXlsx
	case ".pptx":
		return FormatPptx
	case ".pdf":
		return FormatPDF
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		return FormatImage
	case ".txt", ".text", ".md", ".markdown", ".csv", ".tsv", ".log",
		".json", ".xml", ".yaml", ".yml", ".html", ".htm", ".go", ".py", ".js", ".ts":
		return FormatText
	}
	return ""
}

// detectFormatPath resolves a format for a file on disk without reading the
// whole file: it tries the extension first, then peeks at a small header. It is
// used to route PDFs to the streaming (no byte cap) extraction path. Formats
// other than PDF may return "" here; the caller falls back to a full read that
// re-detects from the complete bytes.
func detectFormatPath(name, path string) string {
	if f := formatByExt(name); f != "" {
		return f
	}
	fh, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer fh.Close()
	buf := make([]byte, 512)
	n, _ := io.ReadFull(fh, buf)
	return sniffFormat(buf[:n])
}

func sniffFormat(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("%PDF")):
		return FormatPDF
	case bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return FormatImage
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}): // JPEG / JFIF
		return FormatImage
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return FormatImage
	case bytes.HasPrefix(data, []byte("BM")): // BMP
		return FormatImage
	case len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return FormatImage
	case bytes.HasPrefix(data, []byte{'I', 'I', 0x2A, 0x00}), bytes.HasPrefix(data, []byte{'M', 'M', 0x00, 0x2A}): // TIFF
		return FormatImage
	case bytes.HasPrefix(data, []byte("PK\x03\x04")), bytes.HasPrefix(data, []byte("PK\x05\x06")):
		if f := sniffZip(data); f != "" {
			return f
		}
	}
	if utf8.Valid(data) {
		return FormatText
	}
	return ""
}

// sniffZip inspects an OOXML zip's internal layout to tell docx/xlsx/pptx apart.
func sniffZip(data []byte) string {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ""
	}
	for _, f := range zr.File {
		switch {
		case f.Name == "word/document.xml" || strings.HasPrefix(f.Name, "word/"):
			return FormatDocx
		case f.Name == "xl/workbook.xml" || strings.HasPrefix(f.Name, "xl/"):
			return FormatXlsx
		case f.Name == "ppt/presentation.xml" || strings.HasPrefix(f.Name, "ppt/slides/"):
			return FormatPptx
		}
	}
	return ""
}

// --- docx ------------------------------------------------------------------

func extractDocx(data []byte) (*Document, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	xmlData, err := readZipEntry(zr, "word/document.xml")
	if err != nil {
		return nil, err
	}
	text, paras := extractOOXMLText(xmlData)
	return &Document{
		Format: FormatDocx,
		Text:   text,
		Metadata: map[string]any{
			"paragraphs": paras,
		},
	}, nil
}

// --- pptx ------------------------------------------------------------------

var pptxSlideRe = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)

func extractPptx(data []byte, cfg *config) (*Document, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	type slide struct {
		idx int
		f   *zip.File
	}
	var slides []slide
	for _, f := range zr.File {
		if m := pptxSlideRe.FindStringSubmatch(f.Name); m != nil {
			n, _ := strconv.Atoi(m[1])
			slides = append(slides, slide{idx: n, f: f})
		}
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].idx < slides[j].idx })

	total := len(slides)
	selected, err := ParsePageSpec(cfg.pages, total)
	if err != nil {
		return nil, err
	}

	parts := make([]string, 0, len(selected))
	pageTexts := make([]string, 0, len(selected))
	for _, n := range selected {
		s := slides[n-1] // ParsePageSpec guarantees 1 <= n <= total
		rc, err := s.f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", s.f.Name, err)
		}
		xmlData, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", s.f.Name, err)
		}
		text, _ := extractOOXMLText(xmlData)
		parts = append(parts, text)
		pageTexts = append(pageTexts, text)
	}
	return &Document{
		Format:    FormatPptx,
		Text:      strings.Join(parts, "\n\n"),
		PageTexts: pageTexts,
		Metadata: map[string]any{
			"slides":           total,
			"slides_extracted": len(selected),
		},
	}, nil
}

// extractOOXMLText pulls text runs out of a WordprocessingML/DrawingML part.
// Both <w:t> (Word) and <a:t> (PowerPoint/Drawing) carry text and share the
// local element name "t"; paragraphs share the local name "p". It returns the
// joined text and the paragraph count.
func extractOOXMLText(xmlData []byte) (string, int) {
	dec := xml.NewDecoder(bytes.NewReader(xmlData))
	var b strings.Builder
	inText := false
	paras := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				b.WriteByte('\n')
				paras++
			}
		}
	}
	return strings.TrimRight(b.String(), "\n"), paras
}

// --- xlsx ------------------------------------------------------------------

func extractXlsx(data []byte) (*Document, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open workbook: %w", err)
	}
	defer f.Close()

	names := f.GetSheetList()
	var b strings.Builder
	for i, name := range names {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("# ")
		b.WriteString(name)
		b.WriteByte('\n')
		rows, err := f.GetRows(name)
		if err != nil {
			return nil, fmt.Errorf("read sheet %q: %w", name, err)
		}
		for _, row := range rows {
			b.WriteString(strings.Join(row, "\t"))
			b.WriteByte('\n')
		}
	}
	return &Document{
		Format: FormatXlsx,
		Text:   strings.TrimRight(b.String(), "\n"),
		Metadata: map[string]any{
			"sheets":      len(names),
			"sheet_names": names,
		},
	}, nil
}

// --- pdf -------------------------------------------------------------------

// extractPDF reads a PDF from an in-memory buffer (used by ExtractBytes, which
// still honors the byte cap).
func extractPDF(data []byte, cfg *config) (*Document, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open pdf: %w", err)
	}
	return extractPDFReader(r, cfg)
}

// extractPDFPath streams a PDF from disk page-by-page (used by Extract for large
// files; no whole-file read and no byte cap).
func extractPDFPath(path string, cfg *config) (*Document, error) {
	r, err := pdf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open pdf: %w", err)
	}
	return extractPDFReader(r, cfg)
}

// extractPDFReader is the shared page-selection + per-page logic behind both PDF
// entry points. PageTexts holds one entry per selected page (empty string for a
// null or unreadable page); Text joins the non-empty pages.
func extractPDFReader(r *pdf.Reader, cfg *config) (*Document, error) {
	total := r.NumPage()
	selected, err := ParsePageSpec(cfg.pages, total)
	if err != nil {
		return nil, err
	}
	pageTexts := make([]string, 0, len(selected))
	var b strings.Builder
	for _, i := range selected {
		p := r.Page(i)
		if p.V.IsNull() {
			pageTexts = append(pageTexts, "")
			continue
		}
		text, err := p.GetPlainText(nil)
		if err != nil {
			// Skip an unreadable page rather than failing the whole document.
			pageTexts = append(pageTexts, "")
			continue
		}
		pageTexts = append(pageTexts, text)
		if text == "" {
			continue
		}
		b.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			b.WriteByte('\n')
		}
	}
	return &Document{
		Format:    FormatPDF,
		Text:      strings.TrimRight(b.String(), "\n"),
		PageTexts: pageTexts,
		Metadata: map[string]any{
			"pages":           total,
			"pages_extracted": len(selected),
		},
	}, nil
}

// --- image -----------------------------------------------------------------

func extractImage(name string, data []byte) (*Document, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image config: %w", err)
	}
	meta := map[string]any{
		"width":      cfg.Width,
		"height":     cfg.Height,
		"format":     format,
		"size_bytes": len(data),
	}
	if len(data) <= imageDataURILimit {
		meta["data_uri"] = "data:" + imageMIME(format) + ";base64," + base64.StdEncoding.EncodeToString(data)
	} else {
		meta["data_uri_omitted"] = fmt.Sprintf("image is %d bytes, over the %d-byte inline limit", len(data), imageDataURILimit)
	}
	return &Document{
		Format:   FormatImage,
		Text:     "", // no OCR
		Metadata: meta,
	}, nil
}

func imageMIME(format string) string {
	switch format {
	case "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "bmp":
		return "image/bmp"
	case "tiff":
		return "image/tiff"
	default:
		return "application/octet-stream"
	}
}

// --- text ------------------------------------------------------------------

func extractText(data []byte) (*Document, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("not valid UTF-8 text and no recognized binary format")
	}
	return &Document{
		Format:   FormatText,
		Text:     string(data),
		Metadata: map[string]any{"size_bytes": len(data)},
	}, nil
}

// --- helpers ---------------------------------------------------------------

func readZipEntry(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("open %s: %w", name, err)
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", name, err)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("%s not found in archive", name)
}
