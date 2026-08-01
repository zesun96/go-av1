//go:build amd64 && !purego

package bitstream

import (
	"math/rand"
	"slices"
	"testing"
)

func TestSymbolAdaptSmallAMD64MatchesGo(t *testing.T) {
	for _, nSymbols := range []int{2, 3} {
		for _, disableUpdate := range []bool{false, true} {
			name := string(rune('0' + nSymbols))
			if disableUpdate {
				name += "_no_update"
			}
			t.Run(name, func(t *testing.T) {
				rng := rand.New(rand.NewSource(0x415631 + int64(nSymbols)))
				data := make([]byte, 4096)
				_, _ = rng.Read(data)
				scalar := NewMSAC(data, disableUpdate)
				native := NewMSAC(data, disableUpdate)
				scalarCDF := makeUniformDav1dCDF(nSymbols + 1)
				nativeCDF := slices.Clone(scalarCDF)

				original := haveSymbolAdaptSmallAMD64
				defer func() { haveSymbolAdaptSmallAMD64 = original }()
				for i := 0; i < 2048; i++ {
					haveSymbolAdaptSmallAMD64 = false
					want := scalar.SymbolAdaptDav1d(scalarCDF, nSymbols)
					haveSymbolAdaptSmallAMD64 = true
					got := native.SymbolAdaptDav1d(nativeCDF, nSymbols)
					if got != want || native.State() != scalar.State() || !slices.Equal(nativeCDF, scalarCDF) {
						t.Fatalf("step %d: native val/state/cdf = %d/%+v/%v; Go = %d/%+v/%v",
							i, got, native.State(), nativeCDF, want, scalar.State(), scalarCDF)
					}
				}
			})
		}
	}
}
