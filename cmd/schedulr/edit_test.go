package main

import "testing"

func TestEditStateAddAndFocusAddRow(t *testing.T) {
	sched := &plugSchedule{}
	e := newEditState(0, sched)
	if _, _, isAddRow := e.focused(); !isAddRow {
		t.Fatal("expected empty schedule to focus the add row")
	}
	e.addSegment()
	if len(sched.Segments) != 1 {
		t.Fatalf("len(Segments) = %d, want 1", len(sched.Segments))
	}
	segIdx, field, isAddRow := e.focused()
	if isAddRow || segIdx != 0 || field != 0 {
		t.Fatalf("focused() = (%d,%d,%v), want (0,0,false)", segIdx, field, isAddRow)
	}
}

func TestEditStateMoveSegmentClampsToAddRow(t *testing.T) {
	sched := &plugSchedule{Segments: []*segment{{StartMinute: 0, EndMinute: 15}}}
	e := newEditState(0, sched)
	e.moveSegment(100)
	if _, _, isAddRow := e.focused(); !isAddRow {
		t.Fatal("expected focus to clamp at the add row")
	}
	e.moveSegment(-100)
	segIdx, field, isAddRow := e.focused()
	if isAddRow || segIdx != 0 || field != 0 {
		t.Fatalf("focused() = (%d,%d,%v), want (0,0,false)", segIdx, field, isAddRow)
	}
}

func TestEditStateMoveFieldClampsWithinRow(t *testing.T) {
	sched := &plugSchedule{Segments: []*segment{{StartMinute: 0, EndMinute: 15}}}
	e := newEditState(0, sched)
	e.moveField(100)
	if _, field, _ := e.focused(); field != fieldsPerSegment-1 {
		t.Fatalf("field = %d, want %d", field, fieldsPerSegment-1)
	}
	e.moveField(-100)
	if _, field, _ := e.focused(); field != 0 {
		t.Fatalf("field = %d, want 0", field)
	}
}

func TestEditStateMoveSegmentPreservesField(t *testing.T) {
	sched := &plugSchedule{Segments: []*segment{
		{StartMinute: 0, EndMinute: 15},
		{StartMinute: 100, EndMinute: 120},
	}}
	e := newEditState(0, sched)
	e.moveField(1) // field=1=end, on segment 0
	e.moveSegment(1)
	segIdx, field, isAddRow := e.focused()
	if isAddRow || segIdx != 1 || field != 1 {
		t.Fatalf("focused() = (%d,%d,%v), want (1,1,false)", segIdx, field, isAddRow)
	}
}

func TestEditStateAdjustStartPushesEnd(t *testing.T) {
	sched := &plugSchedule{Segments: []*segment{{StartMinute: 0, EndMinute: 10}}}
	e := newEditState(0, sched)
	// focus is (seg=0, field=0=start)
	e.adjust(1) // start -> 5, still < end(10)-5=5, ok
	if sched.Segments[0].StartMinute != 5 || sched.Segments[0].EndMinute != 10 {
		t.Fatalf("segment = %+v", sched.Segments[0])
	}
	e.adjust(1) // start -> 10, end must push to 15 to keep 5-min minimum
	if sched.Segments[0].StartMinute != 10 || sched.Segments[0].EndMinute != 15 {
		t.Fatalf("segment = %+v", sched.Segments[0])
	}
}

func TestEditStateAdjustEndPushesStart(t *testing.T) {
	sched := &plugSchedule{Segments: []*segment{{StartMinute: 100, EndMinute: 110}}}
	e := newEditState(0, sched)
	e.moveField(1) // field=1=end
	e.adjust(-1)   // end -> 105, still >= start(100)+5, ok
	if sched.Segments[0].StartMinute != 100 || sched.Segments[0].EndMinute != 105 {
		t.Fatalf("segment = %+v", sched.Segments[0])
	}
	e.adjust(-1) // end -> 100, must push start down to 95
	if sched.Segments[0].StartMinute != 95 || sched.Segments[0].EndMinute != 100 {
		t.Fatalf("segment = %+v", sched.Segments[0])
	}
}

func TestEditStateRemoveFocusedSegment(t *testing.T) {
	sched := &plugSchedule{Segments: []*segment{
		{StartMinute: 0, EndMinute: 10},
		{StartMinute: 100, EndMinute: 110},
	}}
	e := newEditState(0, sched)
	e.removeFocusedSegment()
	if len(sched.Segments) != 1 || sched.Segments[0].StartMinute != 100 {
		t.Fatalf("Segments = %+v", sched.Segments)
	}
}

func TestEditStateRemoveOnAddRowIsNoop(t *testing.T) {
	sched := &plugSchedule{}
	e := newEditState(0, sched)
	e.removeFocusedSegment()
	if len(sched.Segments) != 0 {
		t.Fatalf("Segments = %+v, want empty", sched.Segments)
	}
}
