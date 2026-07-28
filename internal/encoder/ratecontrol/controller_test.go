package ratecontrol

import (
	"math"
	"testing"
)

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

func TestRateControlClosedLoopConverges(t *testing.T) {
	for _, mode := range []Mode{ModeVBR, ModeCBR} {
		t.Run(map[Mode]string{ModeVBR: "vbr", ModeCBR: "cbr"}[mode], func(t *testing.T) {
			rc, err := New(Config{
				Mode: mode, TargetKbps: 300,
				FrameRateNum: 30, FrameRateDen: 1, InitialQIndex: 80,
			})
			if err != nil {
				t.Fatal(err)
			}
			const targetBits = 10_000.0
			total := 0
			for frame := 0; frame < 180; frame++ {
				// Deterministic monotonic stand-in for an encoder: every
				// 32 qindex steps change frame size by approximately 2x.
				bits := int(targetBits * math.Exp2(float64(100-rc.QIndex())/32))
				total += bits
				rc.Update(bits, frame == 0)
				if math.Abs(rc.bufferBits) > rc.bufferCap {
					t.Fatalf("buffer %.0f exceeds cap %.0f", rc.bufferBits, rc.bufferCap)
				}
			}
			average := float64(total) / 180
			if errRatio := math.Abs(average-targetBits) / targetBits; errRatio > 0.10 {
				t.Fatalf("average %.0f bits, target %.0f (error %.1f%%)",
					average, targetBits, errRatio*100)
			}
		})
	}
}
