package lineinput

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

var ErrInputCanceled = errors.New("input canceled")

type editorState struct {
	prompt   string
	buf      []rune
	cursor   int
	lastCols int
}

var (
	editorMu sync.Mutex
	active   *editorState
)

// ReadInteractiveLine reads one editable terminal line with UTF-8 aware cursor
// movement and deletion. When stdin/stdout are not terminals, it falls back to
// simple line reading.
func ReadInteractiveLine(prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return readLineFallback(prompt)
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer func() {
		deactivateEditor()
		_ = term.Restore(fd, oldState)
	}()

	activateEditor(prompt)

	byteBuf := make([]byte, 1)

	for {
		if _, err := os.Stdin.Read(byteBuf); err != nil {
			return "", err
		}

		switch b := byteBuf[0]; b {
		case '\r', '\n':
			return commitEditorLine(), nil
		case 3:
			return "", ErrInputCanceled
		case 4:
			if currentEditorLength() == 0 {
				return "", io.EOF
			}
		case 127, 8:
			backspaceEditor()
		case 27:
			seq, err := readEscapeSequence()
			if err != nil {
				return "", err
			}
			handleEscapeSequence(seq)
		default:
			r, size := DecodeRuneFromReader(os.Stdin, b)
			if size == 0 {
				continue
			}
			insertRune(r)
		}
	}
}

// WriteAsyncLine prints a background status line while preserving the currently
// edited prompt line, if any.
func WriteAsyncLine(line string) {
	line = strings.TrimRight(line, "\n")
	if line == "" {
		return
	}

	editorMu.Lock()
	defer editorMu.Unlock()

	if active == nil {
		fmt.Println(line)
		return
	}

	clearActiveLineLocked()
	fmt.Print(line, "\n")
	renderActiveLocked()
}

// WriteAsyncStream writes streaming text without forcing a trailing newline.
// It is intended for token-by-token output (e.g. delegated agent stream); it
// preserves the prompt line by clearing/redrawing around the chunk.
func WriteAsyncStream(chunk string) {
	if chunk == "" {
		return
	}

	editorMu.Lock()
	defer editorMu.Unlock()

	if active == nil {
		fmt.Print(chunk)
		return
	}

	clearActiveLineLocked()
	fmt.Print(chunk)
	renderActiveLocked()
}

func readLineFallback(prompt string) (string, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) > 0 {
			fmt.Println()
			return strings.TrimRight(line, "\r\n"), nil
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func activateEditor(prompt string) {
	editorMu.Lock()
	defer editorMu.Unlock()
	active = &editorState{prompt: prompt}
	fmt.Print(prompt)
}

func deactivateEditor() {
	editorMu.Lock()
	defer editorMu.Unlock()
	active = nil
}

func currentEditorLength() int {
	editorMu.Lock()
	defer editorMu.Unlock()
	if active == nil {
		return 0
	}
	return len(active.buf)
}

func commitEditorLine() string {
	editorMu.Lock()
	defer editorMu.Unlock()
	if active == nil {
		return ""
	}
	line := string(active.buf)
	padding := max(0, active.lastCols-runewidth.StringWidth(line))
	fmt.Print("\r", active.prompt, line, strings.Repeat(" ", padding), "\r\n")
	return line
}

func backspaceEditor() {
	editorMu.Lock()
	defer editorMu.Unlock()
	if active == nil || active.cursor <= 0 {
		return
	}
	active.buf = append(active.buf[:active.cursor-1], active.buf[active.cursor:]...)
	active.cursor--
	renderActiveLocked()
}

func handleEscapeSequence(seq string) {
	editorMu.Lock()
	defer editorMu.Unlock()
	if active == nil {
		return
	}
	switch seq {
	case "[D":
		if active.cursor > 0 {
			active.cursor--
			renderActiveLocked()
		}
	case "[C":
		if active.cursor < len(active.buf) {
			active.cursor++
			renderActiveLocked()
		}
	case "[3~":
		if active.cursor < len(active.buf) {
			active.buf = append(active.buf[:active.cursor], active.buf[active.cursor+1:]...)
			renderActiveLocked()
		}
	case "[H", "OH":
		if active.cursor != 0 {
			active.cursor = 0
			renderActiveLocked()
		}
	case "[F", "OF":
		if active.cursor != len(active.buf) {
			active.cursor = len(active.buf)
			renderActiveLocked()
		}
	}
}

func insertRune(r rune) {
	editorMu.Lock()
	defer editorMu.Unlock()
	if active == nil {
		return
	}
	active.buf = append(active.buf[:active.cursor], append([]rune{r}, active.buf[active.cursor:]...)...)
	active.cursor++
	renderActiveLocked()
}

func clearActiveLineLocked() {
	if active == nil {
		return
	}
	totalWidth := runewidth.StringWidth(active.prompt) + active.lastCols
	fmt.Print("\r", strings.Repeat(" ", max(0, totalWidth)), "\r")
}

func renderActiveLocked() {
	if active == nil {
		return
	}
	line := string(active.buf)
	displayWidth := runewidth.StringWidth(line)
	if displayWidth < active.lastCols {
		fmt.Print("\r", active.prompt, line, strings.Repeat(" ", active.lastCols-displayWidth), "\r", active.prompt)
	} else {
		fmt.Print("\r", active.prompt, line, "\r", active.prompt)
	}
	if active.cursor > 0 {
		fmt.Print(renderCursorPrefix(string(active.buf[:active.cursor])))
	}
	active.lastCols = displayWidth
}

func readEscapeSequence() (string, error) {
	var seq []byte
	buf := make([]byte, 1)
	for len(seq) < 8 {
		if _, err := os.Stdin.Read(buf); err != nil {
			return "", err
		}
		seq = append(seq, buf[0])
		if (buf[0] >= 'A' && buf[0] <= 'Z') || buf[0] == '~' {
			break
		}
	}
	return string(seq), nil
}

// DecodeRuneFromReader is exported for focused UTF-8 input tests.
func DecodeRuneFromReader(reader io.Reader, first byte) (rune, int) {
	if first < utf8.RuneSelf {
		if first < 32 {
			return 0, 0
		}
		return rune(first), 1
	}

	size := utf8SequenceLength(first)
	if size == 0 {
		return utf8.RuneError, 1
	}
	buf := make([]byte, size)
	buf[0] = first
	for i := 1; i < size; i++ {
		if _, err := reader.Read(buf[i : i+1]); err != nil {
			return utf8.RuneError, 1
		}
	}
	r, n := utf8.DecodeRune(buf)
	if r == utf8.RuneError && n == 1 {
		return utf8.RuneError, 1
	}
	return r, n
}

func utf8SequenceLength(first byte) int {
	switch {
	case first&0xE0 == 0xC0:
		return 2
	case first&0xF0 == 0xE0:
		return 3
	case first&0xF8 == 0xF0:
		return 4
	default:
		return 0
	}
}

func renderCursorPrefix(s string) string {
	width := runewidth.StringWidth(s)
	if width <= 0 {
		return ""
	}
	return fmt.Sprintf("\x1b[%dC", width)
}
