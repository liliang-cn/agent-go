package agent

func (s *Service) shouldExposeMemoryTools() bool {
	return s != nil && s.memory() != nil
}
