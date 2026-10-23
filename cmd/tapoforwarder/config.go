package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const configFileVersion = 1

// configFile is the on-disk list of managed strips. Aliases and outlets are a
// cache of what the device last reported, so metrics keep their names in the
// config even while a strip is offline.
type configFile struct {
	Version int           `json:"version"`
	Strips  []stripConfig `json:"strips"`
}

type stripConfig struct {
	Host     string         `json:"host"`
	Port     int            `json:"port,omitempty"`
	HTTPPort int            `json:"http_port,omitempty"`
	Alias    string         `json:"alias,omitempty"`
	Model    string         `json:"model,omitempty"`
	DeviceID string         `json:"device_id,omitempty"`
	Outlets  []outletConfig `json:"outlets,omitempty"`
}

type outletConfig struct {
	Index int    `json:"index"`
	ID    string `json:"id"`
	Alias string `json:"alias"`
}

// configStore guards the config file and persists every change.
type configStore struct {
	path string
	mu   sync.Mutex
	file configFile
}

func loadConfig(path string) (*configStore, error) {
	store := &configStore{path: path, file: configFile{Version: configFileVersion}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &store.file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return store, nil
}

// strips returns a copy of the configured strips.
func (c *configStore) strips() []stripConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]stripConfig, len(c.file.Strips))
	for i, strip := range c.file.Strips {
		strip.Outlets = append([]outletConfig(nil), strip.Outlets...)
		result[i] = strip
	}
	return result
}

// add appends strip unless its host is already configured. It reports whether
// the strip was added.
func (c *configStore) add(strip stripConfig) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, existing := range c.file.Strips {
		if existing.Host == strip.Host {
			return false, nil
		}
	}
	c.file.Strips = append(c.file.Strips, strip)
	return true, c.saveLocked()
}

func (c *configStore) remove(host string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, existing := range c.file.Strips {
		if existing.Host == host {
			c.file.Strips = append(c.file.Strips[:i], c.file.Strips[i+1:]...)
			return true, c.saveLocked()
		}
	}
	return false, nil
}

// update applies fn to the strip with host and saves only when fn reports a
// change, so steady-state polling never rewrites the file.
func (c *configStore) update(host string, fn func(*stripConfig) bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.file.Strips {
		if c.file.Strips[i].Host == host {
			if fn(&c.file.Strips[i]) {
				return c.saveLocked()
			}
			return nil
		}
	}
	return nil
}

// saveLocked writes the file atomically (write to a temp file, then rename) so
// a crash mid-write never corrupts the config.
func (c *configStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	c.file.Version = configFileVersion
	data, err := json.MarshalIndent(c.file, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".tapoforwarder-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, c.path)
}
