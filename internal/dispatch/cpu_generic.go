//go:build !amd64 || purego

package dispatch

func detectCPU() CPUFeatures {
	return CPUFeatures{}
}
