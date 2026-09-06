package discovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func DefaultUserConfigRoot() (string, error) {
	if runtime.GOOS == "darwin" {
		homeDirectory, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		xdgRoot := filepath.Join(homeDirectory, ".config")
		_, statErr := os.Stat(filepath.Join(xdgRoot, "hivedeck"))
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
