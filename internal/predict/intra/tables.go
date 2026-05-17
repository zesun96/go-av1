package intra

// SmWeights is a verbatim port of dav1d_sm_weights from
// dav1d/src/tables.c (lines 690-716). The table is indexed by block
// edge size: callers offset by `bs` (≥2), so weights[bs..bs+bs-1]
// holds the per-position weights used by the SMOOTH/SMOOTH_H/SMOOTH_V
// intra prediction modes.
var SmWeights = [128]uint8{
	// Unused leading entries; the AV1 spec only references offsets
	// starting at bs=2.
	0, 0,
	// bs = 2
	255, 128,
	// bs = 4
	255, 149, 85, 64,
	// bs = 8
	255, 197, 146, 105, 73, 50, 37, 32,
	// bs = 16
	255, 225, 196, 170, 145, 123, 102, 84,
	68, 54, 43, 33, 26, 20, 17, 16,
	// bs = 32
	255, 240, 225, 210, 196, 182, 169, 157,
	145, 133, 122, 111, 101, 92, 83, 74,
	66, 59, 52, 45, 39, 34, 29, 25,
	21, 17, 14, 12, 10, 9, 8, 8,
	// bs = 64
	255, 248, 240, 233, 225, 218, 210, 203,
	196, 189, 182, 176, 169, 163, 156, 150,
	144, 138, 133, 127, 121, 116, 111, 106,
	101, 96, 91, 86, 82, 77, 73, 69,
	65, 61, 57, 54, 50, 47, 44, 41,
	38, 35, 32, 29, 27, 25, 22, 20,
	18, 16, 15, 13, 12, 10, 9, 8,
	7, 6, 6, 5, 5, 4, 4, 4,
}

// DrIntraDerivative is a verbatim port of dav1d_dr_intra_derivative
// (dav1d/src/tables.c lines 719-749). Indexed by (angle >> 1) for the
// AV1 directional intra prediction modes; entries marked 0 in the
// table are never used by the spec because they sit between the
// quantised angle steps.
var DrIntraDerivative = [44]uint16{
	0,
	1023, 0,
	547,
	372, 0, 0,
	273,
	215, 0,
	178,
	151, 0,
	132,
	116, 0,
	102, 0,
	90,
	80, 0,
	71,
	64, 0,
	57,
	51, 0,
	45, 0,
	40,
	35, 0,
	31,
	27, 0,
	23,
	19, 0,
	15, 0,
	11, 0,
	7,
	3,
}
