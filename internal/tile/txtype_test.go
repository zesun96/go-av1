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
