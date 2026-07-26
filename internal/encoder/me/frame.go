package me

import "errors"

// FrameConfig describes a block-grid motion analysis between two luma frames.
type FrameConfig struct {
	Source          []byte
	Reference       []byte
	SourceStride    int
	ReferenceStride int
	Width           int
	Height          int
	BlockSize       int
	SearchRange     int
}

// BlockResult records the visible block geometry and its best motion result.
type BlockResult struct {
	X      int
	Y      int
	Width  int
	Height int
	Result Result
}

// FrameResult is a raster-ordered motion field. ZeroSAD is the distortion
// without motion compensation and TotalSAD is the distortion after search.
type FrameResult struct {
	Blocks   []BlockResult
	ZeroSAD  uint64
	TotalSAD uint64
}

// AnalyzeFrame searches an independently translational motion vector for each
// block. Blocks at the right and bottom edges are clipped to the visible frame.
func AnalyzeFrame(cfg FrameConfig) (FrameResult, error) {
	if cfg.BlockSize <= 0 {
		return FrameResult{}, errors.New("me: invalid frame-analysis block size")
	}
	base := Config{
		Source:          cfg.Source,
		Reference:       cfg.Reference,
		SourceStride:    cfg.SourceStride,
		ReferenceStride: cfg.ReferenceStride,
		Width:           cfg.Width,
		Height:          cfg.Height,
		BlockWidth:      min(cfg.BlockSize, cfg.Width),
		BlockHeight:     min(cfg.BlockSize, cfg.Height),
		SearchRange:     cfg.SearchRange,
	}
	if err := validate(base); err != nil {
		return FrameResult{}, err
	}

	blocksWide := (cfg.Width + cfg.BlockSize - 1) / cfg.BlockSize
	blocksHigh := (cfg.Height + cfg.BlockSize - 1) / cfg.BlockSize
	out := FrameResult{
		Blocks: make([]BlockResult, 0, blocksWide*blocksHigh),
	}
	for y := 0; y < cfg.Height; y += cfg.BlockSize {
		for x := 0; x < cfg.Width; x += cfg.BlockSize {
			block := base
			block.X = x
			block.Y = y
			block.BlockWidth = min(cfg.BlockSize, cfg.Width-x)
			block.BlockHeight = min(cfg.BlockSize, cfg.Height-y)
			result, err := Search(block)
			if err != nil {
				return FrameResult{}, err
			}
			out.ZeroSAD += blockSAD(block, MV{})
			out.TotalSAD += result.SAD
			out.Blocks = append(out.Blocks, BlockResult{
				X: x, Y: y,
				Width: block.BlockWidth, Height: block.BlockHeight,
				Result: result,
			})
		}
	}
	return out, nil
}
