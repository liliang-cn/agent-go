package agent

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// docs/trace-schema.md is the trace's schema for a reader who is not reading
// this code. A field added to traceLine, or an event added to TraceWriter,
// without a word there fails here.
func TestTraceSchemaDocCoversEveryFieldAndEvent(t *testing.T) {
	raw, err := os.ReadFile("../../docs/trace-schema.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	typ := reflect.TypeOf(traceLine{})
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		if !strings.Contains(doc, "`"+tag+"`") && !strings.Contains(doc, " "+tag+",") && !strings.Contains(doc, "{"+tag+",") {
			t.Errorf("field %q is not in docs/trace-schema.md", tag)
		}
	}
	src, err := os.ReadFile("trace_writer.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`Event(?:\s*=|:)\s*"([a-z_]+)"`).FindAllStringSubmatch(string(src), -1) {
		if !strings.Contains(doc, "`"+m[1]+"`") {
			t.Errorf("event %q is not in docs/trace-schema.md", m[1])
		}
	}
}
