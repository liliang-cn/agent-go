package squad

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/agent-go/v2/pkg/config"
	agentgolog "github.com/liliang-cn/agent-go/v2/pkg/log"
	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var SquadCmd = &cobra.Command{
	Use:   "squad",
	Short: "Run squad tasks and manage squads or squad agents",
	Long: `Run squad tasks, inspect squads, and manage squad agents.

With no subcommand, 'agentgo squad' starts interactive squad chat.

Interactive controls:
  Ctrl+C    cancel the current input line
  Ctrl+D    exit when the current input is empty
  quit      exit interactive mode
  exit      exit interactive mode`,
	Example: `  agentgo squad
  agentgo squad go "@Captain summarize the repo"
  agentgo squad agent add Writer --squad "Docs Squad" --description "Writes concise docs"`,
	Args: cobra.NoArgs,
	RunE: runInteractiveSquad,
}

const builtInSquadID = "squad-default-001"

type delegatedTask struct {
	AgentName   string
	Instruction string
}

var agentMentionPattern = regexp.MustCompile(`^@([^\s@]+)$`)
var errInputCanceled = errors.New("input canceled")

func init() {
	SquadCmd.AddCommand(goCmd)
	SquadCmd.AddCommand(addCmd)
	SquadCmd.AddCommand(deleteCmd)
	SquadCmd.AddCommand(listCmd)
	SquadCmd.AddCommand(statusCmd)
	SquadCmd.AddCommand(squadA2ACmd)
	SquadCmd.AddCommand(memberCmd)
	memberCmd.AddCommand(memberAddCmd)
	memberCmd.AddCommand(memberListCmd)
	memberCmd.AddCommand(memberShowCmd)

	addCmd.Flags().StringVar(&squadDescription, "description", "", "squad description")

	memberAddCmd.Flags().StringVar(&memberDescription, "description", "", "agent description")
	memberAddCmd.Flags().StringVar(&memberInstructions, "instructions", "", "agent system instructions")
	memberAddCmd.Flags().StringVar(&memberKind, "kind", "specialist", "agent role inside the squad: specialist or captain")
	memberAddCmd.Flags().StringVar(&memberSquadID, "squad-id", "", "target squad ID (defaults to the default squad)")
	memberAddCmd.Flags().StringVar(&memberSquadName, "squad", "", "target squad name (defaults to the default squad)")
	memberAddCmd.Flags().StringVar(&memberModel, "model", "", "preferred provider or model")
}

var (
	squadDescription   string
	memberDescription  string
	memberInstructions string
	memberKind         string
	memberSquadID      string
	memberSquadName    string
	memberModel        string
)

var goCmd = &cobra.Command{
	Use:   "go [task]",
	Short: "Run a squad task",
	Long:  `Run one squad task explicitly, for example: agentgo squad go "@Captain summarize and implement".`,
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		return runSquadMessage(context.Background(), manager, "", strings.Join(args, " "))
	},
}

var addCmd = &cobra.Command{
	Use:     "add [name]",
	Aliases: []string{"create"},
	Short:   "Add a squad",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}

		name := strings.TrimSpace(args[0])
		if name == "" {
			return fmt.Errorf("squad name is required")
		}

		description := strings.TrimSpace(squadDescription)
		if description == "" {
			description = name
		}

		squad, err := manager.CreateSquad(context.Background(), &agent.Squad{
			Name:        name,
			Description: description,
		})
		if err != nil {
			return err
		}

		if lead, leadErr := manager.GetLeadAgentForSquad(squad.ID); leadErr == nil && strings.TrimSpace(lead.Name) != "" {
			fmt.Printf("Added squad '%s' with default captain '%s'.\n", squad.Name, lead.Name)
		} else {
			fmt.Printf("Added squad '%s'.\n", squad.Name)
		}
		return nil
	},
}

var deleteCmd = &cobra.Command{
	Use:     "delete [name]",
	Aliases: []string{"remove", "rm"},
	Short:   "Delete a squad",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}

		squad, err := resolveSquad(manager, args[0])
		if err != nil {
			return err
		}
		if squad.ID == builtInSquadID {
			return fmt.Errorf("cannot delete the built-in AgentGo Squad")
		}
		if err := manager.DeleteSquad(context.Background(), squad.ID); err != nil {
			return err
		}
		fmt.Printf("Deleted squad '%s'.\n", squad.Name)
		return nil
	},
}

func getManager() (*agent.SquadManager, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, err
	}
	agentDBPath := cfg.AgentDBPath()
	store, err := agent.NewStore(agentDBPath)
	if err != nil {
		return nil, err
	}
	manager := agent.NewSquadManager(store)
	manager.SetConfig(cfg)
	_ = manager.SeedDefaultMembers()
	return manager, nil
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List squads",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		statuses, err := manager.ListSquadStatuses()
		if err != nil {
			return err
		}

		type squadRow struct {
			Name        string
			LeadAgent   string
			Agents      int
			Status      string
			BuiltIn     bool
			A2A         bool
			Description string
		}

		rows := make([]squadRow, 0, len(statuses))
		for _, squad := range statuses {
			row := squadRow{
				Name:        squad.Name,
				Description: squad.Description,
				BuiltIn:     squad.SquadID == builtInSquadID,
				Status:      squad.Status,
				Agents:      squad.AgentCount,
				A2A:         squad.EnableA2A,
			}
			if len(squad.CaptainNames) > 0 {
				row.LeadAgent = squad.CaptainNames[0]
			}
			rows = append(rows, row)
		}

		slices.SortFunc(rows, func(a, b squadRow) int {
			switch {
			case a.Name < b.Name:
				return -1
			case a.Name > b.Name:
				return 1
			default:
				return 0
			}
		})

		fmt.Println("Squads")
		if len(rows) == 0 {
			fmt.Println("  (none)")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tCAPTAIN\tAGENTS\tSTATUS\tBUILT-IN\tA2A\tDESCRIPTION")
		for _, row := range rows {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n", row.Name, valueOrDash(row.LeadAgent), row.Agents, row.Status, yesNo(row.BuiltIn), yesNo(row.A2A), row.Description)
		}
		w.Flush()
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status [name]",
	Short: "Show runtime status for one squad",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		squad, err := resolveSquad(manager, args[0])
		if err != nil {
			return err
		}
		return followSquadStatus(cmd.Context(), manager, squad.ID)
	},
}

var memberCmd = &cobra.Command{
	Use:     "agent",
	Aliases: []string{"member"},
	Short:   "Manage squad agents",
}

var memberAddCmd = &cobra.Command{
	Use:     "add [name]",
	Aliases: []string{"create"},
	Short:   "Add an agent directly into a squad",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}

		name := strings.TrimSpace(args[0])
		if name == "" {
			return fmt.Errorf("agent name is required")
		}

		kind, err := normalizeSquadRole(strings.TrimSpace(memberKind))
		if err != nil {
			return err
		}

		description := strings.TrimSpace(memberDescription)
		if description == "" {
			description = name
		}
		instructions := strings.TrimSpace(memberInstructions)
		if instructions == "" {
			instructions = description
		}
		squadID, err := resolveMemberSquadID(manager, strings.TrimSpace(memberSquadID), strings.TrimSpace(memberSquadName))
		if err != nil {
			return err
		}

		member, err := manager.CreateMember(context.Background(), &agent.AgentModel{
			Name:         name,
			TeamID:       squadID,
			Kind:         kind,
			Description:  description,
			Instructions: instructions,
			Model:        strings.TrimSpace(memberModel),
		})
		if err != nil {
			return err
		}

		fmt.Printf("Added %s '%s'.\n", kindDisplay(member.Kind), member.Name)
		return nil
	},
}

var memberListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all squad agents",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		agentsList, err := manager.ListMembers()
		if err != nil {
			return err
		}

		var leadAgents []*agent.AgentModel
		var specialists []*agent.AgentModel
		for _, a := range agentsList {
			switch a.Kind {
			case agent.AgentKindSpecialist:
				specialists = append(specialists, a)
			default:
				leadAgents = append(leadAgents, a)
			}
		}

		slices.SortFunc(leadAgents, func(a, b *agent.AgentModel) int {
			return compareAgentNames(a.Name, b.Name)
		})
		slices.SortFunc(specialists, func(a, b *agent.AgentModel) int {
			return compareAgentNames(a.Name, b.Name)
		})

		printAgentSection("Captains", leadAgents)
		fmt.Println()
		printAgentSection("Specialists", specialists)
		return nil
	},
}

var memberShowCmd = &cobra.Command{
	Use:   "show [name]",
	Short: "Show detailed squad agent configuration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := getManager()
		if err != nil {
			return err
		}
		a, err := manager.GetMemberByName(args[0])
		if err != nil {
			return err
		}

		fmt.Printf("Name: %s\n", a.Name)
		fmt.Printf("Squad Role: %s\n", kindDisplay(a.Kind))
		fmt.Printf("Model: %s\n", valueOrDash(a.Model))
		fmt.Printf("Description: %s\n", valueOrDash(a.Description))
		fmt.Printf("RAG: %s\n", enabledState(a.EnableRAG))
		fmt.Printf("Memory: %s\n", enabledState(a.EnableMemory))
		fmt.Printf("MCP: %s\n", enabledState(a.EnableMCP))
		fmt.Printf("PTC: %s\n", enabledState(a.EnablePTC))
		fmt.Printf("Skills: %s\n", joinOrDash(a.Skills))
		fmt.Printf("MCP Tools: %s\n", joinOrDash(a.MCPTools))
		fmt.Printf("Created: %s\n", formatTimestamp(a.CreatedAt))
		fmt.Printf("Updated: %s\n", formatTimestamp(a.UpdatedAt))
		if a.Instructions != "" {
			fmt.Printf("\nInstructions:\n%s\n", a.Instructions)
		}
		return nil
	},
}

func printAgentSection(title string, agentsList []*agent.AgentModel) {
	fmt.Println(title)
	if len(agentsList) == 0 {
		fmt.Println("  (none)")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tDESCRIPTION")
	for _, a := range agentsList {
		fmt.Fprintf(w, "%s\t%s\n", a.Name, a.Description)
	}
	w.Flush()
}

func compareAgentNames(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func valueOrDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func joinOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ", ")
}

func enabledState(v bool) string {
	if v {
		return "enabled"
	}
	return "disabled"
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func normalizeSquadRole(input string) (agent.AgentKind, error) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "", "specialist":
		return agent.AgentKindSpecialist, nil
	case "captain", "lead", "lead-agent", "leader":
		return agent.AgentKindCaptain, nil
	default:
		return "", fmt.Errorf("invalid role %q: use specialist or captain", input)
	}
}

func kindDisplay(kind agent.AgentKind) string {
	switch kind {
	case agent.AgentKindCaptain:
		return "captain"
	case agent.AgentKindSpecialist:
		return "specialist"
	case agent.AgentKindAgent:
		return "agent"
	default:
		return strings.ToLower(string(kind))
	}
}

func resolveSquad(manager *agent.SquadManager, input string) (*agent.Squad, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("squad name or id is required")
	}
	if squad, err := manager.GetSquadByName(input); err == nil {
		return squad, nil
	}
	squads, err := manager.ListSquads()
	if err != nil {
		return nil, err
	}
	for _, squad := range squads {
		if strings.EqualFold(strings.TrimSpace(squad.ID), input) {
			return squad, nil
		}
	}
	return nil, fmt.Errorf("unknown squad: %s", input)
}

func followSquadStatus(ctx context.Context, manager *agent.SquadManager, squadID string) error {
	lastSnapshot := ""
	lastTaskState := map[string]agent.SharedTaskStatus{}
	printedResults := map[string]struct{}{}

	for {
		status, err := manager.GetSquadStatus(squadID)
		if err != nil {
			return err
		}

		snapshot := fmt.Sprintf("%s|%d|%d|%d", status.Status, status.RunningTasks, status.QueuedTasks, status.AgentCount)
		if snapshot != lastSnapshot {
			fmt.Printf("Squad: %s\n", status.Name)
			fmt.Printf("Status: %s\n", status.Status)
			fmt.Printf("Captains: %s\n", joinOrDash(status.CaptainNames))
			fmt.Printf("Agents: %d\n", status.AgentCount)
			fmt.Printf("Running Tasks: %d\n", status.RunningTasks)
			fmt.Printf("Queued Tasks: %d\n\n", status.QueuedTasks)
			lastSnapshot = snapshot
		}

		tasks := manager.ListSharedTasksForSquad(squadID, time.Time{}, 50)
		for _, task := range tasks {
			if lastTaskState[task.ID] != task.Status {
				fmt.Printf("• %s [%s] %s\n", task.CaptainName, task.Status, trimForDisplay(task.Prompt, 100))
				lastTaskState[task.ID] = task.Status
			}
			if (task.Status == agent.SharedTaskStatusCompleted || task.Status == agent.SharedTaskStatusFailed) && task.ResultText != "" {
				if _, ok := printedResults[task.ID]; !ok {
					fmt.Printf("\n%s\n\n", trimForDisplay(task.ResultText, 800))
					printedResults[task.ID] = struct{}{}
				}
			}
		}

		if status.Status != "running" && status.Status != "queued" {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func trimForDisplay(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit-3] + "..."
}

func formatTimestamp(ts time.Time) string {
	if ts.IsZero() {
		return "-"
	}
	return ts.Format(time.RFC3339)
}

func resolveMemberSquadID(manager *agent.SquadManager, squadID, squadName string) (string, error) {
	if squadID != "" && squadName != "" {
		return "", fmt.Errorf("use either --squad-id or --squad, not both")
	}
	if squadID != "" {
		return squadID, nil
	}
	if squadName == "" {
		return "", nil
	}

	squads, err := manager.ListSquads()
	if err != nil {
		return "", err
	}
	for _, squad := range squads {
		if strings.EqualFold(strings.TrimSpace(squad.Name), squadName) {
			return squad.ID, nil
		}
	}
	return "", fmt.Errorf("unknown squad: %s", squadName)
}

func runInteractiveSquad(cmd *cobra.Command, args []string) error {
	manager, err := getManager()
	if err != nil {
		return err
	}
	return runInteractiveSquadChat(context.Background(), manager)
}

func runInteractiveSquadChat(ctx context.Context, manager *agent.SquadManager) error {
	conversationKey := "cli-squad-" + uuid.NewString()

	fmt.Println("🤝 AgentGo Squad Mode")
	fmt.Println("💡 Direct requests go to Captain by default")
	fmt.Println("💡 Use @Captain or any existing squad agent name to delegate")
	fmt.Println("💡 Ctrl+C cancels the current input")
	fmt.Println("💡 Ctrl+D exits when the input is empty")
	fmt.Println("💡 Type 'quit' or 'exit' to end")
	fmt.Println()

	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		return runInteractiveSquadLineEditor(ctx, manager, conversationKey)
	}

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("squad> ")
		if !scanner.Scan() {
			fmt.Println()
			return nil
		}

		input := strings.TrimSpace(scanner.Text())
		switch input {
		case "":
			continue
		case "quit", "exit":
			return nil
		}

		if err := runSquadMessage(ctx, manager, conversationKey, input); err != nil {
			fmt.Printf("Error: %v\n\n", err)
		}
	}
}

func runInteractiveSquadLineEditor(ctx context.Context, manager *agent.SquadManager, conversationKey string) error {
	fd := int(os.Stdin.Fd())

	for {
		oldState, err := term.MakeRaw(fd)
		if err != nil {
			return err
		}

		input, err := readInteractiveLine("squad> ")
		_ = term.Restore(fd, oldState)

		if err != nil {
			if errors.Is(err, errInputCanceled) {
				fmt.Print("^C\r\n")
				continue
			}
			if err == io.EOF {
				fmt.Println()
				return nil
			}
			return err
		}

		trimmed := strings.TrimSpace(input)
		switch trimmed {
		case "":
			continue
		case "quit", "exit":
			return nil
		}

		if err := runSquadMessage(ctx, manager, conversationKey, trimmed); err != nil {
			fmt.Printf("Error: %v\n\n", err)
		}
	}
}

func readInteractiveLine(prompt string) (string, error) {
	fmt.Print(prompt)

	var (
		buf      []rune
		cursor   int
		byteBuf  = make([]byte, 1)
		lastCols int
	)

	render := func() {
		line := string(buf)
		displayWidth := runewidth.StringWidth(line)
		if displayWidth < lastCols {
			fmt.Print("\r", prompt, line, strings.Repeat(" ", lastCols-displayWidth), "\r", prompt)
		} else {
			fmt.Print("\r", prompt, line, "\r", prompt)
		}
		if cursor > 0 {
			fmt.Print(renderCursorPrefix(string(buf[:cursor])))
		}
		lastCols = displayWidth
	}

	for {
		_, err := os.Stdin.Read(byteBuf)
		if err != nil {
			return "", err
		}

		b := byteBuf[0]
		switch b {
		case '\r', '\n':
			fmt.Print("\r", prompt, string(buf), strings.Repeat(" ", max(0, lastCols-runewidth.StringWidth(string(buf)))), "\r\n")
			return string(buf), nil
		case 3:
			return "", errInputCanceled
		case 4:
			if len(buf) == 0 {
				return "", io.EOF
			}
		case 127, 8:
			if cursor > 0 {
				buf = append(buf[:cursor-1], buf[cursor:]...)
				cursor--
				render()
			}
		case 27:
			seq, seqErr := readEscapeSequence()
			if seqErr != nil {
				return "", seqErr
			}
			switch seq {
			case "[D":
				if cursor > 0 {
					cursor--
					render()
				}
			case "[C":
				if cursor < len(buf) {
					cursor++
					render()
				}
			case "[3~":
				if cursor < len(buf) {
					buf = append(buf[:cursor], buf[cursor+1:]...)
					render()
				}
			case "[H", "OH":
				if cursor != 0 {
					cursor = 0
					render()
				}
			case "[F", "OF":
				if cursor != len(buf) {
					cursor = len(buf)
					render()
				}
			}
		default:
			r, size := decodeInputRune(b)
			if size == 0 {
				continue
			}
			buf = append(buf[:cursor], append([]rune{r}, buf[cursor:]...)...)
			cursor++
			render()
		}
	}
}

func readEscapeSequence() (string, error) {
	var seq []byte
	buf := make([]byte, 1)
	for len(seq) < 8 {
		_, err := os.Stdin.Read(buf)
		if err != nil {
			return "", err
		}
		seq = append(seq, buf[0])
		if (buf[0] >= 'A' && buf[0] <= 'Z') || buf[0] == '~' {
			break
		}
	}
	return string(seq), nil
}

func decodeInputRune(first byte) (rune, int) {
	return decodeInputRuneFromReader(os.Stdin, first)
}

func decodeInputRuneFromReader(reader io.Reader, first byte) (rune, int) {
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

func runSquadMessage(ctx context.Context, manager *agent.SquadManager, conversationKey, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil
	}

	fmt.Printf("\n🤔 You: %s\n", message)

	tasks, err := parseDelegatedTasks(message, func(name string) bool {
		_, getErr := manager.GetMemberByName(name)
		return getErr == nil
	})
	if err != nil {
		return err
	}

	if len(tasks) == 0 {
		tasks = []delegatedTask{{
			AgentName:   "Captain",
			Instruction: message,
		}}
	}

	var previousAgent string
	var previousResult string
	for idx, task := range tasks {
		fmt.Printf("\n🚀 Running %d/%d with @%s...\n", idx+1, len(tasks), task.AgentName)
		instruction := task.Instruction
		if idx > 0 && previousResult != "" {
			instruction = buildSequentialInstruction(previousAgent, previousResult, task.Instruction)
		}

		var (
			res         string
			dispatchErr error
		)
		debugMode := agentgolog.IsDebug()
		res, dispatchErr = runSquadLiveDispatch(ctx, manager, conversationKey, task.AgentName, instruction, debugMode)
		if dispatchErr != nil {
			return fmt.Errorf("task failed for @%s: %w", task.AgentName, dispatchErr)
		}
		if debugMode {
			fmt.Printf("\n✅ Response from @%s:\n%s\n", task.AgentName, res)
		}
		previousAgent = task.AgentName
		previousResult = strings.TrimSpace(res)
	}

	fmt.Println()
	return nil
}

func renderSquadDebugEvents(events <-chan *agent.Event) (string, error) {
	var partial strings.Builder
	var final string

	for evt := range events {
		switch evt.Type {
		case agent.EventTypeDebug:
			printSquadDebugBlock(evt.Round, evt.DebugType, evt.Content)
		case agent.EventTypeToolCall:
			printSquadDebugToolCall(evt.ToolName, evt.ToolArgs)
		case agent.EventTypeToolResult:
			printSquadDebugToolResult(evt.ToolName, evt.ToolResult, evt.Content)
		case agent.EventTypePartial:
			partial.WriteString(evt.Content)
		case agent.EventTypeComplete:
			final = strings.TrimSpace(evt.Content)
		case agent.EventTypeError:
			msg := strings.TrimSpace(evt.Content)
			if msg == "" {
				msg = "agent execution failed"
			}
			return "", errors.New(msg)
		}
	}

	if strings.TrimSpace(final) != "" {
		return final, nil
	}
	if strings.TrimSpace(partial.String()) != "" {
		return strings.TrimSpace(partial.String()), nil
	}
	return "", nil
}

func printSquadDebugBlock(round int, debugType, content string) {
	label := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(debugType), "_", " "))
	if label == "" {
		label = "DEBUG"
	}
	sep := strings.Repeat("─", 60)
	fmt.Printf("\n%s\n🐛 DEBUG [Round %d] %s\n%s\n%s\n", sep, round, label, sep, strings.TrimSpace(content))
}

func printSquadDebugToolCall(name string, args map[string]interface{}) {
	sep := strings.Repeat("─", 60)
	fmt.Printf("\n%s\n🛠 TOOL CALL: %s\n%s\n%s\n", sep, name, sep, formatSquadDebugValue(args))
}

func printSquadDebugToolResult(name string, result interface{}, errText string) {
	sep := strings.Repeat("─", 60)
	if strings.TrimSpace(errText) != "" {
		fmt.Printf("\n%s\n❌ TOOL RESULT: %s\n%s\n%s\n", sep, name, sep, strings.TrimSpace(errText))
		return
	}
	fmt.Printf("\n%s\n📦 TOOL RESULT: %s\n%s\n%s\n", sep, name, sep, formatSquadDebugValue(result))
}

func formatSquadDebugValue(v interface{}) string {
	switch val := v.(type) {
	case nil:
		return "(empty)"
	case string:
		trimmed := strings.TrimSpace(val)
		if trimmed == "" {
			return "(empty)"
		}
		return trimmed
	default:
		b, err := json.MarshalIndent(val, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}

func buildSequentialInstruction(previousAgent, previousResult, nextInstruction string) string {
	const maxContextChars = 6000

	previousResult = strings.TrimSpace(previousResult)
	if len(previousResult) > maxContextChars {
		previousResult = previousResult[:maxContextChars] + "\n...[truncated]"
	}

	return strings.TrimSpace(
		"Previous result from @" + previousAgent + ":\n" +
			previousResult + "\n\n" +
			"Use that result as input for your step. Complete the following task:\n" +
			strings.TrimSpace(nextInstruction),
	)
}

func parseDelegatedTasks(input string, isKnownAgent func(name string) bool) ([]delegatedTask, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, nil
	}

	words := strings.Fields(trimmed)
	if len(words) == 0 {
		return nil, nil
	}

	firstName, ok := parseMentionedAgent(words[0])
	if !ok {
		return nil, nil
	}
	if isKnownAgent != nil && !isKnownAgent(firstName) {
		return nil, fmt.Errorf("unknown agent: %s", firstName)
	}

	// Support leading shared mentions like:
	//   @Captain @SomeMember summarize the repo and write a note
	// In this form every leading mention receives the same instruction.
	leadingMentions := []string{firstName}
	firstInstructionIndex := 1
	for firstInstructionIndex < len(words) {
		nextName, isMention := parseMentionedAgent(words[firstInstructionIndex])
		if !isMention {
			break
		}
		if isKnownAgent != nil && !isKnownAgent(nextName) {
			return nil, fmt.Errorf("unknown agent: %s", nextName)
		}
		leadingMentions = append(leadingMentions, nextName)
		firstInstructionIndex++
	}
	if len(leadingMentions) > 1 {
		sharedInstruction := strings.TrimSpace(strings.Join(words[firstInstructionIndex:], " "))
		if sharedInstruction == "" {
			return nil, fmt.Errorf("please provide an instruction after the agent mentions")
		}
		tasks := make([]delegatedTask, 0, len(leadingMentions))
		for _, name := range leadingMentions {
			tasks = append(tasks, delegatedTask{
				AgentName:   name,
				Instruction: sharedInstruction,
			})
		}
		return tasks, nil
	}

	tasks := make([]delegatedTask, 0, 2)
	current := delegatedTask{AgentName: firstName}

	for _, word := range words[1:] {
		if nextName, isMention := parseMentionedAgent(word); isMention {
			if isKnownAgent != nil && isKnownAgent(nextName) {
				current.Instruction = strings.TrimSpace(current.Instruction)
				if current.Instruction == "" {
					return nil, fmt.Errorf("please provide an instruction for %s", current.AgentName)
				}
				tasks = append(tasks, current)
				current = delegatedTask{AgentName: nextName}
				continue
			}
		}

		if current.Instruction == "" {
			current.Instruction = word
		} else {
			current.Instruction += " " + word
		}
	}

	current.Instruction = strings.TrimSpace(current.Instruction)
	if current.Instruction == "" {
		return nil, fmt.Errorf("please provide an instruction for %s", current.AgentName)
	}
	tasks = append(tasks, current)
	return tasks, nil
}

func parseMentionedAgent(word string) (string, bool) {
	matches := agentMentionPattern.FindStringSubmatch(word)
	if len(matches) != 2 {
		return "", false
	}
	return matches[1], true
}
