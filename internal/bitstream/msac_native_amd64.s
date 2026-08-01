//go:build amd64 && !purego

#include "textflag.h"

#define CDF_INC(off) \
	MOVWLZX off(SI), AX; \
	MOVL $32768, DX; \
	SUBL AX, DX; \
	SHRL CX, DX; \
	ADDL DX, AX; \
	MOVW AX, off(SI)

#define CDF_DEC(off) \
	MOVWLZX off(SI), AX; \
	MOVL AX, DX; \
	SHRL CX, DX; \
	SUBL DX, AX; \
	MOVW AX, off(SI)

// Fixed three-symbol adaptive decode (nSymbols=2).
TEXT ·symbolAdapt2AMD64(SB), NOSPLIT, $0-24
	MOVQ m+0(FP), DI
	MOVQ cdf+8(FP), SI
	MOVQ 32(DI), R8
	MOVL 40(DI), R9
	MOVQ R8, R10
	SHRQ $48, R10
	MOVL R9, R11
	SHRL $8, R11
	MOVL R9, R12

	MOVWLZX 0(SI), R13
	SHRL $6, R13
	IMULL R11, R13
	SHRL $1, R13
	ADDL $8, R13
	MOVL R10, R14
	SUBL R13, R14
	JGE selected2_0
	MOVL R13, R12

	MOVWLZX 2(SI), R13
	SHRL $6, R13
	IMULL R11, R13
	SHRL $1, R13
	ADDL $4, R13
	MOVL R10, R14
	SUBL R13, R14
	JGE selected2_1
	MOVL R13, R12
	XORL R13, R13
	MOVL $2, AX
	JMP selected2

selected2_0:
	XORL AX, AX
	JMP selected2
selected2_1:
	MOVL $1, AX

selected2:
	MOVL AX, val+16(FP)
	MOVL R13, R14
	SHLQ $48, R14
	SUBQ R14, R8
	SUBL R13, R12
	BSRL R12, CX
	MOVL $15, R15
	SUBL CX, R15
	MOVL R15, CX
	SHLQ CX, R8
	SHLL CX, R12
	MOVQ R8, 32(DI)
	MOVL R12, 40(DI)
	MOVQ 48(DI), R14
	SUBQ R15, R14
	MOVQ R14, 48(DI)
	MOVL R15, shift+20(FP)

	CMPB 56(DI), $0
	JE done2
	MOVWLZX 4(SI), R10
	MOVL R10, CX
	SHRL $4, CX
	ADDL $4, CX
	MOVL val+16(FP), R9
	CMPL R9, $0
	JE update2_0
	CMPL R9, $1
	JE update2_1
	CDF_INC(0)
	CDF_INC(2)
	JMP updateCount2
update2_1:
	CDF_INC(0)
	CDF_DEC(2)
	JMP updateCount2
update2_0:
	CDF_DEC(0)
	CDF_DEC(2)
updateCount2:
	MOVL R10, R14
	SUBL $32, R14
	JGE done2
	INCL R10
	MOVW R10, 4(SI)
done2:
	RET

// MSAC amd64 layout: dif=32, rng=40, cnt=48, allowUpdateCDF=56.
// The fixed four-symbol kernel returns the normalization shift so Go can
// execute the uncommon refill without duplicating slice-boundary handling.
TEXT ·symbolAdapt3AMD64(SB), NOSPLIT, $0-24
	MOVQ m+0(FP), DI
	MOVQ cdf+8(FP), SI
	MOVQ 32(DI), R8
	MOVL 40(DI), R9
	MOVQ R8, R10
	SHRQ $48, R10
	MOVL R9, R11
	SHRL $8, R11
	MOVL R9, R12

	// Threshold for symbol 0.
	MOVWLZX 0(SI), R13
	SHRL $6, R13
	IMULL R11, R13
	SHRL $1, R13
	ADDL $12, R13
	MOVL R10, R14
	SUBL R13, R14
	JGE selected0
	MOVL R13, R12

	// Threshold for symbol 1.
	MOVWLZX 2(SI), R13
	SHRL $6, R13
	IMULL R11, R13
	SHRL $1, R13
	ADDL $8, R13
	MOVL R10, R14
	SUBL R13, R14
	JGE selected1
	MOVL R13, R12

	// Threshold for symbol 2.
	MOVWLZX 4(SI), R13
	SHRL $6, R13
	IMULL R11, R13
	SHRL $1, R13
	ADDL $4, R13
	MOVL R10, R14
	SUBL R13, R14
	JGE selected2
	MOVL R13, R12
	XORL R13, R13
	MOVL $3, AX
	JMP selected

selected0:
	XORL AX, AX
	JMP selected
selected1:
	MOVL $1, AX
	JMP selected
selected2:
	MOVL $2, AX

selected:
	MOVL AX, val+16(FP)
	MOVL R13, R14
	SHLQ $48, R14
	SUBQ R14, R8
	SUBL R13, R12
	BSRL R12, CX
	MOVL $15, R15
	SUBL CX, R15
	MOVL R15, CX
	SHLQ CX, R8
	SHLL CX, R12
	MOVQ R8, 32(DI)
	MOVL R12, 40(DI)
	MOVQ 48(DI), R14
	SUBQ R15, R14
	MOVQ R14, 48(DI)
	MOVL R15, shift+20(FP)

	CMPB 56(DI), $0
	JE done
	MOVWLZX 6(SI), R10
	MOVL R10, CX
	SHRL $4, CX
	ADDL $5, CX
	MOVL val+16(FP), R9
	CMPL R9, $0
	JE update0
	CMPL R9, $1
	JE update1
	CMPL R9, $2
	JE update2
	CDF_INC(0)
	CDF_INC(2)
	CDF_INC(4)
	JMP updateCount
update2:
	CDF_INC(0)
	CDF_INC(2)
	CDF_DEC(4)
	JMP updateCount
update1:
	CDF_INC(0)
	CDF_DEC(2)
	CDF_DEC(4)
	JMP updateCount
update0:
	CDF_DEC(0)
	CDF_DEC(2)
	CDF_DEC(4)
updateCount:
	MOVL R10, R14
	SUBL $32, R14
	JGE done
	INCL R10
	MOVW R10, 6(SI)
done:
	RET
