package reference

import "testing"

func TestManagerRotatesSlotsAndRetainsHistory(t *testing.T) {
	var m Manager
	m.ResetKey()
	if got := m.Latest(); got != 0 {
		t.Fatalf("latest after key=%d, want 0", got)
	}
	for frame := 1; frame <= 10; frame++ {
		plan := m.PlanInter()
		wantTarget := frame & 7
		if plan.TargetSlot != wantTarget || plan.RefreshFlags != 1<<uint(wantTarget) {
			t.Fatalf("frame %d plan=%+v, want target %d", frame, plan, wantTarget)
		}
		if int(plan.RefIdx[0]) != m.Latest() {
			t.Fatalf("frame %d LAST slot=%d, latest=%d", frame, plan.RefIdx[0], m.Latest())
		}
		m.CommitInter(plan.TargetSlot)
		if m.Latest() != wantTarget {
			t.Fatalf("frame %d latest=%d, want %d", frame, m.Latest(), wantTarget)
		}
		history := m.History()
		if history[0] != wantTarget {
			t.Fatalf("frame %d history=%v", frame, history)
		}
	}
}

func TestManagerMapsLast2ToPreviousFrame(t *testing.T) {
	var m Manager
	m.ResetKey()
	first := m.PlanInter()
	m.CommitInter(first.TargetSlot)
	second := m.PlanInter()
	if second.RefIdx[0] != 1 || second.RefIdx[1] != 0 {
		t.Fatalf("refs=%v, want LAST=1 LAST2=0", second.RefIdx)
	}
}
