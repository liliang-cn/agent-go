package runner

import (
	"os"
	"path/filepath"
)

// makeTempHome returns a unique scratch directory for a scenario's
// AGENTGO_HOME; it is the caller's responsibility to invoke cleanup.
func makeTempHome(scenarioName string) (string, func(), error) {
	dir, err := os.MkdirTemp(os.TempDir(), "agentgo-eval-"+scenarioName+"-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = removeAll(dir) }
	return dir, cleanup, nil
}

func removeAll(path string) error {
	if path == "" {
		return nil
	}
	clean := filepath.Clean(path)
	if clean == "/" || clean == "." {
		return nil
	}
	return os.RemoveAll(clean)
}
