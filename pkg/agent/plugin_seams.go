package agent

// PluginInfo describes one plugin a host installed on a Builder. The agent
// package neither reads nor interprets plugins — pkg/plugin does, by calling
// the Builder's ordinary options — so this is only the record that lets a
// running service say what it was built from.
type PluginInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Dir         string `json:"dir"`
	// Components names what the plugin contributed: skills, mcp, agents,
	// prompt, extension.
	Components []string `json:"components,omitempty"`
	// Skipped names what the plugin carries but was not wired, with the
	// reason, e.g. "extension: exec not allowed".
	Skipped []string `json:"skipped,omitempty"`
}

// AddSkillsPaths adds skill directories and turns skills on. Unlike
// WithSkills, which names the whole set, it appends: the install's default
// directories (or the ones WithSkillsPaths gave) stay.
func (b *Builder) AddSkillsPaths(paths ...string) *Builder {
	if len(paths) == 0 {
		return b
	}
	b.enableSkills = true
	b.pluginSkillsPaths = append(b.pluginSkillsPaths, paths...)
	return b
}

// AddMCPConfigPaths adds MCP server files and turns MCP on. Unlike WithMCP
// with WithMCPConfigPaths, which replaces the install's configured servers,
// it appends to them.
func (b *Builder) AddMCPConfigPaths(paths ...string) *Builder {
	if len(paths) == 0 {
		return b
	}
	b.enableMCP = true
	b.pluginMCPPaths = append(b.pluginMCPPaths, paths...)
	return b
}

// RecordPlugin notes that a plugin was installed, for Service.Plugins.
func (b *Builder) RecordPlugin(info PluginInfo) *Builder {
	b.plugins = append(b.plugins, info)
	return b
}

// Plugins lists the plugins this service was built from, in install order.
func (s *Service) Plugins() []PluginInfo {
	out := make([]PluginInfo, len(s.plugins))
	copy(out, s.plugins)
	return out
}

// InstalledPlugins lists what has been recorded on the Builder so far.
func (b *Builder) InstalledPlugins() []PluginInfo {
	return append([]PluginInfo(nil), b.plugins...)
}
