package agent

import "errors"

type blockedReasoner interface {
	BlockedReason() string
}

func blockedReasonFromError(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var blocker blockedReasoner
	if errors.As(err, &blocker) {
		return blocker.BlockedReason(), true
	}
	return "", false
}
