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
