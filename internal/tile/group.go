// group.go provides the top-level tile-group decode entry point used by the
// pkg/av1 decoder pipeline.
package tile

import (
	"github.com/zesun96/go-av1/internal/header"
)

// DecodeTileGroup parses a tile_group_obu() payload (or the tile portion of
// an OBU_FRAME) and reconstructs all tiles into fb.
//
// logf, if non-nil, receives diagnostic messages (tile boundaries, parse
// errors). Pass nil to suppress all logging.
//
// Errors in individual tiles are logged and skipped so that the caller always
// receives a (possibly partial) picture rather than a hard failure.
func DecodeTileGroup(
	payload []byte,
	fhdr *header.FrameHeader,
	seq *header.SequenceHeader,
	fb *FrameBuf,
	logf func(string, ...any),
) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	logf("tile: DecodeTileGroup payloadLen=%d numTiles=%d×%d NBytes=%d",
		len(payload), fhdr.Tiling.Cols, fhdr.Tiling.Rows, fhdr.Tiling.NBytes)
	tiles, err := ParseTileGroup(payload, fhdr)
	if err != nil {
		logf("tile: ParseTileGroup: %v (parsed %d tiles so far)", err, len(tiles))
	}
	logf("tile: ParseTileGroup → %d tiles", len(tiles))

	// Allocate frame-level neighbour state (shared across tiles in raster order).
	fs := NewFrameState(fb.Width, fb.Height)

	for _, td := range tiles {
		if err2 := DecodeTile(td, fhdr, seq, fb, fs, logf); err2 != nil {
			logf("tile: DecodeTile row=%d col=%d: %v", td.Row, td.Col, err2)
		}
	}
	return nil
}
