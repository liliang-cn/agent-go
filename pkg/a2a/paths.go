package a2a

import (
	"fmt"
	"net/url"
	"strings"
)

func NormalizePathPrefix(prefix string) string {
	prefix = "/" + strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "/" {
		return "/a2a"
	}
	return prefix
}

func AgentCardPath(prefix, a2aID string) string {
	return fmt.Sprintf("%s/agents/%s/.well-known/agent-card.json", NormalizePathPrefix(prefix), url.PathEscape(strings.TrimSpace(a2aID)))
}

func AgentInvokePath(prefix, a2aID string) string {
	return fmt.Sprintf("%s/agents/%s/invoke", NormalizePathPrefix(prefix), url.PathEscape(strings.TrimSpace(a2aID)))
}

func SquadCardPath(prefix, a2aID string) string {
	return fmt.Sprintf("%s/squads/%s/.well-known/agent-card.json", NormalizePathPrefix(prefix), url.PathEscape(strings.TrimSpace(a2aID)))
}

func SquadInvokePath(prefix, a2aID string) string {
	return fmt.Sprintf("%s/squads/%s/invoke", NormalizePathPrefix(prefix), url.PathEscape(strings.TrimSpace(a2aID)))
}
