package storage

import (
	"fmt"
	"os"
	"path/filepath"
)

// Environment variables that locate the data directory. The api
// resolves the same chain, so bench and api agree without configuration.
const (
	dataDirEnv     = "SERVTUNER_DATA_DIR"
	xdgDataHomeEnv = "XDG_DATA_HOME"
	runsSubdir     = "runs"
)

// DataDir returns the directory under which the file backend keeps bundles:
// $SERVTUNER_DATA_DIR, else $XDG_DATA_HOME/servtuner, else ~/.local/share/servtuner.
// This is the only place that applies the rule.
func DataDir() (string, error) {
	if dir := os.Getenv(dataDirEnv); dir != "" {
		return dir, nil
	}
	if dir := os.Getenv(xdgDataHomeEnv); dir != "" {
		return filepath.Join(dir, "servtuner"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate data directory (set %s to choose one): %w", dataDirEnv, err)
	}
	return filepath.Join(home, ".local", "share", "servtuner"), nil
}

// RunBundleDir is where the file backend keeps the bundle of one run:
// <dataDir>/runs/<runID>.
func RunBundleDir(dataDir, runID string) string {
	return filepath.Join(dataDir, runsSubdir, runID)
}
