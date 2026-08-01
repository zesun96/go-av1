//go:build amd64 && !purego

package bitstream

var haveSymbolAdaptSmallAMD64 = true

//go:noescape
func symbolAdapt2AMD64(m *MSAC, cdf *uint16) (val, shift uint32)

//go:noescape
func symbolAdapt3AMD64(m *MSAC, cdf *uint16) (val, shift uint32)
