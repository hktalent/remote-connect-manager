package screenshot

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var screenshotDir string

func Init(dir string) error {
	screenshotDir = dir
	return os.MkdirAll(dir, 0755)
}

// SaveScreenshotData saves base64 PNG data and returns the relative path
func SaveScreenshotData(connID, base64Data string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}
	filename := fmt.Sprintf("%s_%d.png", connID, time.Now().Unix())
	fullPath := filepath.Join(screenshotDir, filename)
	if err := os.WriteFile(fullPath, data, 0644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	// Return relative URL path
	return "/screenshots/" + filename, nil
}

// CleanupOldScreenshots removes screenshots except the latest one for a connection
func CleanupOldScreenshots(connID string) {
	matches, _ := filepath.Glob(filepath.Join(screenshotDir, connID+"_*.png"))
	if len(matches) <= 1 {
		return
	}
	// keep only the most recent
	latest := ""
	var latestMod time.Time
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		if latest == "" || fi.ModTime().After(latestMod) {
			latest = m
			latestMod = fi.ModTime()
		}
	}
	for _, m := range matches {
		if m != latest {
			os.Remove(m)
		}
	}
}
