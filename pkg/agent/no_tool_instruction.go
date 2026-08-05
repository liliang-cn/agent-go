package agent

import "strings"

// noToolInstructionPhrases are explicit, unambiguous refusals of tool use.
//
// Every entry carries a negation plus the word "tool", so an ordinary request
// that merely mentions tools ("use a tool to compute 5*4") cannot match. The
// list is deliberately literal rather than heuristic: withholding tools from a
// task that needs them is a worse failure than the one this prevents.
var noToolInstructionPhrases = []string{
	// English
	"without using any tool",
	"without using tool",
	"without any tool",
	"without tools",
	"without calling any tool",
	"do not use any tool",
	"do not use tools",
	"don't use any tool",
	"don't use tools",
	"do not call any tool",
	"don't call any tool",
	"no tool use",
	"no tools allowed",
	// Chinese
	"不要使用任何工具",
	"不使用任何工具",
	"不用任何工具",
	"不许使用任何工具",
	"禁止使用工具",
	"禁止使用任何工具",
	"不要使用工具",
	"不要用工具",
	"不许用工具",
	"别用工具",
	"不要调用工具",
	"不要调用任何工具",
	"无需使用工具",
	"不借助任何工具",
	"不借助工具",
}

// looksLikeNoToolInstruction reports whether the request explicitly forbids
// tool use.
//
// agentbench `personal-planet` ("Without using any tools, name the largest
// planet") had the model answer correctly but only after four tool calls. The
// fix is not another sentence in the system prompt telling it to obey — the
// tools are attached to the request, and an attached tool gets used. Withhold
// them instead, and the instruction is satisfied by construction.
func looksLikeNoToolInstruction(goal string) bool {
	normalized := strings.ToLower(goal)
	// Curly apostrophes are common in pasted text and would defeat "don't".
	normalized = strings.ReplaceAll(normalized, "’", "'")
	for _, phrase := range noToolInstructionPhrases {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}
