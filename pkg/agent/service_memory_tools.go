package agent

import "strings"

func (s *Service) shouldExposeMemoryTools() bool {
	if s == nil || s.memoryService == nil {
		return false
	}
	return !s.isFileOnlyMemoryMode()
}

func (s *Service) isFileOnlyMemoryMode() bool {
	if s == nil {
		return false
	}
	storeType := strings.ToLower(strings.TrimSpace(s.memoryStoreType))
	return storeType == "" || storeType == "file"
}
