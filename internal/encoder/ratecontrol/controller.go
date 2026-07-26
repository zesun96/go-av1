// Package ratecontrol implements the encoder's frame-level quantizer control.
package ratecontrol

import (
	"errors"
	"math"
)

// Mode selects the frame-level rate-control strategy.
type Mode uint8

const (
	ModeCRF Mode = iota
	ModeCQP
	ModeVBR
	ModeCBR
)

// Config configures a Controller.
type Config struct {
	Mode          Mode
	TargetKbps    int
	FrameRateNum  int
	FrameRateDen  int
	InitialQIndex int
}

// Controller adjusts qindex using measured packet sizes.
type Controller struct {
	mode       Mode
	qindex     int
	targetBits float64
	totalBits  float64
	frames     int
	bufferBits float64
	bufferCap  float64
}

// New constructs a frame-level controller.
func New(cfg Config) (*Controller, error) {
	if cfg.FrameRateNum <= 0 || cfg.FrameRateDen <= 0 {
		return nil, errors.New("ratecontrol: invalid frame rate")
	}
	if cfg.Mode > ModeCBR {
		return nil, errors.New("ratecontrol: invalid mode")
	}
	if (cfg.Mode == ModeVBR || cfg.Mode == ModeCBR) && cfg.TargetKbps <= 0 {
		return nil, errors.New("ratecontrol: target bitrate is required")
	}
	fps := float64(cfg.FrameRateNum) / float64(cfg.FrameRateDen)
	targetBits := 0.0
	if cfg.TargetKbps > 0 {
		targetBits = float64(cfg.TargetKbps*1000) / fps
	}
	return &Controller{
		mode:       cfg.Mode,
		qindex:     clamp(cfg.InitialQIndex, 1, 255),
		targetBits: targetBits,
		bufferCap:  math.Max(targetBits*fps, 1), // one-second virtual buffer
	}, nil
}

// QIndex returns the quantizer to use for the next frame.
func (c *Controller) QIndex() int {
	if c == nil {
		return 1
	}
	return c.qindex
}

// Update feeds the encoded size of one frame back into the controller.
func (c *Controller) Update(bits int, keyframe bool) {
	if c == nil || c.mode == ModeCRF || c.mode == ModeCQP || c.targetBits <= 0 {
		return
	}
	actual := math.Max(float64(bits), 1)
	budget := c.targetBits
	if keyframe {
		budget *= 4
	}
	c.totalBits += actual
	c.frames++

	var delta float64
	switch c.mode {
	case ModeVBR:
		instant := math.Log2(actual / budget)
		average := math.Log2(c.totalBits / (float64(c.frames) * c.targetBits))
		delta = 4*instant + 5*average
	case ModeCBR:
		c.bufferBits = clampFloat(c.bufferBits+actual-c.targetBits, -c.bufferCap, c.bufferCap)
		fullness := c.bufferBits / c.bufferCap
		delta = 5*math.Log2(actual/budget) + 18*fullness
	}
	step := clamp(int(math.Round(delta)), -12, 12)
	c.qindex = clamp(c.qindex+step, 1, 255)
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

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
