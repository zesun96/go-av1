package tile

import "testing"

func TestTxTypeIntraSetsCoverImplicitLastSymbol(t *testing.T) {
	if len(TxTypeIntra2Set) != TxTypeIntra2Symbols {
		t.Fatalf("len(TxTypeIntra2Set) = %d, want %d", len(TxTypeIntra2Set), TxTypeIntra2Symbols)
	}
	if got := TxTypeIntra2Set[TxTypeIntra2Symbols-1]; got != 2 {
		t.Fatalf("TxTypeIntra2Set last = %d, want 2 (DCT_ADST)", got)
	}

	if len(TxTypeIntra1Set) != TxTypeIntra1Symbols {
		t.Fatalf("len(TxTypeIntra1Set) = %d, want %d", len(TxTypeIntra1Set), TxTypeIntra1Symbols)
	}
	if got := TxTypeIntra1Set[TxTypeIntra1Symbols-1]; got != 2 {
		t.Fatalf("TxTypeIntra1Set last = %d, want 2 (DCT_ADST)", got)
	}
}

func TestTxTypeIntraCDFSentinelMatchesSymbolCount(t *testing.T) {
	if got := DefaultTxTypeIntra2CDF[TxTypeIntra2Symbols-1]; got != 0 {
		t.Fatalf("DefaultTxTypeIntra2CDF sentinel = %d, want 0", got)
	}
	if got := DefaultTxTypeIntra1CDF[TxTypeIntra1Symbols-1]; got != 0 {
		t.Fatalf("DefaultTxTypeIntra1CDF sentinel = %d, want 0", got)
	}

	for i := range TxTypeIntra2CDFDefault {
		for j := range TxTypeIntra2CDFDefault[i] {
			if got := TxTypeIntra2CDFDefault[i][j][TxTypeIntra2Symbols-1]; got != 0 {
				t.Fatalf("TxTypeIntra2CDFDefault[%d][%d] sentinel = %d, want 0", i, j, got)
			}
		}
	}
	for i := range TxTypeIntra1CDFDefault {
		for j := range TxTypeIntra1CDFDefault[i] {
			if got := TxTypeIntra1CDFDefault[i][j][TxTypeIntra1Symbols-1]; got != 0 {
				t.Fatalf("TxTypeIntra1CDFDefault[%d][%d] sentinel = %d, want 0", i, j, got)
			}
		}
	}
}

func TestTxTypeIntra2CDFMatchesDav1dMode2(t *testing.T) {
	want := [TxTypeIntra2Symbols + 1]uint16{
		32768 - 362, 32768 - 5887, 32768 - 11678, 32768 - 16725, 0, 0,
	}
	if got := TxTypeIntra2CDFDefault[2][2]; got != want {
		t.Fatalf("TxTypeIntra2CDFDefault[2][2] = %v, want %v", got, want)
	}
}

func TestTxTypeIntra1TX4RowsMatchDav1d(t *testing.T) {
	wantTX4 := [NIntraPredModes][TxTypeIntra1Symbols + 1]uint16{
		{31233, 24733, 23307, 20017, 9301, 4943, 0, 0},
		{32204, 29433, 23059, 21898, 14625, 4674, 0, 0},
		{32096, 29521, 29092, 20786, 13353, 9641, 0, 0},
		{27489, 18883, 17281, 14724, 9241, 2516, 0, 0},
		{28345, 26694, 24783, 22352, 7075, 3470, 0, 0},
		{31282, 28527, 23308, 22106, 16312, 5074, 0, 0},
		{32329, 29930, 29246, 26031, 14710, 9014, 0, 0},
		{31578, 28535, 27913, 21098, 12487, 8391, 0, 0},
		{31723, 28456, 24121, 22609, 14124, 3433, 0, 0},
		{32566, 29034, 28021, 25470, 15641, 8752, 0, 0},
		{32321, 28456, 25949, 23884, 16758, 8910, 0, 0},
		{32491, 28399, 27513, 23863, 16303, 10497, 0, 0},
		{29359, 27332, 22169, 17169, 13081, 8728, 0, 0},
	}
	wantTX8 := [NIntraPredModes][TxTypeIntra1Symbols + 1]uint16{
		{30898, 19026, 18238, 16270, 8998, 5070, 0, 0},
		{32442, 23972, 18136, 17689, 13496, 5282, 0, 0},
		{32284, 25192, 25056, 18325, 13609, 10177, 0, 0},
		{31642, 17428, 16873, 15745, 11872, 2489, 0, 0},
		{32113, 27914, 27519, 26855, 10669, 5630, 0, 0},
		{31469, 26310, 23883, 23478, 17917, 7271, 0, 0},
		{32457, 27473, 27216, 25883, 16661, 10096, 0, 0},
		{31885, 24709, 24498, 21510, 15479, 11219, 0, 0},
		{32027, 25188, 23450, 22423, 16080, 3722, 0, 0},
		{32658, 25362, 24853, 23573, 16727, 9439, 0, 0},
		{32405, 24794, 23411, 22095, 17139, 8294, 0, 0},
		{32615, 25121, 24656, 22832, 17461, 12772, 0, 0},
		{29257, 26436, 21603, 17433, 13445, 9174, 0, 0},
	}
	for mode := range wantTX4 {
		if got := TxTypeIntra1CDFDefault[0][mode]; got != wantTX4[mode] {
			t.Fatalf("TX4 intra1 mode %d CDF = %v, want %v", mode, got, wantTX4[mode])
		}
		if got := TxTypeIntra1CDFDefault[1][mode]; got != wantTX8[mode] {
			t.Fatalf("TX8 intra1 mode %d CDF = %v, want %v", mode, got, wantTX8[mode])
		}
	}
}
