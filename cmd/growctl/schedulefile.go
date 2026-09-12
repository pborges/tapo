package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const scheduleFileVersion = 1

// scheduleFile is the on-disk format for every plug's schedule, keyed by the
// device's stable outlet (child) ID so it survives IP changes and renames.
type scheduleFile struct {
	Version int                      `json:"version"`
	Plugs   map[string]*plugSchedule `json:"plugs"`
}

func defaultSchedulePath() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "growctl-schedule.json"), nil
}

func loadScheduleFile(path string) (*scheduleFile, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &scheduleFile{Version: scheduleFileVersion, Plugs: map[string]*plugSchedule{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var file scheduleFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if file.Plugs == nil {
		file.Plugs = map[string]*plugSchedule{}
	}
	return &file, nil
}

// saveScheduleFile writes the file atomically (write to a temp file, then
// rename) so a crash mid-write never corrupts the schedule.
func saveScheduleFile(path string, file *scheduleFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file.Version = scheduleFileVersion
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".schedule-*.tmp")
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
	return os.Rename(tmpPath, path)
}
