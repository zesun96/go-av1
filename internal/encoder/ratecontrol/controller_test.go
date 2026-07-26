package ratecontrol

import "testing"

func TestCQPStaysFixed(t *testing.T) {
	rc, err := New(Config{
		Mode: ModeCQP, FrameRateNum: 30, FrameRateDen: 1, InitialQIndex: 77,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		rc.Update(1_000_000, false)
	}
	if got := rc.QIndex(); got != 77 {
		t.Fatalf("qindex=%d, want 77", got)
	}
}

func TestVBRRaisesQuantizerWhenOvershooting(t *testing.T) {
	rc, err := New(Config{
		Mode: ModeVBR, TargetKbps: 300,
		FrameRateNum: 30, FrameRateDen: 1, InitialQIndex: 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		rc.Update(40_000, false) // four times the 10 kbit frame budget
	}
	if got := rc.QIndex(); got <= 80 {
		t.Fatalf("qindex=%d, want an increase", got)
	}
}

func TestCBRLowersQuantizerWhenUndershooting(t *testing.T) {
	rc, err := New(Config{
		Mode: ModeCBR, TargetKbps: 300,
		FrameRateNum: 30, FrameRateDen: 1, InitialQIndex: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		rc.Update(1_000, false)
	}
	if got := rc.QIndex(); got >= 120 {
		t.Fatalf("qindex=%d, want a decrease", got)
	}
}
