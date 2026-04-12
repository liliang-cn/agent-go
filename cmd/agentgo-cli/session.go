package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/store"
	"github.com/spf13/cobra"
)

var sessionTraceJSON bool

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Inspect chat sessions",
}

var sessionTraceCmd = &cobra.Command{
	Use:   "trace [session-id]",
	Short: "Trace a session's messages, task ids, and tool calls",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := store.NewAgentGoDB(Cfg.AgentDBPath())
		if err != nil {
			return err
		}
		defer db.Close()
		session, err := db.GetSession(args[0])
		if err != nil {
			return err
		}
		if sessionTraceJSON {
			data, err := json.MarshalIndent(session, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}
		fmt.Printf("Session: %s\n", session.ID)
		fmt.Printf("Type:    %s\n", session.Type)
		fmt.Printf("Title:   %s\n", session.Title)
		fmt.Printf("Messages:%d\n", len(session.Messages))
		fmt.Println(strings.Repeat("-", 40))
		for i, msg := range session.Messages {
			fmt.Printf("[%d] role=%s task=%s", i+1, msg.Role, valueOrDash(msg.TaskID))
			if msg.ToolCallID != "" {
				fmt.Printf(" tool_call_id=%s", msg.ToolCallID)
			}
			fmt.Println()
			for _, call := range msg.ToolCalls {
				fmt.Printf("    tool_call %s %s\n", call.ID, call.Function.Name)
			}
			if strings.TrimSpace(msg.Content) != "" {
				fmt.Printf("    %s\n", trimTaskText(msg.Content, 240))
			}
		}
		return nil
	},
}

func init() {
	sessionCmd.AddCommand(sessionTraceCmd)
	sessionTraceCmd.Flags().BoolVar(&sessionTraceJSON, "json", false, "Output as JSON")
}
