package header

import "testing"

func TestFrameType_IsIntra(t *testing.T) {
	cases := []struct {
		t    FrameType
		want bool
	}{
		{FrameTypeKey, true},
		{FrameTypeIntra, true},
		{FrameTypeInter, false},
		{FrameTypeSwitch, false},
	}
	for _, c := range cases {
		if got := c.t.IsIntra(); got != c.want {
			t.Fatalf("FrameType(%d).IsIntra = %v, want %v", c.t, got, c.want)
		}
	}
}

func TestWarpedMotionParams_ABCD(t *testing.T) {
	w := WarpedMotionParams{Alpha: 1, Beta: 2, Gamma: 3, Delta: 4}
	got := w.ABCD()
	if got != [4]int16{1, 2, 3, 4} {
		t.Fatalf("ABCD = %v", got)
	}
}

// Compile-time sanity: spec constants line up with dav1d.
func TestConstants(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"MaxCDEFStrengths", MaxCDEFStrengths, 8},
		{"MaxOperatingPoints", MaxOperatingPoints, 32},
		{"MaxTileCols", MaxTileCols, 64},
		{"MaxTileRows", MaxTileRows, 64},
		{"MaxSegments", MaxSegments, 8},
		{"NumRefFrames", NumRefFrames, 8},
		{"PrimaryRefNone", PrimaryRefNone, 7},
		{"RefsPerFrame", RefsPerFrame, 7},
		{"TotalRefsPerFrame", TotalRefsPerFrame, 8},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Fatalf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}
