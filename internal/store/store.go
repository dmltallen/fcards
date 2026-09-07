// Package store persists fcards config and the offline outbox on disk.
package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"

	"fcards/internal/api"
)

type Config struct {
	Email          string `json:"email"`
	APIKey         string `json:"api_key"`
	WorkspaceID    string `json:"workspace_id"`
	WorkspaceName  string `json:"workspace_name"`
	ReplicaID      string `json:"replica_id"`
	InstallationID string `json:"installation_id"`
}

// MkdirData ensures the data directory exists.
func MkdirData(dir string) error { return os.MkdirAll(dir, 0o700) }

func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "fcards"), nil
}

func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "fcards"), nil
}

func LoadConfig() (*Config, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func SaveConfig(c *Config) error {
	dir, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return nil
}

// OutboxItem is a pending write batch (usually one review submission).
type OutboxItem struct {
	ID  string          `json:"id"`
	Ops []api.Operation `json:"ops"`
}

// OutboxCount returns how many write batches are queued for later sync.
func OutboxCount() int {
	items, err := LoadOutbox()
	if err != nil {
		return 0
	}
	return len(items)
}

func outboxPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "outbox.jsonl"), nil
}

func AppendOutbox(item OutboxItem) error {
	path, err := outboxPath()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func LoadOutbox() ([]OutboxItem, error) {
	path, err := outboxPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var items []OutboxItem
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var item OutboxItem
		if err := json.Unmarshal(line, &item); err == nil {
			items = append(items, item)
		}
	}
	return items, scanner.Err()
}

// RewriteOutbox replaces the outbox (after successful drain, keeping failures).
func RewriteOutbox(items []OutboxItem) error {
	path, err := outboxPath()
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, item := range items {
		data, err := json.Marshal(item)
		if err != nil {
			continue
		}
		if _, err := w.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	return w.Flush()
}
