package discovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// formicConfigDirOverrideVariable lets a test (or an operator) point the catalog and
// equip file at a scratch directory instead of the real per-platform user config root —
// never the real %APPDATA%\formic on a developer's own machine during a test run.
const formicConfigDirOverrideVariable = "FORMIC_CONFIG_DIR"

func DefaultUserConfigRoot() (string, error) {
	if override := os.Getenv(formicConfigDirOverrideVariable); override != "" {
		return override, nil
	}
	if runtime.GOOS == "darwin" {
		homeDirectory, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		xdgRoot := filepath.Join(homeDirectory, ".config")
		_, statErr := os.Stat(filepath.Join(xdgRoot, "formic"))
		if statErr == nil {
			return xdgRoot, nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect cross-platform config directory: %w", statErr)
		}
	}
	configRoot, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return configRoot, nil
}
