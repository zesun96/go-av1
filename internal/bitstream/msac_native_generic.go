//go:build !amd64 || purego

package bitstream

const haveSymbolAdaptSmallAMD64 = false

func symbolAdapt2AMD64(m *MSAC, cdf *uint16) (val, shift uint32) { return 0, 0 }
func symbolAdapt3AMD64(m *MSAC, cdf *uint16) (val, shift uint32) { return 0, 0 }
