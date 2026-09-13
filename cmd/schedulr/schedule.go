package main

import "time"

// segment describes one repeating on/off pulse within a day. StartMinute and
// EndMinute are offsets from midnight in [0,1440] with EndMinute >
// StartMinute (no overnight wrap; use two segments for that). When
// GapMinutes is 0 the segment is a single pulse. When it is >0 the pulse
// repeats with GapMinutes of off-time between the end of one pulse and the
// start of the next, RepeatCount times (0 means keep repeating for the rest
// of the day). E.g. a 15m pulse with a 10m gap produces 15m on, 10m off, 15m
// on, 10m off, ...
type segment struct {
	StartMinute int `json:"start_minute"`
	EndMinute   int `json:"end_minute"`
	GapMinutes  int `json:"gap_minutes,omitempty"`
	RepeatCount int `json:"repeat_count,omitempty"`
}

const minutesPerDay = 24 * 60

// duration returns the length of one pulse, in minutes.
func (s *segment) duration() int { return s.EndMinute - s.StartMinute }

// pulses returns every [start,end) window this segment is active during one
// day, expanding its repeat if configured.
func (s *segment) pulses() [][2]int {
	d := s.duration()
	if d <= 0 {
		return nil
	}
	if s.GapMinutes <= 0 {
		return [][2]int{{s.StartMinute, s.EndMinute}}
	}
	period := d + s.GapMinutes
	var pulses [][2]int
	for start, n := s.StartMinute, 0; start+d <= minutesPerDay; start += period {
		if s.RepeatCount > 0 && n >= s.RepeatCount {
			break
		}
		pulses = append(pulses, [2]int{start, start + d})
		n++
	}
	return pulses
}

// runCount reports how many pulses this segment produces in a day, for
// display (e.g. "48 runs").
func (s *segment) runCount() int { return len(s.pulses()) }

// plugSchedule is the persisted schedule for one plug (outlet).
type plugSchedule struct {
	Enabled  bool       `json:"enabled"`
	Segments []*segment `json:"segments"`
}

// isOnAtMinute reports whether any segment is active at the given
// minute-of-day.
func (p *plugSchedule) isOnAtMinute(minute int) bool {
	for _, seg := range p.Segments {
		for _, pulse := range seg.pulses() {
			if minute >= pulse[0] && minute < pulse[1] {
				return true
			}
		}
	}
	return false
}

// desiredState reports the on/off state the schedule calls for at t, and
// whether the schedule is enabled at all (an ignored/disabled schedule should
// never be corrected).
func (p *plugSchedule) desiredState(t time.Time) (on, managed bool) {
	if p == nil || !p.Enabled {
		return false, false
	}
	return p.isOnAtMinute(minuteOfDay(t)), true
}

func minuteOfDay(t time.Time) int { return t.Hour()*60 + t.Minute() }

func formatMinute(minute int) string {
	minute = ((minute % minutesPerDay) + minutesPerDay) % minutesPerDay
	return formatHHMM(minute/60, minute%60)
}

func formatHHMM(hour, minute int) string {
	const digits = "0123456789"
	buf := [5]byte{digits[hour/10], digits[hour%10], ':', digits[minute/10], digits[minute%10]}
	return string(buf[:])
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
