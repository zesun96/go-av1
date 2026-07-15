package tile

import "testing"

func TestPartition32CDFDefaults(t *testing.T) {
	want := [4][11]uint16{
		{14306, 11848, 9644, 5121, 4541, 3719, 3249, 2590, 1224, 0, 0},
		{25079, 23708, 20712, 7776, 7108, 6586, 5817, 4727, 3716, 0, 0},
		{26753, 23759, 22706, 8224, 7359, 6223, 5697, 5242, 721, 0, 0},
		{31374, 30560, 29972, 4154, 3707, 3302, 2928, 2583, 869, 0, 0},
	}
	if Partition32CDFDefault != want {
		t.Fatalf("Partition32CDFDefault = %v, want %v", Partition32CDFDefault, want)
	}
}
