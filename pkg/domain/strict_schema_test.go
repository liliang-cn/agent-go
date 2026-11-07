package domain

import (
	"encoding/json"
	"reflect"
	"testing"
)

func strictOf(t *testing.T, in string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(in), &m); err != nil {
		t.Fatal(err)
	}
	out, ok := StrictJSONSchema(m).(map[string]interface{})
	if !ok {
		t.Fatal("not a map")
	}
	return out
}

func TestStrictJSONSchemaNestedAndOptional(t *testing.T) {
	out := strictOf(t, `{"type":"object","properties":{
		"name":{"type":"string"},
		"inner":{"type":"object","properties":{"a":{"type":"integer"}},"required":["a"]},
		"list":{"type":"array","items":{"type":"object","properties":{
			"kind":{"type":"string","enum":["x","y"]},"path":{"type":"string"}},"required":["kind"]}}
	},"required":["inner"]}`)

	if out["additionalProperties"] != false {
		t.Fatal("root not closed")
	}
	if !reflect.DeepEqual(out["required"], []interface{}{"inner", "list", "name"}) {
		t.Fatalf("root required = %v", out["required"])
	}
	props := out["properties"].(map[string]interface{})
	if !reflect.DeepEqual(props["name"].(map[string]interface{})["type"], []interface{}{"string", "null"}) {
		t.Fatalf("optional name not nullable: %v", props["name"])
	}
	inner := props["inner"].(map[string]interface{})
	if inner["additionalProperties"] != false || inner["type"] != "object" {
		t.Fatalf("inner = %v", inner)
	}
	item := props["list"].(map[string]interface{})["items"].(map[string]interface{})
	if item["additionalProperties"] != false {
		t.Fatal("array item object not closed")
	}
	if !reflect.DeepEqual(item["required"], []interface{}{"kind", "path"}) {
		t.Fatalf("item required = %v", item["required"])
	}
	ip := item["properties"].(map[string]interface{})
	if !reflect.DeepEqual(ip["kind"].(map[string]interface{})["type"], "string") {
		t.Fatal("required kind must stay non-null")
	}
	if !reflect.DeepEqual(ip["path"].(map[string]interface{})["type"], []interface{}{"string", "null"}) {
		t.Fatal("optional path not nullable")
	}
}

func TestStrictJSONSchemaEnumGainsNull(t *testing.T) {
	out := strictOf(t, `{"type":"object","properties":{"k":{"type":"string","enum":["a"]}}}`)
	k := out["properties"].(map[string]interface{})["k"].(map[string]interface{})
	if !reflect.DeepEqual(k["enum"], []interface{}{"a", nil}) {
		t.Fatalf("enum = %v", k["enum"])
	}
}

func TestStrictJSONSchemaCombinatorsAndDefs(t *testing.T) {
	out := strictOf(t, `{"type":"object","properties":{
		"u":{"anyOf":[{"type":"object","properties":{"a":{"type":"string"}}},{"type":"string"}]}
	},"$defs":{"d":{"type":"object","properties":{"z":{"type":"number"}}}}}`)
	u := out["properties"].(map[string]interface{})["u"].(map[string]interface{})
	branches := u["anyOf"].([]interface{})
	if branches[0].(map[string]interface{})["additionalProperties"] != false {
		t.Fatal("anyOf branch not closed")
	}
	if len(branches) != 3 {
		t.Fatalf("optional anyOf should gain a null branch: %v", branches)
	}
	d := out["$defs"].(map[string]interface{})["d"].(map[string]interface{})
	if d["additionalProperties"] != false || !reflect.DeepEqual(d["required"], []interface{}{"z"}) {
		t.Fatalf("$defs entry = %v", d)
	}
}

func TestStrictJSONSchemaLeavesInputUntouched(t *testing.T) {
	in := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"a": map[string]interface{}{"type": "string"},
		},
	}
	before, _ := json.Marshal(in)
	_ = StrictJSONSchema(in)
	after, _ := json.Marshal(in)
	if string(before) != string(after) {
		t.Fatalf("input modified:\n%s\n%s", before, after)
	}
}

func TestStrictJSONSchemaBytesAndNullDecode(t *testing.T) {
	out := StrictJSONSchemaBytes([]byte(`{"type":"object","properties":{"p":{"type":"string"}}}`))
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil || m["additionalProperties"] != false {
		t.Fatalf("bytes form: %s %v", out, err)
	}
	// A null for a field the Go struct holds as string decodes to "".
	var v struct {
		P string   `json:"p"`
		L []string `json:"l"`
	}
	if err := json.Unmarshal([]byte(`{"p":null,"l":null}`), &v); err != nil || v.P != "" || v.L != nil {
		t.Fatalf("null decode: %+v %v", v, err)
	}
}
