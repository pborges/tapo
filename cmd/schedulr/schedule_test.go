package main

import (
	"testing"
	"time"
)

func TestSegmentPulsesNoRepeat(t *testing.T) {
	seg := &segment{StartMinute: 60, EndMinute: 90}
	pulses := seg.pulses()
	if len(pulses) != 1 || pulses[0] != [2]int{60, 90} {
		t.Fatalf("pulses = %v", pulses)
	}
	if got := seg.runCount(); got != 1 {
		t.Fatalf("runCount = %d, want 1", got)
	}
}

func TestSegmentPulsesRepeatRestOfDay(t *testing.T) {
	// 15m on, 30m gap -> 45m period.
	seg := &segment{StartMinute: 0, EndMinute: 15, GapMinutes: 30}
	if got := seg.runCount(); got != 32 {
		t.Fatalf("runCount = %d, want 32", got)
	}
	pulses := seg.pulses()
	if pulses[0] != [2]int{0, 15} || pulses[31] != [2]int{1395, 1410} {
		t.Fatalf("pulses[0]=%v pulses[31]=%v", pulses[0], pulses[31])
	}
}

func TestSegmentPulsesRepeatCount(t *testing.T) {
	// 10m on, 60m gap, 3 runs -> pulses 70m apart.
	seg := &segment{StartMinute: 0, EndMinute: 10, GapMinutes: 60, RepeatCount: 3}
	pulses := seg.pulses()
	want := [][2]int{{0, 10}, {70, 80}, {140, 150}}
	if len(pulses) != len(want) {
		t.Fatalf("pulses = %v, want %v", pulses, want)
	}
	for i := range want {
		if pulses[i] != want[i] {
			t.Fatalf("pulses[%d] = %v, want %v", i, pulses[i], want[i])
		}
	}
}

func TestSegmentPulsesAreSeparatedNotMerged(t *testing.T) {
	// 15m on, 10m gap, 10 runs must produce 10 distinct 15m pulses each
	// separated by a 10m off period, not one merged block.
	seg := &segment{StartMinute: 0, EndMinute: 15, GapMinutes: 10, RepeatCount: 10}
	pulses := seg.pulses()
	if len(pulses) != 10 {
		t.Fatalf("len(pulses) = %d, want 10", len(pulses))
	}
	for i, p := range pulses {
		wantStart := i * 25
		if p != [2]int{wantStart, wantStart + 15} {
			t.Fatalf("pulses[%d] = %v, want [%d %d]", i, p, wantStart, wantStart+15)
		}
	}
	// The gap between consecutive pulses must be off.
	if seg.pulses()[0][1] != 15 {
		t.Fatalf("first pulse should end at 15, got %v", seg.pulses()[0])
	}
}

func TestSegmentZeroOrNegativeDurationIsIgnored(t *testing.T) {
	seg := &segment{StartMinute: 90, EndMinute: 90}
	if pulses := seg.pulses(); pulses != nil {
		t.Fatalf("pulses = %v, want nil", pulses)
	}
}

func TestPlugScheduleDesiredState(t *testing.T) {
	sched := &plugSchedule{
		Enabled: true,
		Segments: []*segment{
			{StartMinute: 6 * 60, EndMinute: 20 * 60},
		},
	}
	on, managed := sched.desiredState(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	if !managed || !on {
		t.Fatalf("desiredState at noon = (%v,%v), want (true,true)", on, managed)
	}
	on, managed = sched.desiredState(time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC))
	if !managed || on {
		t.Fatalf("desiredState at 2am = (%v,%v), want (false,true)", on, managed)
	}
}

func TestPlugScheduleDisabledIsUnmanaged(t *testing.T) {
	sched := &plugSchedule{Enabled: false, Segments: []*segment{{StartMinute: 0, EndMinute: minutesPerDay}}}
	on, managed := sched.desiredState(time.Now())
	if managed || on {
		t.Fatalf("desiredState = (%v,%v), want (false,false)", on, managed)
	}
}

func TestOverlappingSegmentsUnion(t *testing.T) {
	sched := &plugSchedule{
		Enabled: true,
		Segments: []*segment{
			{StartMinute: 0, EndMinute: 60},
			{StartMinute: 30, EndMinute: 90},
		},
	}
	if !sched.isOnAtMinute(45) {
		t.Fatalf("expected minute 45 to be on")
	}
	if !sched.isOnAtMinute(75) {
		t.Fatalf("expected minute 75 to be on (from second segment)")
	}
	if sched.isOnAtMinute(91) {
		t.Fatalf("expected minute 91 to be off")
	}
}

func TestFormatMinute(t *testing.T) {
	cases := map[int]string{0: "00:00", 90: "01:30", 1439: "23:59"}
	for minute, want := range cases {
		if got := formatMinute(minute); got != want {
			t.Fatalf("formatMinute(%d) = %q, want %q", minute, got, want)
		}
	}
}
