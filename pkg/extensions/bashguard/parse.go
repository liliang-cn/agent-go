package bashguard

import (
	"path"
	"strings"
)

// Command is one simple command after normalisation: wrappers such as sudo,
// env and command are stripped, quotes are removed, and the home directory is
// spelled "~" however the model wrote it (~, $HOME, ${HOME}). A "~" that the
// shell would not expand — one inside quotes — is spelled "./~", which is
// what it names.
type Command struct {
	// Name is the program's base name ("rm" for /bin/rm).
	Name string
	// Args are the words after the name, unquoted.
	Args []string
	// Redirects are the command's redirections in source order.
	Redirects []Redirect
	// Sudo is true when the command was run through sudo or doas.
	Sudo bool
	// Nested holds the pipelines inside this command's $( ), backtick,
	// <( ) and sh -c substitutions. They are also in Script.Pipelines.
	Nested []Pipeline
	// Text is the normalised command, words joined by one space.
	Text string
}

// Redirect is one redirection: Op is the operator (">", ">>", "<", "&>", ...)
// and Target the word after it.
type Redirect struct {
	Op     string
	Target string
}

// Pipeline is a run of commands joined by | or |&.
type Pipeline struct {
	Commands []Command
	// Text is the normalised pipeline, commands joined by " | ".
	Text string
}

// Script is a parsed command line. Pipelines is flat: the top-level
// pipelines followed by every pipeline nested in a substitution, so a rule
// sees a command wherever it runs.
type Script struct {
	Pipelines []Pipeline
}

// Parse splits a shell command line into pipelines of simple commands. It
// never fails: an unterminated quote or substitution runs to the end of the
// input, which is how a guard should read something it cannot fully parse.
func Parse(src string) Script {
	return parseDepth(src, 0)
}

const maxDepth = 16

func parseDepth(src string, depth int) Script {
	l := &lexer{src: []rune(src), depth: depth}
	toks := l.tokens()
	var s Script
	var nested []Pipeline

	var pipe []Command
	var words []word
	var redirs []Redirect
	flushCmd := func() {
		if c, ok := buildCommand(words, redirs, depth, &nested); ok {
			pipe = append(pipe, c)
		}
		words, redirs = nil, nil
	}
	flushPipe := func() {
		flushCmd()
		if len(pipe) > 0 {
			s.Pipelines = append(s.Pipelines, newPipeline(pipe))
		}
		pipe = nil
	}
	for _, t := range toks {
		switch t.kind {
		case tokWord:
			words = append(words, t.w)
		case tokRedir:
			redirs = append(redirs, Redirect{Op: t.op, Target: t.w.text})
			nested = append(nested, t.w.nested...)
		case tokOp:
			if t.op == "|" || t.op == "|&" {
				flushCmd()
			} else {
				flushPipe()
			}
		}
	}
	flushPipe()
	s.Pipelines = append(s.Pipelines, nested...)
	return s
}

func newPipeline(cmds []Command) Pipeline {
	texts := make([]string, len(cmds))
	for i, c := range cmds {
		texts[i] = c.Text
	}
	return Pipeline{Commands: cmds, Text: strings.Join(texts, " | ")}
}

// prefixes that run the rest of the command line as a command.
var wrappers = map[string]bool{
	"sudo": true, "doas": true, "env": true, "command": true, "exec": true,
	"nohup": true, "time": true, "nice": true, "builtin": true,
}

// words that start a compound command or negate one; the command follows.
var keywords = map[string]bool{
	"!": true, "{": true, "}": true, "(": true, ")": true, "then": true, "do": true,
	"else": true, "elif": true, "if": true, "while": true, "until": true,
}

// sudo options that take a value as the next word.
var sudoValueOpts = map[string]bool{
	"-u": true, "-g": true, "-C": true, "-D": true, "-h": true, "-p": true,
	"-r": true, "-t": true, "-U": true, "-T": true, "-R": true,
	"--user": true, "--group": true, "--chdir": true, "--prompt": true,
}

var shells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true, "ash": true,
}

func isAssignment(w word) bool {
	if w.quotedStart {
		return false
	}
	eq := strings.IndexByte(w.text, '=')
	if eq <= 0 {
		return false
	}
	for i, r := range w.text[:eq] {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func buildCommand(ws []word, redirs []Redirect, depth int, nested *[]Pipeline) (Command, bool) {
	var c Command
	c.Redirects = redirs
	for _, w := range ws {
		c.Nested = append(c.Nested, w.nested...)
	}
	*nested = append(*nested, collect(ws)...)

	i := 0
	lastWrapper := -1
	for i < len(ws) {
		t := ws[i].text
		switch {
		case keywords[t] && !ws[i].quotedStart:
			i++
		case isAssignment(ws[i]):
			i++
		case wrappers[path.Base(t)]: // quoting a command name does not stop it running
			name := path.Base(t)
			lastWrapper = i
			i++
			if name == "sudo" || name == "doas" {
				c.Sudo = true
			}
			for i < len(ws) {
				a := ws[i].text
				if a == "--" {
					i++
					break
				}
				if name == "env" && isAssignment(ws[i]) {
					i++
					continue
				}
				if !strings.HasPrefix(a, "-") || a == "-" {
					break
				}
				i++
				switch name {
				case "sudo", "doas":
					if sudoValueOpts[a] {
						i++
					}
				case "env":
					if a == "-u" || a == "-C" || a == "--unset" || a == "--chdir" {
						i++
					}
				case "nice":
					if a == "-n" {
						i++
					}
				}
			}
		default:
			goto done
		}
	}
done:
	if i >= len(ws) && lastWrapper >= 0 {
		// "env" or "command" alone is itself the command (env prints the
		// environment).
		i = lastWrapper
	}
	if i >= len(ws) {
		if len(redirs) == 0 {
			return Command{}, false
		}
		c.Text = redirText(redirs)
		return c, true
	}
	c.Name = path.Base(ws[i].text)
	for _, w := range ws[i+1:] {
		c.Args = append(c.Args, w.text)
	}

	// sh -c 'script' and eval run their argument as a command line.
	if depth < maxDepth {
		var inner string
		if shells[c.Name] {
			for j, a := range c.Args {
				if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "c") {
					if j+1 < len(c.Args) {
						inner = c.Args[j+1]
					}
					break
				}
			}
		} else if c.Name == "eval" {
			inner = strings.Join(c.Args, " ")
		}
		if inner != "" {
			sub := parseDepth(inner, depth+1)
			c.Nested = append(c.Nested, sub.Pipelines...)
			*nested = append(*nested, sub.Pipelines...)
		}
	}

	parts := append([]string{c.Name}, c.Args...)
	c.Text = strings.Join(parts, " ")
	if r := redirText(redirs); r != "" {
		c.Text += " " + r
	}
	return c, true
}

func redirText(rs []Redirect) string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = r.Op + " " + r.Target
	}
	return strings.Join(parts, " ")
}

func collect(ws []word) []Pipeline {
	var out []Pipeline
	for _, w := range ws {
		out = append(out, w.nested...)
	}
	return out
}

// ---- lexer ----

type tokKind int

const (
	tokWord tokKind = iota
	tokOp
	tokRedir
)

type word struct {
	text        string
	nested      []Pipeline
	quotedStart bool // the first character came from a quote or escape
}

type token struct {
	kind tokKind
	op   string
	w    word
}

type lexer struct {
	src      []rune
	i        int
	depth    int
	heredocs []string // delimiters waiting for the next newline
}

func (l *lexer) peek(off int) rune {
	if l.i+off < len(l.src) {
		return l.src[l.i+off]
	}
	return 0
}

func (l *lexer) tokens() []token {
	var out []token
	for l.i < len(l.src) {
		r := l.src[l.i]
		switch {
		case r == ' ' || r == '\t' || r == '\r':
			l.i++
		case r == '\\' && l.peek(1) == '\n':
			l.i += 2
		case r == '\n':
			l.i++
			out = append(out, token{kind: tokOp, op: ";"})
			l.skipHeredocs()
		case r == '#':
			for l.i < len(l.src) && l.src[l.i] != '\n' {
				l.i++
			}
		case r == '&' && l.peek(1) == '&', r == '|' && l.peek(1) == '|':
			out = append(out, token{kind: tokOp, op: string([]rune{r, r})})
			l.i += 2
		case r == '|' && l.peek(1) == '&':
			out = append(out, token{kind: tokOp, op: "|&"})
			l.i += 2
		case r == '&' && l.peek(1) == '>':
			out = append(out, l.redirect())
		case r == '|' || r == ';' || r == '&' || r == '(' || r == ')':
			out = append(out, token{kind: tokOp, op: string(r)})
			l.i++
		case (r == '<' || r == '>') && l.peek(1) == '(':
			out = append(out, token{kind: tokWord, w: l.word()})
		case r == '<' || r == '>' || (r >= '0' && r <= '9' && l.fdRedirect()):
			out = append(out, l.redirect())
		default:
			out = append(out, token{kind: tokWord, w: l.word()})
		}
	}
	return out
}

// fdRedirect reports whether digits at the cursor are a file descriptor
// prefix of a redirection (2>, 1>>).
func (l *lexer) fdRedirect() bool {
	j := l.i
	for j < len(l.src) && l.src[j] >= '0' && l.src[j] <= '9' {
		j++
	}
	return j < len(l.src) && (l.src[j] == '>' || l.src[j] == '<')
}

func (l *lexer) redirect() token {
	fd := l.i
	for l.i < len(l.src) && l.src[l.i] >= '0' && l.src[l.i] <= '9' {
		l.i++
	}
	prefix := string(l.src[fd:l.i])
	ops := []string{"&>>", "&>", "<<<", "<<-", "<<", "<>", "<&", ">>", ">|", ">&", "<", ">"}
	op := ""
	for _, o := range ops {
		if strings.HasPrefix(string(l.src[l.i:min(len(l.src), l.i+len(o))]), o) {
			op = o
			break
		}
	}
	l.i += len(op)
	for l.i < len(l.src) && (l.src[l.i] == ' ' || l.src[l.i] == '\t') {
		l.i++
	}
	w := l.word()
	if op == "<<" || op == "<<-" {
		l.heredocs = append(l.heredocs, w.text)
	}
	return token{kind: tokRedir, op: prefix + op, w: w}
}

// skipHeredocs consumes the bodies of here-documents opened on the line just
// ended. A body is data, not commands.
func (l *lexer) skipHeredocs() {
	for len(l.heredocs) > 0 {
		delim := l.heredocs[0]
		l.heredocs = l.heredocs[1:]
		for l.i < len(l.src) {
			end := l.i
			for end < len(l.src) && l.src[end] != '\n' {
				end++
			}
			line := strings.TrimLeft(string(l.src[l.i:end]), "\t")
			l.i = min(end+1, len(l.src))
			if line == delim {
				break
			}
		}
	}
}

func isWordEnd(r rune) bool {
	switch r {
	case ' ', '\t', '\r', '\n', ';', '&', '|', '(', ')', '<', '>':
		return true
	}
	return false
}

const home = "~"

func (l *lexer) word() word {
	var b strings.Builder
	var w word
	start := true
	for l.i < len(l.src) {
		r := l.src[l.i]
		if start && (r == '<' || r == '>') && l.peek(1) == '(' {
			// process substitution <( ) / >( )
			l.i += 2
			inner := l.balanced(')')
			b.WriteString(string(r) + "(" + inner + ")")
			w.nested = append(w.nested, l.sub(inner)...)
			start = false
			continue
		}
		if isWordEnd(r) {
			break
		}
		switch {
		case r == '\'':
			if start {
				w.quotedStart = true
			}
			l.i++
			s := l.i
			for l.i < len(l.src) && l.src[l.i] != '\'' {
				l.i++
			}
			lit := string(l.src[s:min(l.i, len(l.src))])
			if start && strings.HasPrefix(lit, "~") {
				lit = "./" + lit
			}
			b.WriteString(lit)
			l.i++
		case r == '"':
			if start {
				w.quotedStart = true
			}
			l.i++
			l.dquote(&b, &w, start)
		case r == '\\':
			if start {
				w.quotedStart = true
			}
			if l.i+1 < len(l.src) {
				c := l.src[l.i+1]
				if start && c == '~' {
					b.WriteString("./")
				}
				b.WriteRune(c)
			}
			l.i += 2
		case r == '~' && start:
			// ~ and ~user expand; everything else about the word follows.
			b.WriteString(home)
			l.i++
		case r == '$':
			l.dollar(&b, &w)
		case r == '`':
			l.i++
			inner := l.until('`')
			b.WriteString("`" + inner + "`")
			w.nested = append(w.nested, l.sub(inner)...)
		default:
			b.WriteRune(r)
			l.i++
		}
		start = false
	}
	w.text = b.String()
	return w
}

// dquote reads the body of a double-quoted string; the opening quote has
// been consumed.
func (l *lexer) dquote(b *strings.Builder, w *word, start bool) {
	first := true
	for l.i < len(l.src) {
		r := l.src[l.i]
		switch {
		case r == '"':
			l.i++
			return
		case r == '\\':
			if l.i+1 < len(l.src) {
				c := l.src[l.i+1]
				if c == '"' || c == '\\' || c == '$' || c == '`' {
					b.WriteRune(c)
				} else if c != '\n' {
					b.WriteRune('\\')
					b.WriteRune(c)
				}
			}
			l.i += 2
		case r == '$':
			l.dollar(b, w)
		case r == '`':
			l.i++
			inner := l.until('`')
			b.WriteString("`" + inner + "`")
			w.nested = append(w.nested, l.sub(inner)...)
		default:
			if first && start && r == '~' {
				b.WriteString("./")
			}
			b.WriteRune(r)
			l.i++
		}
		first = false
	}
}

// dollar reads $HOME, ${HOME}, $( ) and leaves other expansions verbatim.
func (l *lexer) dollar(b *strings.Builder, w *word) {
	rest := string(l.src[l.i:min(len(l.src), l.i+8)])
	switch {
	case strings.HasPrefix(rest, "${HOME}"):
		b.WriteString(home)
		l.i += len("${HOME}")
	case strings.HasPrefix(rest, "$HOME") && !isNameRune(l.peek(5)):
		b.WriteString(home)
		l.i += len("$HOME")
	case l.peek(1) == '(':
		l.i += 2
		inner := l.balanced(')')
		b.WriteString("$(" + inner + ")")
		w.nested = append(w.nested, l.sub(inner)...)
	default:
		b.WriteRune('$')
		l.i++
	}
}

func isNameRune(r rune) bool {
	return r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
}

func (l *lexer) sub(inner string) []Pipeline {
	if l.depth >= maxDepth {
		return nil
	}
	return parseDepth(inner, l.depth+1).Pipelines
}

// until returns the text up to an unescaped close rune and consumes it.
func (l *lexer) until(close rune) string {
	s := l.i
	for l.i < len(l.src) && l.src[l.i] != close {
		if l.src[l.i] == '\\' {
			l.i++
		}
		l.i++
	}
	out := string(l.src[s:min(l.i, len(l.src))])
	l.i++
	return out
}

// balanced returns the text up to the close paren matching one already
// consumed, respecting quotes, and consumes the close paren.
func (l *lexer) balanced(close rune) string {
	s := l.i
	depth := 1
	for l.i < len(l.src) {
		r := l.src[l.i]
		switch r {
		case '\\':
			l.i++
		case '\'':
			l.i++
			for l.i < len(l.src) && l.src[l.i] != '\'' {
				l.i++
			}
		case '"':
			l.i++
			for l.i < len(l.src) && l.src[l.i] != '"' {
				if l.src[l.i] == '\\' {
					l.i++
				}
				l.i++
			}
		case '(':
			depth++
		case close:
			depth--
			if depth == 0 {
				out := string(l.src[s:l.i])
				l.i++
				return out
			}
		}
		l.i++
	}
	return string(l.src[s:min(l.i, len(l.src))])
}
