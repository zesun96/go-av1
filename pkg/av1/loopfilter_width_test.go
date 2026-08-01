package av1

import "testing"

func TestSafeLoopFilterWidthMatchesTableReference(t *testing.T) {
	for width := -2; width <= 20; width++ {
		for before := -2; before <= 20; before++ {
			for after := -2; after <= 20; after++ {
				got := safeLoopFilterWidth(width, before, after)
				want := safeLoopFilterWidthReference(width, before, after)
				if got != want {
					t.Fatalf("width=%d before=%d after=%d got=%d want=%d",
						width, before, after, got, want)
				}
			}
		}
	}
}

func BenchmarkSafeLoopFilterWidth(b *testing.B) {
	for _, tc := range []struct {
		name string
		fn   func(int, int, int) int
	}{
		{name: "Table", fn: safeLoopFilterWidthReference},
		{name: "Branches", fn: safeLoopFilterWidth},
	} {
		b.Run(tc.name, func(b *testing.B) {
			var sink int
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				sink += tc.fn(16-(n&15), 2+(n&7), 2+((n>>3)&7))
			}
			benchmarkSafeLoopFilterWidthSink = sink
		})
	}
}

var benchmarkSafeLoopFilterWidthSink int

func safeLoopFilterWidthReference(width, before, after int) int {
	for _, candidate := range []int{16, 8, 6, 4} {
		if width >= candidate {
			radius := map[int]int{16: 7, 8: 4, 6: 3, 4: 2}[candidate]
			if before >= radius && after >= radius {
				return candidate
			}
		}
	}
	return 0
}
