// Package me implements motion estimation for the AV1 encoder.
package me

import (
	"errors"
)

// MV is a motion vector in AV1's 1/8-pixel units. Positive components point
// right/down in the reference frame.
type MV struct {
	X int
	Y int
}

// Config describes one luma-block motion search.
type Config struct {
	Source          []byte
	Reference       []byte
	SourceStride    int
	ReferenceStride int
	Width           int
	Height          int
	X               int
	Y               int
	BlockWidth      int
	BlockHeight     int
	SearchRange     int // full pixels in each direction
}

// Result is the best motion vector and its sum of absolute differences.
type Result struct {
	MV  MV
	SAD uint64
}

// Search performs a full-pixel search followed by half-, quarter-, and
// eighth-pixel refinement using bilinear interpolation. Full-pixel search is
// exhaustive inside SearchRange so it can serve as the correctness baseline
// for later hierarchical and SIMD implementations.
func Search(cfg Config) (Result, error) {
	if err := validate(cfg); err != nil {
		return Result{}, err
	}
	best := Result{SAD: ^uint64(0)}
	for dy := -cfg.SearchRange; dy <= cfg.SearchRange; dy++ {
		for dx := -cfg.SearchRange; dx <= cfg.SearchRange; dx++ {
			mv := MV{X: dx * 8, Y: dy * 8}
			best = chooseBetter(best, Result{MV: mv, SAD: blockSAD(cfg, mv)})
		}
	}
	for _, step := range []int{4, 2, 1} {
		center := best.MV
		for oy := -step; oy <= step; oy += step {
			for ox := -step; ox <= step; ox += step {
				mv := MV{X: center.X + ox, Y: center.Y + oy}
				limit := cfg.SearchRange*8 + 7
				if mv.X < -limit || mv.X > limit || mv.Y < -limit || mv.Y > limit {
					continue
				}
				best = chooseBetter(best, Result{MV: mv, SAD: blockSAD(cfg, mv)})
			}
		}
	}
	return best, nil
}

func validate(cfg Config) error {
	switch {
	case cfg.Width <= 0 || cfg.Height <= 0:
		return errors.New("me: invalid frame dimensions")
	case cfg.SourceStride < cfg.Width || cfg.ReferenceStride < cfg.Width:
		return errors.New("me: stride is smaller than width")
	case len(cfg.Source) < cfg.SourceStride*cfg.Height ||
		len(cfg.Reference) < cfg.ReferenceStride*cfg.Height:
		return errors.New("me: frame buffer is too small")
	case cfg.BlockWidth <= 0 || cfg.BlockHeight <= 0:
		return errors.New("me: invalid block dimensions")
	case cfg.X < 0 || cfg.Y < 0 ||
		cfg.X+cfg.BlockWidth > cfg.Width || cfg.Y+cfg.BlockHeight > cfg.Height:
		return errors.New("me: source block is outside the frame")
	case cfg.SearchRange < 0:
		return errors.New("me: negative search range")
	default:
		return nil
	}
}

func blockSAD(cfg Config, mv MV) uint64 {
	var sad uint64
	for y := 0; y < cfg.BlockHeight; y++ {
		srcOff := (cfg.Y+y)*cfg.SourceStride + cfg.X
		refY8 := (cfg.Y+y)*8 + mv.Y
		for x := 0; x < cfg.BlockWidth; x++ {
			refX8 := (cfg.X+x)*8 + mv.X
			pred := sampleBilinear(cfg.Reference, cfg.ReferenceStride,
				cfg.Width, cfg.Height, refX8, refY8)
			diff := int(cfg.Source[srcOff+x]) - pred
			if diff < 0 {
				diff = -diff
			}
			sad += uint64(diff)
		}
	}
	return sad
}

func sampleBilinear(ref []byte, stride, width, height, x8, y8 int) int {
	x0 := x8 >> 3
	y0 := y8 >> 3
	fx := x8 - x0*8
	fy := y8 - y0*8
	x1, y1 := x0+1, y0+1
	x0 = clamp(x0, 0, width-1)
	x1 = clamp(x1, 0, width-1)
	y0 = clamp(y0, 0, height-1)
	y1 = clamp(y1, 0, height-1)
	p00 := int(ref[y0*stride+x0])
	p01 := int(ref[y0*stride+x1])
	p10 := int(ref[y1*stride+x0])
	p11 := int(ref[y1*stride+x1])
	top := p00*(8-fx) + p01*fx
	bottom := p10*(8-fx) + p11*fx
	return (top*(8-fy) + bottom*fy + 32) >> 6
}

func chooseBetter(current, candidate Result) Result {
	if candidate.SAD < current.SAD {
		return candidate
	}
	if candidate.SAD > current.SAD {
		return current
	}
	costCurrent := abs(current.MV.X) + abs(current.MV.Y)
	costCandidate := abs(candidate.MV.X) + abs(candidate.MV.Y)
	if costCandidate < costCurrent ||
		(costCandidate == costCurrent && (candidate.MV.Y < current.MV.Y ||
			(candidate.MV.Y == current.MV.Y && candidate.MV.X < current.MV.X))) {
		return candidate
	}
	return current
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
