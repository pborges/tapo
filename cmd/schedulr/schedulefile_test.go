package main

import (
	"path/filepath"
	"testing"
)

func TestLoadScheduleFileMissingReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	file, err := loadScheduleFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Plugs == nil || len(file.Plugs) != 0 {
		t.Fatalf("file = %+v, want empty non-nil Plugs", file)
	}
}

func TestSaveAndLoadScheduleFileRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "schedule.json")
	want := &scheduleFile{Plugs: map[string]*plugSchedule{
		"outlet-1": {Enabled: true, Segments: []*segment{
			{StartMinute: 360, EndMinute: 1200},
			{StartMinute: 0, EndMinute: 15, GapMinutes: 30, RepeatCount: 10},
		}},
	}}
	if err := saveScheduleFile(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadScheduleFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sched, ok := got.Plugs["outlet-1"]
	if !ok || !sched.Enabled || len(sched.Segments) != 2 {
		t.Fatalf("loaded = %+v", got.Plugs)
	}
	if sched.Segments[1].GapMinutes != 30 || sched.Segments[1].RepeatCount != 10 {
		t.Fatalf("segment[1] = %+v", sched.Segments[1])
	}
}
