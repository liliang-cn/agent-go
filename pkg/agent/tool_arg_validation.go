package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Tool-call argument validation.
//
// Every tool declares a parameters schema, and until this nothing checked a
// call against it. A model that left out a required argument had its call
// executed anyway, and what happened next was up to the handler: a strict one
// refused (one wasted execution, and an error written in whatever words the
// handler chose), a lenient one — written against the schema, trusting it —
// did the wrong thing and reported success, and the model had no reason to
// look again.
//
// So a model-issued call is checked against its tool's declared schema before
// it runs. A call that fails is not executed; the model gets the failing field
// paths and the expected types back as the tool's error, which is the one
// thing that lets it repair the call rather than repeat it.
//
// The validator must never be the reason a valid call fails. Anything it
// cannot judge passes: a schema that does not compile, a remote $ref (loading
// is refused, so compilation fails, so the call passes), a panic inside the
// library. `format` is an annotation, never an assertion, here. OpenAPI's
// `nullable` is honoured, and a null sent for an optional top-level argument
// is treated as absent — both are things handlers in the wild accept.
//
// Only the dispatch path for model calls validates (executeDirectToolCall).
// A host or PTC calling a handler directly is not a model guessing at a
// schema and is not checked.

// ToolArgumentError is the error a call that failed its schema returns. The
// runtime writes Error() into the tool message, so it is addressed to the
// model.
type ToolArgumentError struct {
	Tool   string
	Issues []ToolArgumentIssue
}

// ToolArgumentIssue is one failing field.
type ToolArgumentIssue struct {
	// Path is the field, dotted from the argument root ("" = the arguments
	// object itself).
	Path string
	// Expected is the declared type, when the schema names one.
	Expected string
	// Message says what is wrong.
	Message string
}

func (e *ToolArgumentError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "invalid arguments for %s — the call was NOT run: ", e.Tool)
	for i, issue := range e.Issues {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(issue.String())
	}
	fmt.Fprintf(&b, ". Fix these arguments and call %s again.", e.Tool)
	return b.String()
}

func (i ToolArgumentIssue) String() string {
	var s string
	if i.Path == "" {
		s = i.Message
	} else {
		s = fmt.Sprintf("field %q: %s", i.Path, i.Message)
	}
	if i.Expected != "" && !strings.Contains(i.Message, "expected") {
		s += fmt.Sprintf(" (expected %s)", i.Expected)
	}
	return s
}

// maxArgIssues bounds how many fields one error names. A model fixes the first
// few and the next attempt reports the rest; a wall of forty is noise.
const maxArgIssues = 6

type compiledArgSchema struct {
	schema *jsonschema.Schema // nil: could not compile, every call passes
	raw    map[string]interface{}
}

// validateToolCallArgs checks a model's arguments against the tool's declared
// schema. It returns a *ToolArgumentError when the call must not run, and nil
// both when it is valid and when the validator cannot judge it.
func (s *Service) validateToolCallArgs(name string, currentAgent *Agent, args map[string]interface{}) (verr error) {
	if s == nil || s.skipToolArgValidation || name == "" {
		return nil
	}
	if isTaskTerminalToolName(name) || isSearchToolName(name) || strings.HasPrefix(name, "transfer_to_") {
		// Control signals and the catalogue search carry their own checks,
		// and a terminal tool's result is read as the final answer.
		return nil
	}
	params, ok := s.declaredToolParameters(name, currentAgent)
	if !ok || len(params) == 0 {
		return nil
	}
	compiled := s.compileArgSchema(name, params)
	if compiled == nil || compiled.schema == nil {
		return nil
	}

	defer func() {
		// The library's own recover covers two panic types; anything else in
		// a validator is a validator bug, and it must not become a failed call.
		if r := recover(); r != nil {
			verr = nil
		}
	}()

	instance, ok := argsAsJSONInstance(args, compiled.raw)
	if !ok {
		return nil
	}
	err := compiled.schema.Validate(instance)
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		// InfiniteLoopError / InvalidJSONTypeError: not a verdict on the call.
		return nil
	}
	issues := argIssuesFrom(ve, compiled.raw, instance)
	if len(issues) == 0 {
		return nil
	}
	return &ToolArgumentError{Tool: name, Issues: issues}
}

// declaredToolParameters finds the schema the model was shown for a tool, in
// the same order dispatch resolves the handler. Skills are not looked up:
// their variables are free-form by design.
func (s *Service) declaredToolParameters(name string, currentAgent *Agent) (map[string]interface{}, bool) {
	if currentAgent != nil {
		for _, def := range currentAgent.Tools() {
			if def.Function.Name == name {
				return def.Function.Parameters, true
			}
		}
	}
	if s.toolRegistry != nil {
		if def, ok := s.toolRegistry.DefinitionOf(name); ok {
			return def.Function.Parameters, true
		}
	}
	if s.mcpService != nil && s.isMCPTool(name) {
		for _, def := range s.mcpService.ListTools() {
			if def.Function.Name == name {
				return def.Function.Parameters, true
			}
		}
	}
	return nil, false
}

// compileArgSchema compiles once per distinct schema. The key includes a hash
// of the schema so a tool re-registered with a new one is recompiled.
func (s *Service) compileArgSchema(name string, params map[string]interface{}) *compiledArgSchema {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(raw)
	key := name + "\x00" + hex.EncodeToString(sum[:8])
	if cached, ok := s.argSchemas.Load(key); ok {
		return cached.(*compiledArgSchema)
	}

	out := &compiledArgSchema{}
	var normalized map[string]interface{}
	if err := json.Unmarshal(raw, &normalized); err == nil {
		normalizeArgSchema(normalized, true)
		out.raw = normalized
		if doc, err := json.Marshal(normalized); err == nil {
			out.schema = compileArgSchemaDoc(doc)
		}
	}
	actual, _ := s.argSchemas.LoadOrStore(key, out)
	return actual.(*compiledArgSchema)
}

func compileArgSchemaDoc(doc []byte) (schema *jsonschema.Schema) {
	defer func() {
		if recover() != nil {
			schema = nil
		}
	}()
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	compiler.AssertFormat = false
	compiler.AssertContent = false
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("remote schema references are not loaded")
	}
	const ref = "mem:///tool_parameters.json"
	if err := compiler.AddResource(ref, bytes.NewReader(doc)); err != nil {
		return nil
	}
	compiled, err := compiler.Compile(ref)
	if err != nil {
		return nil
	}
	return compiled
}

// schemaMapKeywords hold a map of name -> subschema; schemaKeywords hold one
// subschema; schemaListKeywords hold a list of them. Walking by keyword is
// what keeps a property that happens to be called "format" or "nullable" from
// being mistaken for the keyword.
var (
	schemaMapKeywords  = []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"}
	schemaKeywords     = []string{"items", "additionalProperties", "not", "if", "then", "else", "contains", "propertyNames", "unevaluatedProperties", "unevaluatedItems", "additionalItems"}
	schemaListKeywords = []string{"allOf", "anyOf", "oneOf", "prefixItems", "items"}
)

// normalizeArgSchema rewrites, in place, the things a tool schema commonly
// says that a strict 2020-12 validator would read differently from the tool:
//
//   - $schema at the root is dropped, so a draft-07 declaration does not turn
//     `format` into an assertion;
//   - `format` is dropped everywhere — an annotation, never a reason to refuse;
//   - OpenAPI `nullable: true` becomes a type that admits null.
func normalizeArgSchema(node map[string]interface{}, root bool) {
	if node == nil {
		return
	}
	if root {
		delete(node, "$schema")
	}
	delete(node, "format")
	if nullable, _ := node["nullable"].(bool); nullable {
		switch t := node["type"].(type) {
		case string:
			node["type"] = []interface{}{t, "null"}
		case []interface{}:
			node["type"] = append(t, "null")
		}
	}
	delete(node, "nullable")
	for _, kw := range schemaMapKeywords {
		if m, ok := node[kw].(map[string]interface{}); ok {
			for _, sub := range m {
				if sm, ok := sub.(map[string]interface{}); ok {
					normalizeArgSchema(sm, false)
				}
			}
		}
	}
	for _, kw := range schemaKeywords {
		if sm, ok := node[kw].(map[string]interface{}); ok {
			normalizeArgSchema(sm, false)
		}
	}
	for _, kw := range schemaListKeywords {
		if list, ok := node[kw].([]interface{}); ok {
			for _, sub := range list {
				if sm, ok := sub.(map[string]interface{}); ok {
					normalizeArgSchema(sm, false)
				}
			}
		}
	}
}

// argsAsJSONInstance turns the call's arguments into what the validator
// reads: a JSON round-trip (a hook may have put Go values in the map), a nil
// map as an empty object, and null top-level values the schema does not
// require removed, since "not given" is what a model sending null means.
func argsAsJSONInstance(args map[string]interface{}, schema map[string]interface{}) (interface{}, bool) {
	if args == nil {
		args = map[string]interface{}{}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var instance interface{}
	if err := dec.Decode(&instance); err != nil {
		return nil, false
	}
	obj, ok := instance.(map[string]interface{})
	if !ok {
		return instance, true
	}
	required := stringSet(schema["required"])
	for k, v := range obj {
		if v == nil && !required[k] {
			delete(obj, k)
		}
	}
	return obj, true
}

func stringSet(v interface{}) map[string]bool {
	out := map[string]bool{}
	if list, ok := v.([]interface{}); ok {
		for _, item := range list {
			if s, ok := item.(string); ok {
				out[s] = true
			}
		}
	}
	return out
}

// argIssuesFrom flattens the library's error tree into field-level issues:
// every leaf, deduplicated, in a stable order.
func argIssuesFrom(ve *jsonschema.ValidationError, schema map[string]interface{}, instance interface{}) []ToolArgumentIssue {
	var leaves []*jsonschema.ValidationError
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			leaves = append(leaves, e)
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)

	seen := map[string]bool{}
	var issues []ToolArgumentIssue
	add := func(issue ToolArgumentIssue) {
		key := issue.Path + "\x00" + issue.Message
		if seen[key] {
			return
		}
		seen[key] = true
		issues = append(issues, issue)
	}
	for _, leaf := range leaves {
		instPath := pointerTokens(leaf.InstanceLocation)
		kwPath := pointerTokens(leaf.KeywordLocation)
		if len(kwPath) > 0 && kwPath[len(kwPath)-1] == "required" {
			if sub, ok := schemaAt(schema, kwPath[:len(kwPath)-1]); ok {
				obj, _ := instanceAt(instance, instPath).(map[string]interface{})
				missing := false
				for _, name := range sortedRequired(sub["required"]) {
					if _, present := obj[name]; present {
						continue
					}
					missing = true
					add(ToolArgumentIssue{
						Path:     dottedPath(append(append([]string(nil), instPath...), name)),
						Expected: declaredType(sub, name),
						Message:  "required field is missing",
					})
				}
				if missing {
					continue
				}
			}
		}
		msg := strings.TrimSpace(leaf.Message)
		if msg == "" {
			msg = "value does not match the schema"
		}
		add(ToolArgumentIssue{Path: dottedPath(instPath), Message: msg})
	}
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Path < issues[j].Path })
	if len(issues) > maxArgIssues {
		issues = issues[:maxArgIssues]
	}
	return issues
}

func pointerTokens(ptr string) []string {
	ptr = strings.TrimPrefix(ptr, "/")
	if ptr == "" {
		return nil
	}
	parts := strings.Split(ptr, "/")
	for i, p := range parts {
		p = strings.ReplaceAll(p, "~1", "/")
		parts[i] = strings.ReplaceAll(p, "~0", "~")
	}
	return parts
}

func dottedPath(tokens []string) string {
	return strings.Join(tokens, ".")
}

// schemaAt follows a keyword location through the raw schema. A location that
// passes through a $ref is not followed, and the caller falls back to the
// library's own message.
func schemaAt(schema map[string]interface{}, tokens []string) (map[string]interface{}, bool) {
	var cur interface{} = schema
	for _, tok := range tokens {
		switch node := cur.(type) {
		case map[string]interface{}:
			next, ok := node[tok]
			if !ok {
				return nil, false
			}
			cur = next
		case []interface{}:
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
	}
	m, ok := cur.(map[string]interface{})
	return m, ok
}

func instanceAt(instance interface{}, tokens []string) interface{} {
	cur := instance
	for _, tok := range tokens {
		switch node := cur.(type) {
		case map[string]interface{}:
			cur = node[tok]
		case []interface{}:
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil
			}
			cur = node[idx]
		default:
			return nil
		}
	}
	return cur
}

func sortedRequired(v interface{}) []string {
	set := stringSet(v)
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func declaredType(schema map[string]interface{}, prop string) string {
	props, _ := schema["properties"].(map[string]interface{})
	p, _ := props[prop].(map[string]interface{})
	switch t := p["type"].(type) {
	case string:
		return t
	case []interface{}:
		parts := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " or ")
	}
	return ""
}

// isToolArgumentError reports whether err is a refused call.
func isToolArgumentError(err error) bool {
	var ae *ToolArgumentError
	return errors.As(err, &ae)
}
