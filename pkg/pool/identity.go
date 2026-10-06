package pool

import "strings"

// Model identity for a pool used as a service's generator.
//
// A Pool is the generator most hosts hand to WithLLM, and until now it did
// not say which model it was: the service's Info().Model stayed empty and the
// context window could not be resolved. The pool knows its providers' models,
// so it can answer.

// GetModelName reports the model this pool serves: the one model every
// provider is configured with, or the first provider's when they differ.
// Empty when no provider is configured.
func (p *Pool) GetModelName() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, prov := range p.config.Providers {
		if m := strings.TrimSpace(prov.ModelName); m != "" {
			return m
		}
	}
	return ""
}

// GetBaseURL reports the first configured provider's base URL, for the
// service's Info(). Empty when no provider is configured.
func (p *Pool) GetBaseURL() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, prov := range p.config.Providers {
		if u := strings.TrimSpace(prov.BaseURL); u != "" {
			return u
		}
	}
	return ""
}

// UsageModel is GetModelName under the name usage accounting asks for.
func (p *Pool) UsageModel() string { return p.GetModelName() }
