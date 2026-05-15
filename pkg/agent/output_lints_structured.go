package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// StructuredOutputLint validates that the agent's final text response is a
// JSON document matching a given JSON Schema. It is the Tier A backbone of
// structured outputs: every provider goes through it post-hoc, even when
// Tier B (response_format) is also negotiated. On rejection the lint
// returns a structured reason — field path + validation error — so the
// runtime can re-prompt the model with actionable feedback.
//
// Construct via NewStructuredOutputLint(spec). The lint compiles the schema
// lazily on the first call and caches the compiled validator.
type StructuredOutputLint struct {
	spec *StructuredOutputSpec

	once     sync.Once
	compiled *jsonschema.Schema
	compErr  error
}

// NewStructuredOutputLint wraps a StructuredOutputSpec into a runnable
// OutputLint. Returns nil when the spec is nil or carries no schema.
func NewStructuredOutputLint(spec *StructuredOutputSpec) *StructuredOutputLint {
	if spec == nil || len(spec.Schema) == 0 {
		return nil
	}
	return &StructuredOutputLint{spec: spec}
}

// Name returns the lint's stable identifier. Uses the spec name when
// supplied; falls back to a generic label otherwise.
func (l *StructuredOutputLint) Name() string {
	if l == nil || l.spec == nil {
		return "structured_output"
	}
	if name := strings.TrimSpace(l.spec.Name); name != "" {
		return "structured_output:" + name
	}
	return "structured_output"
}

// Check parses the text as JSON and validates against the schema. Returns
// (true, "") on a match; otherwise (false, reason) where reason carries
// the JSON pointer to the failing field and a short message — both shown
// to the model on retry.
func (l *StructuredOutputLint) Check(text string, _ LintContext) (bool, string) {
	if l == nil || l.spec == nil {
		return true, ""
	}
	if err := l.ensureCompiled(); err != nil {
		// A malformed schema is a developer bug, not a model failure —
		// pass it through rather than punishing the model with endless
		// retries it can't satisfy.
		return true, ""
	}

	payload := extractJSONPayload(text)
	if payload == "" {
		return false, fmt.Sprintf(
			"final answer must be a single JSON document matching schema %q — got non-JSON content. Reply with ONLY the JSON object, no prose, no code fences.",
			l.spec.Name,
		)
	}

	var doc interface{}
	dec := json.NewDecoder(bytes.NewReader([]byte(payload)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return false, fmt.Sprintf(
			"final answer is not valid JSON: %s. Reply with ONLY the JSON object, no prose, no code fences.",
			err.Error(),
		)
	}

	if err := l.compiled.Validate(doc); err != nil {
		return false, fmt.Sprintf(
			"final answer does not match schema %q: %s. Fix the highlighted fields and reply with ONLY the corrected JSON.",
			l.spec.Name,
			summarizeSchemaError(err),
		)
	}
	return true, ""
}

func (l *StructuredOutputLint) ensureCompiled() error {
	l.once.Do(func() {
		compiler := jsonschema.NewCompiler()
		// Use a stable, in-memory URL so error messages don't leak file
		// paths and the validator is fully self-contained.
		ref := "mem:///structured_output_schema.json"
		if err := compiler.AddResource(ref, bytes.NewReader(l.spec.Schema)); err != nil {
			l.compErr = fmt.Errorf("add schema resource: %w", err)
			return
		}
		schema, err := compiler.Compile(ref)
		if err != nil {
			l.compErr = fmt.Errorf("compile schema: %w", err)
			return
		}
		l.compiled = schema
	})
	return l.compErr
}

// extractJSONPayload pulls the JSON document out of a model's free-form
// text. Accepts: a bare JSON object/array, a JSON value wrapped in
// ```json fences, or a JSON value wrapped in ``` fences. Trims any
// leading or trailing prose so a model that "explains then answers" can
// still pass the lint when the JSON is recognizable.
func extractJSONPayload(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// Fenced ```json / ``` blocks: prefer the first match.
	if m := fencedJSONRe.FindStringSubmatch(text); len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}

	// Bare JSON: try to find the outermost { ... } or [ ... ] span.
	if start, end := outermostJSONSpan(text); start >= 0 {
		return strings.TrimSpace(text[start : end+1])
	}
	return ""
}

var fencedJSONRe = regexp.MustCompile("(?s)```(?:json)?\\s*\\n?(.*?)```")

// outermostJSONSpan finds the first {/[ in text and returns the index of
// its matching closing brace, scanning naïvely (no full parser — that's
// what the actual JSON decoder is for). Returns (-1, -1) on no match.
func outermostJSONSpan(text string) (int, int) {
	start := -1
	var open, close byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '{' || c == '[' {
			start = i
			open = c
			if c == '{' {
				close = '}'
			} else {
				close = ']'
			}
			break
		}
	}
	if start < 0 {
		return -1, -1
	}

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return start, i
			}
		}
	}
	return -1, -1
}

// summarizeSchemaError turns jsonschema's verbose error tree into a
// single-line message highlighting the most actionable failure. Returns
// the raw error string when summarization fails so the model still gets
// some signal.
func summarizeSchemaError(err error) string {
	if err == nil {
		return ""
	}
	var ve *jsonschema.ValidationError
	if !errorsAs(err, &ve) {
		return err.Error()
	}
	// jsonschema nests violations as a tree; surface the deepest leaf
	// since that's the most specific field-level message.
	leaf := deepestSchemaLeaf(ve)
	loc := strings.TrimPrefix(leaf.InstanceLocation, "/")
	if loc == "" {
		loc = "(root)"
	}
	msg := strings.TrimSpace(leaf.Message)
	if msg == "" {
		msg = "value does not match schema"
	}
	return fmt.Sprintf("at %s: %s", loc, msg)
}

func deepestSchemaLeaf(ve *jsonschema.ValidationError) *jsonschema.ValidationError {
	cur := ve
	for len(cur.Causes) > 0 {
		cur = cur.Causes[0]
	}
	return cur
}

// errorsAs avoids importing errors at the top level just for this one
// helper — keeps imports tight. Equivalent to errors.As.
func errorsAs(err error, target **jsonschema.ValidationError) bool {
	for err != nil {
		if v, ok := err.(*jsonschema.ValidationError); ok {
			*target = v
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
