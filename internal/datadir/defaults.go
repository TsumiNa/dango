package datadir

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	appHomeDirName     = ".dango"
	defaultDataDirName = "data"
)

// AppHome returns the user-scoped dango home directory.
//
// AppHome is intended to hold user-specific dango state such as the default
// data directory and future configuration files.
func AppHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}

	return filepath.Join(home, appHomeDirName), nil
}

// DefaultRoot returns the default orchestrator data directory path.
func DefaultRoot() (string, error) {
	appHome, err := AppHome()
	if err != nil {
		return "", err
	}

	return filepath.Join(appHome, defaultDataDirName), nil
}
