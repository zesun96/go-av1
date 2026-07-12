package tile

import (
	"fmt"
	"os"
	"sort"
	"sync"
)

var chromaDebugEnabled = os.Getenv("GOAV1_DEBUG_CHROMA") != ""
var activeChromaDebug *chromaDebugStats

type chromaDebugStats struct {
	mu               sync.Mutex
	hasChromaBlocks  int
	uvModeCounts     map[int]int
	cflBlocks        int
	cflUZero         int
	cflVZero         int
	chromaSizeCounts map[string]int
	planeBlocks      [3]int
	planeNonSkip     [3]int
	planeNonZero     [3]int
	planeSkipCtx     [3]map[int]int
	planeTxCtx       [3]map[int]int
	planeTxtp        [3]map[int]int
	planeDcTokZero   [3]int
	planeDcTokNonZero [3]int
	planeEOB         [3]map[int]int
}

func newChromaDebugStats() *chromaDebugStats {
	if !chromaDebugEnabled {
		return nil
	}
	return &chromaDebugStats{
		uvModeCounts:     make(map[int]int),
		chromaSizeCounts: make(map[string]int),
		planeSkipCtx: [3]map[int]int{
			nil, make(map[int]int), make(map[int]int),
		},
		planeTxCtx: [3]map[int]int{
			nil, make(map[int]int), make(map[int]int),
		},
		planeTxtp: [3]map[int]int{
			nil, make(map[int]int), make(map[int]int),
		},
		planeEOB: [3]map[int]int{
			nil, make(map[int]int), make(map[int]int),
		},
	}
}

func (s *chromaDebugStats) record(st blockSyntaxState, intra intraSyntaxState, cbw, cbh int) {
	if s == nil || !st.hasChroma {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hasChromaBlocks++
	s.uvModeCounts[intra.uvMode]++
	s.chromaSizeCounts[fmt.Sprintf("%dx%d", cbw, cbh)]++
	if intra.uvMode == CFLPred {
		s.cflBlocks++
		if intra.cflAlphaU == 0 {
			s.cflUZero++
		}
		if intra.cflAlphaV == 0 {
			s.cflVZero++
		}
	}
}

func (s *chromaDebugStats) dump(logf func(string, ...any)) {
	if s == nil {
		return
	}
	outf := logf
	if chromaDebugEnabled || outf == nil {
		outf = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	outf("tile: chroma-debug hasChroma=%d cfl=%d cflUZero=%d cflVZero=%d",
		s.hasChromaBlocks, s.cflBlocks, s.cflUZero, s.cflVZero)
	keys := make([]int, 0, len(s.uvModeCounts))
	for k := range s.uvModeCounts {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		outf("tile: chroma-debug uvMode[%d]=%d", k, s.uvModeCounts[k])
	}
	sizeKeys := make([]string, 0, len(s.chromaSizeCounts))
	for k := range s.chromaSizeCounts {
		sizeKeys = append(sizeKeys, k)
	}
	sort.Strings(sizeKeys)
	for _, k := range sizeKeys {
		outf("tile: chroma-debug size[%s]=%d", k, s.chromaSizeCounts[k])
	}
	for pl := 1; pl <= 2; pl++ {
		outf("tile: chroma-debug plane[%d] blocks=%d nonskip=%d nonzero=%d",
			pl, s.planeBlocks[pl], s.planeNonSkip[pl], s.planeNonZero[pl])
		skipKeys := make([]int, 0, len(s.planeSkipCtx[pl]))
		for k := range s.planeSkipCtx[pl] {
			skipKeys = append(skipKeys, k)
		}
		sort.Ints(skipKeys)
		for _, k := range skipKeys {
			outf("tile: chroma-debug plane[%d] skipCtx[%d]=%d", pl, k, s.planeSkipCtx[pl][k])
		}
		txKeys := make([]int, 0, len(s.planeTxCtx[pl]))
		for k := range s.planeTxCtx[pl] {
			txKeys = append(txKeys, k)
		}
		sort.Ints(txKeys)
		for _, k := range txKeys {
			outf("tile: chroma-debug plane[%d] txCtx[%d]=%d", pl, k, s.planeTxCtx[pl][k])
		}
		txtpKeys := make([]int, 0, len(s.planeTxtp[pl]))
		for k := range s.planeTxtp[pl] {
			txtpKeys = append(txtpKeys, k)
		}
		sort.Ints(txtpKeys)
		for _, k := range txtpKeys {
			outf("tile: chroma-debug plane[%d] txtp[%d]=%d", pl, k, s.planeTxtp[pl][k])
		}
		outf("tile: chroma-debug plane[%d] dcTokZero=%d dcTokNonZero=%d",
			pl, s.planeDcTokZero[pl], s.planeDcTokNonZero[pl])
	}
}

func recordChromaDebug(st blockSyntaxState, intra intraSyntaxState, cbw, cbh int) {
	if activeChromaDebug != nil {
		activeChromaDebug.record(st, intra, cbw, cbh)
	}
}

func recordChromaResidualDebug(plane int, skipped bool, eob int, skipCtx int, txCtx int) {
	if activeChromaDebug == nil || plane < 1 || plane > 2 {
		return
	}
	activeChromaDebug.mu.Lock()
	defer activeChromaDebug.mu.Unlock()
	activeChromaDebug.planeBlocks[plane]++
	activeChromaDebug.planeSkipCtx[plane][skipCtx]++
	activeChromaDebug.planeTxCtx[plane][txCtx]++
	if !skipped {
		activeChromaDebug.planeNonSkip[plane]++
		if eob >= 0 {
			activeChromaDebug.planeNonZero[plane]++
		}
	}
}

func recordChromaResidualCtx(plane int, skipCtx int, txCtx int) {
	if activeChromaDebug == nil || plane < 1 || plane > 2 {
		return
	}
	activeChromaDebug.mu.Lock()
	defer activeChromaDebug.mu.Unlock()
	activeChromaDebug.planeSkipCtx[plane][skipCtx]++
	activeChromaDebug.planeTxCtx[plane][txCtx]++
}

func recordChromaResidualTokenDebug(plane int, txtp int, eob int, dcTok int) {
	if activeChromaDebug == nil || plane < 1 || plane > 2 {
		return
	}
	activeChromaDebug.mu.Lock()
	defer activeChromaDebug.mu.Unlock()
	activeChromaDebug.planeTxtp[plane][txtp]++
	activeChromaDebug.planeEOB[plane][eob]++
	if dcTok == 0 {
		activeChromaDebug.planeDcTokZero[plane]++
	} else {
		activeChromaDebug.planeDcTokNonZero[plane]++
	}
}
