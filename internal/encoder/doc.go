// Package encoder is the umbrella package for the AV1 encoder pipeline.
//
// Subpackages will follow the SVT-AV1 module layout:
//
//   - encoder/obuwriter   bitstream serialisation
//   - encoder/me          motion estimation
//   - encoder/md          mode decision
//   - encoder/tx          forward transform / quantisation
//   - encoder/entropy     symbol coder + CDF update
//   - encoder/loop        encoder-side deblock / CDEF / LR optimisation
//   - encoder/ratecontrol VBR / CBR / CRF rate control
//   - encoder/tpl         temporal dependency model
//
// Reference implementation: SVT-AV1/Source/Lib/Codec.
//
// Milestone: M10 onwards. The package exists at M0 only to reserve the
// import path.
package encoder
