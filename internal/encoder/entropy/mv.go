package entropy

import (
	"math/bits"

	"github.com/zesun96/go-av1/internal/encoder/bitwriter"
	"github.com/zesun96/go-av1/internal/tile"
)

const (
	mvJointZero = iota
	mvJointH
	mvJointV
	mvJointHV
)

// EncodeMVResidual writes one NEWMV difference in AV1 1/8-pixel units.
func EncodeMVResidual(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx, x, y int, highPrecision bool) {
	joint := mvJointZero
	if x != 0 {
		joint |= mvJointH
	}
	if y != 0 {
		joint |= mvJointV
	}
	ec.SymbolAdaptDav1d(uint32(joint), ctx.MVJointCDF[:], 3)
	if y != 0 {
		encodeMVComponent(ec, ctx, 0, y, highPrecision)
	}
	if x != 0 {
		encodeMVComponent(ec, ctx, 1, x, highPrecision)
	}
}

func encodeMVComponent(ec *bitwriter.MSACEncoder, ctx *tile.TileCtx,
	comp, diff int, highPrecision bool,
) {
	sign := diff < 0
	if sign {
		diff = -diff
	}
	value := diff - 1
	up := value >> 3
	fp := (value >> 1) & 3
	hp := value & 1

	class := 0
	if up >= 2 {
		class = bits.Len(uint(up)) - 1
	}
	ec.BoolAdapt(boolSymbol(sign), ctx.MVSignCDF[comp][:])
	ec.SymbolAdaptDav1d(uint32(class), ctx.MVClassesCDF[comp][:], 10)
	if class == 0 {
		ec.BoolAdapt(uint32(up), ctx.MVClass0CDF[comp][:])
		ec.SymbolAdaptDav1d(uint32(fp), ctx.MVClass0FPCDF[comp][up][:], 3)
		if highPrecision {
			ec.BoolAdapt(uint32(hp), ctx.MVClass0HPCDF[comp][:])
		}
		return
	}
	for n := 0; n < class; n++ {
		ec.BoolAdapt(uint32((up>>n)&1), ctx.MVClassNCDF[comp][n][:])
	}
	ec.SymbolAdaptDav1d(uint32(fp), ctx.MVClassNFPCDF[comp][:], 3)
	if highPrecision {
		ec.BoolAdapt(uint32(hp), ctx.MVClassNHPCDF[comp][:])
	}
}
