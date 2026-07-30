package agent

import "testing"

// PTC's terminal short-circuit lets a script's return value stand as the final
// reply, skipping the summarising round. isMeaningfulAnswerText is the only gate
// on that, so what it accepts is what the user ends up reading.
func TestIsMeaningfulAnswerTextRejectsPayloads(t *testing.T) {
	notAnswers := []string{
		"",
		"   ",
		"done",
		"Done.",
		"task complete",
		"The task has been completed.",
		// Serialized values: the case that shipped raw JSON to the user.
		`{"content":"tomorrow=2026-07-31","path":"dates.md"}`,
		`{"verify":{"path":"dates.md"},"written":"x"}`,
		`[1,2,3]`,
		`[{"a":1}]`,
		"```json\n{\"ok\":true}\n```",
		"```\n{\"ok\":true}\n```",
	}
	for _, text := range notAnswers {
		if isMeaningfulAnswerText(text) {
			t.Errorf("must not stand as the final answer: %q", text)
		}
	}

	answers := []string{
		"已写入工作区文件 dates.md：明天 2026-07-31，下周一 2026-08-03。\n情绪: 开心",
		"明天是 2026-07-31。",
		"I wrote both dates to dates.md.",
		// Prose that happens to contain a payload is still prose.
		"结果如下：\n```json\n{\"ok\":true}\n```\n已经存好了。",
		// Malformed JSON-ish text is prose, not a payload.
		`{not really json`,
		"Done — and here is why it took two tries.",
	}
	for _, text := range answers {
		if !isMeaningfulAnswerText(text) {
			t.Errorf("should be usable as the final answer: %q", text)
		}
	}
}

func TestLooksLikeMachinePayload(t *testing.T) {
	if !looksLikeMachinePayload(`{"a":1}`) {
		t.Error("a JSON object is a payload")
	}
	if !looksLikeMachinePayload("```json\n[1,2]\n```") {
		t.Error("a fenced JSON array is a payload")
	}
	if looksLikeMachinePayload("{") {
		t.Error("a stray brace is not a payload")
	}
	if looksLikeMachinePayload(`{"a": }`) {
		t.Error("invalid JSON is not a payload — it is probably prose")
	}
	if looksLikeMachinePayload("正常的一句话。") {
		t.Error("prose is not a payload")
	}
}
