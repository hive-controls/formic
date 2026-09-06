package repository

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FindRoot walks upward from start to the nearest directory containing .git.
func FindRoot(start string) (string, error) {
	currentDirectory, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve starting directory: %w", err)
	}

	for {
		_, statErr := os.Stat(filepath.Join(currentDirectory, ".git"))
		if statErr == nil {
			return currentDirectory, nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect repository marker: %w", statErr)
		}
		parentDirectory := filepath.Dir(currentDirectory)
		if parentDirectory == currentDirectory {
			return "", fmt.Errorf("no repository found from %s", start)
		}
		currentDirectory = parentDirectory
	}
}
