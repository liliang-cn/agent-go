package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Inline & spill: a tool result over the cap is not thrown away.
//
// The model gets an inline preview (head, tail, sizes and a spill id) and the
// full text is kept in a spill store, a directory of files that the built-in
// tool_output_read pages through by id. Lossy truncation (tool_output_cap.go)
// remains the fallback when spilling is off or the store cannot take the
// result.
//
// Lifetime: the store belongs to the Service. Entries expire after a TTL
// (touched by every read), the total size is bounded (oldest entries are
// evicted first) and the directory is removed by Service.Close. Ids live in
// the conversation text, so compaction keeps them readable (see
// spillIDsIn); the file behind an id resolves for as long as it is within
// the TTL, which for a run is its lifetime.

const (
	// spillToolName is the built-in that reads a spilled result back.
	spillToolName = "tool_output_read"

	// DefaultSpillTTL is how long an unread spilled result is kept.
	DefaultSpillTTL = 12 * time.Hour
	// DefaultSpillMaxBytes bounds all spilled results held at once.
	DefaultSpillMaxBytes int64 = 256 << 20
	// spillMaxEntryBytes is the largest single result that is spilled; a
	// larger one is cut the old way.
	spillMaxEntryBytes = 64 << 20

	// spillReadBudget bounds what tool_output_read returns in one call, so
	// its result is itself under the default cap and never re-spills.
	spillReadBudget   = 20000
	spillDefaultLines = 200
	spillMaxLineLen   = 2000
	spillMaxMatches   = 100
	spillPreviewNote  = 400 // room kept for the spill note beside a structured cut
)

var spillIDPattern = regexp.MustCompile(`spill_[0-9a-f]{8}`)

// spillIDsIn lists the spill ids mentioned in s, in order, without repeats.
func spillIDsIn(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range spillIDPattern.FindAllString(s, -1) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

type spillEntry struct {
	size     int64
	lines    int
	created  time.Time
	lastUsed time.Time
}

type spillStore struct {
	mu       sync.Mutex
	dir      string
	maxTotal int64
	ttl      time.Duration
	total    int64
	entries  map[string]*spillEntry
	now      func() time.Time
}

func newSpillStore(dir string, maxTotal int64, ttl time.Duration) *spillStore {
	if maxTotal <= 0 {
		maxTotal = DefaultSpillMaxBytes
	}
	if ttl <= 0 {
		ttl = DefaultSpillTTL
	}
	if dir == "" {
		var b [6]byte
		_, _ = rand.Read(b[:])
		dir = filepath.Join(os.TempDir(), "agentgo-spill", fmt.Sprintf("%d-%s", os.Getpid(), hex.EncodeToString(b[:])))
	}
	return &spillStore{dir: dir, maxTotal: maxTotal, ttl: ttl, entries: map[string]*spillEntry{}, now: time.Now}
}

func (st *spillStore) path(id string) string { return filepath.Join(st.dir, id+".txt") }

// put stores text and returns its id. It fails when the text is too large for
// the store or the disk refuses it; the caller then falls back to a lossy cut.
func (st *spillStore) put(text string) (string, error) {
	size := int64(len(text))
	if size > spillMaxEntryBytes || size > st.maxTotal {
		return "", fmt.Errorf("result of %d bytes is too large to spill", size)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.evictLocked(size)
	if err := os.MkdirAll(st.dir, 0o700); err != nil {
		return "", err
	}
	var id string
	for {
		var b [4]byte
		_, _ = rand.Read(b[:])
		id = "spill_" + hex.EncodeToString(b[:])
		if _, taken := st.entries[id]; !taken {
			break
		}
	}
	if err := os.WriteFile(st.path(id), []byte(text), 0o600); err != nil {
		return "", err
	}
	now := st.now()
	st.entries[id] = &spillEntry{size: size, lines: strings.Count(text, "\n") + 1, created: now, lastUsed: now}
	st.total += size
	return id, nil
}

// evictLocked drops expired entries, then the least recently used ones until
// need more bytes fit.
func (st *spillStore) evictLocked(need int64) {
	now := st.now()
	for id, e := range st.entries {
		if now.Sub(e.lastUsed) > st.ttl {
			st.dropLocked(id)
		}
	}
	if st.total+need <= st.maxTotal {
		return
	}
	ids := make([]string, 0, len(st.entries))
	for id := range st.entries {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return st.entries[ids[i]].lastUsed.Before(st.entries[ids[j]].lastUsed) })
	for _, id := range ids {
		if st.total+need <= st.maxTotal {
			return
		}
		st.dropLocked(id)
	}
}

func (st *spillStore) dropLocked(id string) {
	if e, ok := st.entries[id]; ok {
		st.total -= e.size
		delete(st.entries, id)
	}
	_ = os.Remove(st.path(id))
}

// get returns the full text of id, refreshing its TTL.
func (st *spillStore) get(id string) (string, *spillEntry, error) {
	st.mu.Lock()
	e, ok := st.entries[id]
	if ok && st.now().Sub(e.lastUsed) > st.ttl {
		st.dropLocked(id)
		ok = false
	}
	if ok {
		e.lastUsed = st.now()
	}
	st.mu.Unlock()
	if !ok {
		return "", nil, fmt.Errorf("unknown or expired spill id %q", id)
	}
	b, err := os.ReadFile(st.path(id))
	if err != nil {
		return "", nil, fmt.Errorf("spill %s is no longer readable: %w", id, err)
	}
	return string(b), e, nil
}

// sweep removes expired entries; also called opportunistically by put.
func (st *spillStore) sweep() {
	st.mu.Lock()
	st.evictLocked(0)
	st.mu.Unlock()
}

// close removes every spilled file and the directory.
func (st *spillStore) close() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.entries = map[string]*spillEntry{}
	st.total = 0
	_ = os.RemoveAll(st.dir)
}

// WithToolOutputSpill turns inline-and-spill on or off. On by default: a
// result over the cap (WithToolOutputLimit) is kept whole in a spill store,
// the model reads a head/tail preview with a spill id, and tool_output_read
// pages through the rest. Off restores lossy truncation.
func (b *Builder) WithToolOutputSpill(enabled bool) *Builder {
	b.spillDisabled = !enabled
	return b
}

// WithToolOutputSpillStore sets where spilled results live, how many bytes
// they may take in all and how long an unread one is kept. Zero values keep
// the defaults (a private directory under os.TempDir()/agentgo-spill, 256MB,
// 12h).
func (b *Builder) WithToolOutputSpillStore(dir string, maxBytes int64, ttl time.Duration) *Builder {
	b.spillDir, b.spillMaxBytes, b.spillTTL = dir, maxBytes, ttl
	return b
}

// spillEnabled reports whether over-cap results are spilled.
func (s *Service) spillEnabled() bool {
	return s != nil && !s.spillDisabled && s.toolOutputCap() > 0
}

// spill returns the service's store, creating it on first use.
func (s *Service) spill() *spillStore {
	s.spillMu.Lock()
	defer s.spillMu.Unlock()
	if s.spillStore == nil {
		s.spillStore = newSpillStore(s.spillDir, s.spillMaxBytes, s.spillTTL)
	}
	return s.spillStore
}

// closeSpill deletes the spilled files; called from Close.
func (s *Service) closeSpill() {
	s.spillMu.Lock()
	st := s.spillStore
	s.spillStore = nil
	s.spillMu.Unlock()
	if st != nil {
		st.close()
	}
}

func spillMarkerFunc(id string, totalBytes, totalLines int) func(int, int, int) string {
	return func(omittedBytes, omittedLines, _ int) string {
		return fmt.Sprintf("\n\n[... %d bytes (%d lines) omitted here. Full result: %d bytes, %d lines, saved as %s. "+
			"Read more with %s id=%s offset=<line> limit=<lines>, or grep=<pattern> to find lines.]\n\n",
			omittedBytes, omittedLines, totalBytes, totalLines, id, spillToolName, id)
	}
}

func spillNote(id string, totalBytes, totalLines int) string {
	return fmt.Sprintf("\n[Some fields above are cut. Full result: %d bytes, %d lines, saved as %s. "+
		"Read more with %s id=%s offset=<line> limit=<lines>, or grep=<pattern>.]",
		totalBytes, totalLines, id, spillToolName, id)
}

// spillPreview builds the inline preview for text, which is saved under id.
// A JSON result keeps its shape (long string fields are cut) with the note
// appended; anything else keeps its head and tail around a marker.
func spillPreview(res interface{}, text string, limit int, id string) (string, ToolOutputTruncation) {
	lines := strings.Count(text, "\n") + 1
	info := ToolOutputTruncation{OriginalBytes: len(text), Limit: limit, Spilled: true, SpillID: id, TotalLines: lines}
	note := spillNote(id, len(text), lines)
	if inner := limit - len(note) - 32; inner > minStructuredLeafBytes {
		if out, ok := capStructured(res, text, inner); ok {
			// Stay valid JSON: the note rides in a key of the object.
			dec := json.NewDecoder(strings.NewReader(out))
			dec.UseNumber()
			var obj map[string]interface{}
			if dec.Decode(&obj) == nil && obj != nil {
				obj["_spilled"] = strings.TrimSpace(note)
				if enc, err := marshalNoEscape(obj); err == nil && len(enc) <= limit {
					info.KeptBytes, info.StructureKept = len(enc), true
					return enc, info
				}
			}
		}
	}
	out := truncateMiddle(text, limit, spillMarkerFunc(id, len(text), lines))
	info.KeptBytes = len(out)
	return out, info
}

// registerSpillTool adds tool_output_read.
func (s *Service) registerSpillTool() {
	s.AddToolWithMetadata(
		spillToolName,
		"Read back a tool result that was too large to show in full (its preview names a spill id like spill_1a2b3c4d). "+
			"Pass id and either offset/limit (lines by default, 0-based offset; unit=\"bytes\" pages by byte) or grep (a regular expression) to get matching lines with their line numbers. "+
			"Each call returns a bounded page; next_offset says where to continue.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id":          map[string]interface{}{"type": "string", "description": "Spill id from the preview, e.g. spill_1a2b3c4d"},
				"offset":      map[string]interface{}{"type": "integer", "description": "First line (0-based) or byte to read; default 0"},
				"limit":       map[string]interface{}{"type": "integer", "description": "Max lines (or bytes with unit=bytes); default 200 lines, always bounded"},
				"unit":        map[string]interface{}{"type": "string", "enum": []string{"lines", "bytes"}, "description": "Unit of offset/limit; default lines"},
				"grep":        map[string]interface{}{"type": "string", "description": "Regular expression; returns only matching lines with line numbers"},
				"ignore_case": map[string]interface{}{"type": "boolean", "description": "Case-insensitive grep"},
				"max_matches": map[string]interface{}{"type": "integer", "description": "Max matching lines to return; default 100"},
			},
			"required": []string{"id"},
		},
		func(_ context.Context, args map[string]interface{}) (interface{}, error) {
			return s.spillRead(args), nil
		},
		ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel, OutputLimit: -1},
	)
}

func (s *Service) spillRead(args map[string]interface{}) map[string]interface{} {
	id := toolArgString(args, "id")
	if id == "" {
		return toolErr("id required")
	}
	text, entry, err := s.spill().get(id)
	if err != nil {
		return toolErr(err.Error())
	}
	out := map[string]interface{}{
		"id": id, "total_bytes": entry.size, "total_lines": entry.lines,
	}
	if pat := toolArgString(args, "grep"); pat != "" {
		return spillGrep(text, pat, toolArgBool(args, "ignore_case"), toolArgInt(args, "max_matches"), out)
	}
	offset, limit := toolArgInt(args, "offset"), toolArgInt(args, "limit")
	if offset < 0 {
		offset = 0
	}
	if strings.EqualFold(toolArgString(args, "unit"), "bytes") {
		return spillReadBytes(text, offset, limit, out)
	}
	return spillReadLines(text, offset, limit, out)
}

func clipLine(l string) string {
	if len(l) <= spillMaxLineLen {
		return l
	}
	cut := spillMaxLineLen
	for cut > 0 && !utf8.RuneStart(l[cut]) {
		cut--
	}
	return fmt.Sprintf("%s… [line cut: %d of %d bytes shown]", l[:cut], cut, len(l))
}

func spillReadLines(text string, offset, limit int, out map[string]interface{}) map[string]interface{} {
	lines := strings.Split(text, "\n")
	if offset > len(lines) {
		offset = len(lines)
	}
	if limit <= 0 {
		limit = spillDefaultLines
	}
	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	i := offset
	for ; i < end; i++ {
		rendered := fmt.Sprintf("%6d\t%s\n", i+1, clipLine(lines[i]))
		if i > offset && b.Len()+len(rendered) > spillReadBudget {
			break
		}
		b.WriteString(rendered)
	}
	out["content"] = b.String()
	if i < len(lines) {
		out["next_offset"] = i
		fmt.Fprintf(&b, "[lines %d-%d of %d shown; call %s with offset=%d to continue]\n", offset+1, i, len(lines), spillToolName, i)
		out["content"] = b.String()
	}
	return toolOK(out)
}

func spillReadBytes(text string, offset, limit int, out map[string]interface{}) map[string]interface{} {
	if offset > len(text) {
		offset = len(text)
	}
	for offset < len(text) && !utf8.RuneStart(text[offset]) {
		offset++
	}
	if limit <= 0 || limit > spillReadBudget {
		limit = spillReadBudget
	}
	end := offset + limit
	if end > len(text) {
		end = len(text)
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	out["content"] = text[offset:end]
	out["start_byte"], out["end_byte"] = offset, end
	if end < len(text) {
		out["next_offset"] = end
	}
	return toolOK(out)
}

func spillGrep(text, pat string, ignoreCase bool, maxMatches int, out map[string]interface{}) map[string]interface{} {
	expr := pat
	if ignoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		re = regexp.MustCompile(regexp.QuoteMeta(pat))
		if ignoreCase {
			re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(pat))
		}
	}
	if maxMatches <= 0 || maxMatches > spillMaxMatches*5 {
		maxMatches = spillMaxMatches
	}
	var b strings.Builder
	matches, shown := 0, 0
	for n, l := range strings.Split(text, "\n") {
		if !re.MatchString(l) {
			continue
		}
		matches++
		rendered := fmt.Sprintf("%6d\t%s\n", n+1, clipLine(l))
		if shown >= maxMatches || (shown > 0 && b.Len()+len(rendered) > spillReadBudget) {
			continue // keep counting
		}
		b.WriteString(rendered)
		shown++
	}
	out["matches"], out["shown"] = matches, shown
	if shown < matches {
		fmt.Fprintf(&b, "[%d of %d matching lines shown; narrow the pattern or raise max_matches]\n", shown, matches)
	}
	out["content"] = b.String()
	return toolOK(out)
}

// capOrSpill bounds one rendered tool result: spilled when the store takes
// it, cut in the middle otherwise. tool_output_read results are never
// spilled again.
func (s *Service) capOrSpill(toolName string, res interface{}, text string, limit int) (string, ToolOutputTruncation, bool) {
	if limit <= 0 || len(text) <= limit {
		return text, ToolOutputTruncation{}, false
	}
	if toolName != spillToolName && s.spillEnabled() {
		if id, err := s.spill().put(text); err == nil {
			out, info := spillPreview(res, text, limit, id)
			return out, info, true
		}
	}
	return capToolResult(res, text, limit)
}
