package trace

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// What went wrong, in the trace itself.
//
// The lines used to say which tool ran and for how long. A run that came out
// wrong read, in its trace, exactly like one that came out right: "tool bash
// dur=0.21s" whether the script it ran printed a report or a traceback, and
// "run end" whether the run answered or gave up. To find out which, somebody
// had to open the transcript — which a log line is there to save them from.
//
// So the three things a person looks for first are on the line:
//
//   - a tool call that failed carries why, cut short: err="…" when the call
//     itself returned an error, fail="…" when it returned normally but its
//     own result says it failed (a command's exit status, an "ok": false);
//   - a run's last line carries how it ended — outcome=completed, blocked,
//     failed or cancelled — and, when it did not complete, the reason;
//   - the runtime's own interventions are Decide lines already.
//
// Everything written here goes through Redact first. A tail of stderr is the
// likeliest place in a whole run for a credential to turn up — a curl -v, a
// printed environment, a stack trace with a connection string in it.

// maxNote bounds the free text one line carries.
const maxNote = 240

// StepOutcome is Step for a step whose failure is not only a Go error: failed
// says the step's own result reported failure, and note is the short reason
// to put on the line. Call the returned func when the step is done.
func StepOutcome(ctx context.Context, kind, name string) func(err error, failed bool, note string) {
	sink := sinkOf(ctx)
	if !Enabled() && sink == nil {
		return func(error, bool, string) {}
	}
	start := time.Now()
	return func(err error, failed bool, note string) {
		dur := time.Since(start)
		r := Record{Time: start, Kind: kind, Name: name, DurMs: dur.Milliseconds()}
		if err != nil {
			r.Err = Note(err.Error())
		}
		if failed && strings.TrimSpace(note) != "" {
			r.Fail = Note(note)
		} else if failed && err == nil {
			r.Fail = "reported failure, with no reason given"
		}
		if sink != nil {
			sink.Step(ctx, r)
		}
		emit(ctx, r)
		if s := statsOf(ctx); s != nil {
			switch kind {
			case "memory":
				s.tally.Memory(dur)
			case "tool":
				s.tally.Tool(name, dur, err != nil || failed)
			}
		}
	}
}

// EndWith records the run's summary under the outcome it really had, with the
// reason it did not complete. End is EndWith(ctx, "end", "").
func EndWith(ctx context.Context, outcome, reason string) {
	_, sum, ok := Timeline(ctx)
	if !ok {
		return
	}
	// The line still begins "run end": that is what people and scripts search
	// a log for. How it ended is the first thing after the total.
	detail := ""
	if outcome != "" && outcome != "end" {
		detail = "outcome=" + outcome + " "
	}
	detail += fmt.Sprintf("first_model_call=%s first_answer_token=%s model=%s/%d memory=%s tool=%s/%d",
		ms(sum.FirstModelCallMs), firstToken(sum.FirstAnswerTokenMs), ms(sum.ModelMs), sum.ModelCalls,
		ms(sum.MemoryMs), ms(sum.ToolMs), sum.ToolCalls)
	if sum.ToolFailures > 0 {
		detail += fmt.Sprintf(" tool_failures=%d", sum.ToolFailures)
	}
	r := Record{Time: time.Now(), Kind: "run", Name: "end", DurMs: sum.TotalMs, Detail: detail}
	if reason = strings.TrimSpace(reason); reason != "" {
		r.Fail = Note(reason)
	}
	emit(ctx, r)
}

// Note makes free text fit for a trace line: one line, secrets masked, cut to
// a length that leaves the log readable.
func Note(s string) string {
	s = Redact(strings.Join(strings.Fields(s), " "))
	if r := []rune(s); len(r) > maxNote {
		return string(r[:maxNote]) + "…"
	}
	return s
}

// Tail is the end of a tool's output, which is where a failing program says
// what went wrong: its last few non-empty lines, joined, as a Note.
func Tail(s string, lines int) string {
	if lines <= 0 {
		lines = 3
	}
	var kept []string
	all := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i := len(all) - 1; i >= 0 && len(kept) < lines; i-- {
		if t := strings.TrimSpace(all[i]); t != "" {
			kept = append([]string{t}, kept...)
		}
	}
	joined := Redact(strings.Join(kept, " | "))
	// Cut from the front: the last words are the ones that say what failed.
	if r := []rune(joined); len(r) > maxNote {
		return "…" + string(r[len(r)-maxNote:])
	}
	return joined
}

// The shapes a credential takes. Each is recognised by its form, not by which
// service issued it: a named value (key=…, "token": "…", Authorization: …),
// a URL with a password in it, a key block, and a long unbroken run of key
// alphabet that is not a word, a path or a number.
var (
	namedSecret = regexp.MustCompile(`(?i)\b([\w.-]*(?:api[_-]?key|secret|token|passwd|password|credential|authorization|auth|cookie|session[_-]?id|private[_-]?key|access[_-]?key)[\w.-]*)(["']?\s*[:=]\s*["']?)((?:bearer|basic)\s+)?([^\s"',;&]+)`)
	bearer      = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
	urlUserinfo = regexp.MustCompile(`(\b[a-z][a-z0-9+.-]*://[^\s/:@]+):[^\s/@]+@`)
	keyBlock    = regexp.MustCompile(`-{5}BEGIN [A-Z ]+-{5}[\s\S]*?(?:-{5}END [A-Z ]+-{5}|$)`)
	prefixedKey = regexp.MustCompile(`\b(?:sk|pk|rk|ghp|gho|ghu|ghs|ghr|glpat|xox[abprs]|cfut|AKI[A]|ASIA|AIza|ya29)[-_]?[A-Za-z0-9_\-.]{12,}`)
	longToken   = regexp.MustCompile(`[A-Za-z0-9_\-+/=]{32,}`)
)

// Redact masks anything in s that has the shape of a credential.
func Redact(s string) string {
	if s == "" {
		return s
	}
	s = keyBlock.ReplaceAllString(s, "[redacted key]")
	s = urlUserinfo.ReplaceAllString(s, "$1:[redacted]@")
	s = namedSecret.ReplaceAllString(s, "$1$2[redacted]")
	s = bearer.ReplaceAllString(s, "$1 [redacted]")
	s = prefixedKey.ReplaceAllString(s, "[redacted]")
	return longToken.ReplaceAllStringFunc(s, func(tok string) string {
		// A long path or a dashed sentence is not a key; a key mixes classes
		// and has no separators a path would have.
		if strings.ContainsAny(tok, "/") && !strings.ContainsAny(tok, "+=") {
			return tok
		}
		var letters, digits int
		for _, c := range tok {
			switch {
			case c >= '0' && c <= '9':
				digits++
			case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
				letters++
			}
		}
		if digits < 4 || letters < 4 {
			return tok
		}
		return "[redacted]"
	})
}
