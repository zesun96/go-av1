//go:build amd64 && !purego

#include "textflag.h"

// addDC8SSE2 applies a signed constant with unsigned byte saturation.
TEXT ·addDC8SSE2(SB), NOSPLIT, $0-40
	MOVQ dst+0(FP), DI
	MOVQ stride+8(FP), R8
	MOVQ w+16(FP), R9
	MOVQ h+24(FP), R10
	MOVQ dc+32(FP), AX

	TESTQ AX, AX
	JL negative
	MOVD AX, X0
	PUNPCKLBW X0, X0
	PUNPCKLWL X0, X0
	PSHUFL $0, X0, X0

positiveRow:
	XORQ CX, CX
positive16:
	LEAQ 16(CX), DX
	CMPQ DX, R9
	JG positive8
	MOVOU (DI)(CX*1), X1
	PADDUSB X0, X1
	MOVOU X1, (DI)(CX*1)
	ADDQ $16, CX
	JMP positive16
positive8:
	CMPQ CX, R9
	JGE positiveNext
	MOVQ (DI)(CX*1), X1
	PADDUSB X0, X1
	MOVQ X1, (DI)(CX*1)
positiveNext:
	ADDQ R8, DI
	DECQ R10
	JNZ positiveRow
	RET

negative:
	NEGQ AX
	MOVD AX, X0
	PUNPCKLBW X0, X0
	PUNPCKLWL X0, X0
	PSHUFL $0, X0, X0

negativeRow:
	XORQ CX, CX
negative16:
	LEAQ 16(CX), DX
	CMPQ DX, R9
	JG negative8
	MOVOU (DI)(CX*1), X1
	PSUBUSB X0, X1
	MOVOU X1, (DI)(CX*1)
	ADDQ $16, CX
	JMP negative16
negative8:
	CMPQ CX, R9
	JGE negativeNext
	MOVQ (DI)(CX*1), X1
	PSUBUSB X0, X1
	MOVQ X1, (DI)(CX*1)
negativeNext:
	ADDQ R8, DI
	DECQ R10
	JNZ negativeRow
	RET

DATA ·residualRound8<>+0(SB)/8, $0x0000000800000008
DATA ·residualRound8<>+8(SB)/8, $0x0000000800000008
GLOBL ·residualRound8<>(SB), RODATA|NOPTR, $16

// addResidual8SSE41 rounds Q4 residuals and saturating-adds eight pixels.
TEXT ·addResidual8SSE41(SB), NOSPLIT, $0-40
	MOVQ dst+0(FP), DI
	MOVQ stride+8(FP), R8
	MOVQ src+16(FP), SI
	MOVQ w+24(FP), R9
	MOVQ h+32(FP), R10
	MOVO ·residualRound8<>(SB), X7

residualRow:
	XORQ CX, CX
residualCol:
	MOVOU (SI)(CX*4), X0
	MOVOU 16(SI)(CX*4), X1
	PADDL X7, X0
	PADDL X7, X1
	PSRAL $4, X0
	PSRAL $4, X1
	PACKSSLW X1, X0

	MOVQ (DI)(CX*1), X2
	PMOVZXBW X2, X2
	PADDW X0, X2
	PACKUSWB X2, X2
	MOVQ X2, (DI)(CX*1)

	ADDQ $8, CX
	CMPQ CX, R9
	JL residualCol
	LEAQ (SI)(R9*4), SI
	ADDQ R8, DI
	DECQ R10
	JNZ residualRow
	RET
