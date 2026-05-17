package header

import "testing"

func TestOBUType_String(t *testing.T) {
	cases := map[OBUType]string{
		OBUReserved0:            "Reserved0",
		OBUSequenceHeader:       "SequenceHeader",
		OBUTemporalDelimiter:    "TemporalDelimiter",
		OBUFrameHeader:          "FrameHeader",
		OBUTileGroup:            "TileGroup",
		OBUMetadata:             "Metadata",
		OBUFrame:                "Frame",
		OBURedundantFrameHeader: "RedundantFrameHeader",
		OBUTileList:             "TileList",
		OBUPadding:              "Padding",
		OBUType(9):              "OBUType(9)",
		OBUType(14):             "OBUType(14)",
	}
	for v, want := range cases {
		if got := v.String(); got != want {
			t.Fatalf("%d.String() = %q, want %q", v, got, want)
		}
	}
}

func TestOBUType_IsKnown(t *testing.T) {
	known := []OBUType{
		OBUSequenceHeader, OBUTemporalDelimiter, OBUFrameHeader,
		OBUTileGroup, OBUMetadata, OBUFrame, OBURedundantFrameHeader,
		OBUTileList, OBUPadding,
	}
	for _, v := range known {
		if !v.IsKnown() {
			t.Fatalf("%v expected IsKnown true", v)
		}
	}
	unknown := []OBUType{0, 9, 10, 14}
	for _, v := range unknown {
		if v.IsKnown() {
			t.Fatalf("%v expected IsKnown false", v)
		}
	}
}

func TestItoa(t *testing.T) {
	cases := map[uint8]string{
		0: "0", 1: "1", 9: "9", 10: "10", 99: "99",
		100: "100", 255: "255",
	}
	for v, want := range cases {
		if got := itoa(v); got != want {
			t.Fatalf("itoa(%d) = %q, want %q", v, got, want)
		}
	}
}
