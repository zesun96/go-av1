// Package dispatch detects CPU features and selects per-domain kernel tables.
//
// Each algorithmic package (transform, predict/intra, predict/inter, cdef,
// loopfilter, looprestoration, filmgrain) defines a kernel struct and a
// generic Go implementation. SIMD fast paths register themselves via the
// dispatch layer, gated on Detect() at init time.
//
// The build tag "purego" forces every dispatch lookup to the Go reference
// implementation, which is also the source of truth for tests.
//
// Reference implementation:
//
//   - dav1d/src/cpu.{c,h}
//
// Milestone: M0 (interface), M9 (assembly fast paths).
package dispatch

// CPUFeatures records which architecture-specific instruction extensions
// are available at runtime.
type CPUFeatures struct {
	// AMD64 / x86-64.
	SSE3, SSSE3, SSE41, SSE42 bool
	AVX, AVX2, AVX512         bool

	// ARM64 / AArch64.
	NEON, NEONDotProd, NEONI8MM bool
	SVE, SVE2                   bool
}

// Detect returns the runtime CPU feature set.
//
// At M0 it always returns the zero value, which is interpreted by every
// kernel table as "use the generic Go path".
func Detect() CPUFeatures {
	return CPUFeatures{}
}

// ForceGeneric overrides Detect to return the zero value for the rest of
// the process lifetime. Intended for testing the generic code path.
func ForceGeneric() {
	forceGeneric = true
}

var forceGeneric bool

// Active returns the effective feature set, taking ForceGeneric into account.
func Active() CPUFeatures {
	if forceGeneric {
		return CPUFeatures{}
	}
	return Detect()
}
