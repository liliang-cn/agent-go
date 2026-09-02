package exec

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The fake plugin is this test binary, re-executed with modeEnv set — the
// helper-process pattern. It keeps the tests hermetic: no Python, no compile
// step, no testdata binary to keep in sync with the protocol it speaks.
const modeEnv = "AGENTGO_EXEC_TEST_PLUGIN"

func TestMain(m *testing.M) {
	if mode := os.Getenv(modeEnv); mode != "" {
		runFakePlugin(mode)
		return
	}
	os.Exit(m.Run())
}

// pluginCommand is the command that runs this test binary as a plugin in the
// given mode.
func pluginCommand() []string { return []string{os.Args[0]} }

func pluginMode(mode string) Option { return WithEnv(modeEnv + "=" + mode) }

// capabilitiesFor is what each mode declares in its handshake.
var capabilitiesFor = map[string][]string{
	"full":        {CapContext, CapBeforeTool, CapAfterTool, CapLint, CapRunStart, CapRunEnd},
	"lint-only":   {CapLint},
	"hang":        {CapAfterTool},
	"crash":       {CapAfterTool},
	"block":       {CapBeforeTool},
	"veto":        {CapRunStart},
	"slow":        {CapAfterTool},
	"quiet":       {CapContext, CapLint, CapRunStart, CapRunEnd},
	"stubborn":    {},
	"bad-version": {},
	"unknown-cap": {"telepathy"},
}

func runFakePlugin(mode string) {
	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	send := func(rep reply) {
		line, err := json.Marshal(rep)
		if err != nil {
			return
		}
		_, _ = out.Write(append(line, '\n'))
		_ = out.Flush()
	}

	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			if mode == "stubborn" {
				// Ignores EOF on stdin as thoroughly as it ignores the
				// shutdown message. Only a kill ends this process.
				time.Sleep(time.Hour)
			}
			return
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			fmt.Fprintln(os.Stderr, "undecodable request:", err)
			return
		}
		rep := reply{ID: req.ID}

		switch req.Type {
		case typeHello:
			rep.Type = typeHello
			rep.Protocol = ProtocolVersion
			if mode == "bad-version" {
				rep.Protocol = 99
			}
			rep.Capabilities = capabilitiesFor[mode]
		case typeShutdown:
			if mode == "stubborn" {
				continue
			}
			return
		case typeContext:
			if !declares(mode, CapContext) {
				rep.Error = "unexpected request type " + req.Type
				break
			}
			rep.Messages = []replyMessage{{Role: "system", Content: "house rule: " + req.Context.Goal}}
		case typeBeforeTool:
			if !declares(mode, CapBeforeTool) {
				rep.Error = "unexpected request type " + req.Type
				break
			}
			text, _ := req.Call.Args["text"].(string)
			if strings.Contains(text, "forbidden") {
				rep.Block = "that word may not be sent anywhere"
				break
			}
			if strings.Contains(text, "raw") {
				rep.Args = map[string]interface{}{"text": strings.ReplaceAll(text, "raw", "cooked")}
			}
		case typeAfterTool:
			if !declares(mode, CapAfterTool) {
				rep.Error = "unexpected request type " + req.Type
				break
			}
			switch mode {
			case "hang":
				time.Sleep(time.Hour)
			case "crash":
				os.Exit(3)
			case "slow":
				// Long enough that concurrent requests overlap, and it
				// answers with its own pid so a test can see which process
				// served which request.
				time.Sleep(120 * time.Millisecond)
				rep.Result = json.RawMessage(fmt.Sprintf(`{"pid":%d}`, os.Getpid()))
				rep.Replaced = true
				send(rep)
				continue
			}
			raw, err := json.Marshal(req.Result.Result)
			if err != nil {
				rep.Error = "unreadable result"
				break
			}
			masked := strings.ReplaceAll(string(raw), "secret", "[redacted]")
			if masked != string(raw) {
				fmt.Fprintln(os.Stderr, "masked a value in", req.Result.Name)
			}
			rep.Result = json.RawMessage(masked)
			rep.Replaced = true
		case typeLint:
			if !declares(mode, CapLint) {
				rep.Error = "unexpected request type " + req.Type
				break
			}
			if strings.Contains(req.Lint.Text, "secret") {
				rep.OK = false
				rep.Reason = "the answer still names the secret; say it without that"
				break
			}
			rep.OK = true
		case typeRunStart:
			if !declares(mode, CapRunStart) {
				rep.Error = "unexpected request type " + req.Type
				break
			}
			if mode == "veto" {
				rep.Error = "the budget for this agent is spent"
			}
		case typeRunEnd:
			if !declares(mode, CapRunEnd) {
				rep.Error = "unexpected request type " + req.Type
				break
			}
			fmt.Fprintln(os.Stderr, "run ended with", req.Outcome.StopReason)
		default:
			rep.Error = "unknown request type " + req.Type
		}
		send(rep)
	}
}

func declares(mode, capability string) bool {
	for _, c := range capabilitiesFor[mode] {
		if c == capability {
			return true
		}
	}
	return false
}
