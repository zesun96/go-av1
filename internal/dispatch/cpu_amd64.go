//go:build amd64 && !purego

package dispatch

import "sync"

var (
	amd64Features CPUFeatures
	amd64Once     sync.Once
)

func detectCPU() CPUFeatures {
	amd64Once.Do(func() {
		maxLeaf, _, _, _ := cpuid(0, 0)
		if maxLeaf < 1 {
			return
		}
		_, _, ecx, _ := cpuid(1, 0)
		amd64Features.SSE3 = ecx&(1<<0) != 0
		amd64Features.SSSE3 = ecx&(1<<9) != 0
		amd64Features.SSE41 = ecx&(1<<19) != 0
		amd64Features.SSE42 = ecx&(1<<20) != 0

		osxsave := ecx&(1<<27) != 0
		hardwareAVX := ecx&(1<<28) != 0
		var xcr0 uint64
		if osxsave {
			xcr0 = xgetbv()
		}
		amd64Features.AVX = hardwareAVX && xcr0&0x6 == 0x6
		if maxLeaf < 7 {
			return
		}
		_, ebx, _, _ := cpuid(7, 0)
		amd64Features.AVX2 = amd64Features.AVX && ebx&(1<<5) != 0
		const avx512Mask = uint32(1<<16 | 1<<17 | 1<<28 | 1<<30 | 1<<31)
		amd64Features.AVX512 = amd64Features.AVX &&
			xcr0&0xe6 == 0xe6 &&
			ebx&avx512Mask == avx512Mask
	})
	return amd64Features
}

func cpuid(leaf, subleaf uint32) (eax, ebx, ecx, edx uint32)
func xgetbv() uint64
