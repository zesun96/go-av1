package dispatch

import "testing"

func TestActiveCanBeForcedGeneric(t *testing.T) {
	before := Active()
	ForceGeneric()
	if got := Active(); got != (CPUFeatures{}) {
		t.Fatalf("Active after ForceGeneric = %+v, want zero features", got)
	}
	if before.AVX2 && !before.AVX {
		t.Fatal("AVX2 detected without AVX OS support")
	}
	if before.AVX512 && !before.AVX {
		t.Fatal("AVX-512 detected without AVX OS support")
	}
}
