package obu

import (
	"errors"
	"fmt"

	"github.com/zesun96/go-av1/internal/bitstream"
	"github.com/zesun96/go-av1/internal/header"
)

// Errors returned by the OBU parsers.
var (
	// ErrShortBuffer is returned when the input runs out before a complete
	// OBU header could be read.
	ErrShortBuffer = errors.New("obu: buffer too short")
	// ErrForbiddenBit is returned in strict mode when obu_forbidden_bit
	// is set; the spec mandates it to be zero.
	ErrForbiddenBit = errors.New("obu: forbidden bit set")
	// ErrReservedBit is returned in strict mode when an OBU header
	// reserved bit is non-zero.
	ErrReservedBit = errors.New("obu: reserved bit set")
	// ErrLebOverflow is returned when a leb128 length field cannot be
	// represented in 32 bits.
	ErrLebOverflow = errors.New("obu: leb128 overflow")
	// ErrSizeOverflow is returned when a leb128-encoded OBU size points
	// past the end of the supplied buffer.
	ErrSizeOverflow = errors.New("obu: payload size exceeds buffer")
)

// ParseOptions tunes how strictly the parser rejects malformed input.
type ParseOptions struct {
	// StrictStdCompliance mirrors dav1d's strict_std_compliance flag. When
	// true, forbidden / reserved bits must be zero or the parser fails.
	StrictStdCompliance bool
}

// OBU describes one parsed Open Bitstream Unit, including the offsets within
// the containing buffer and a slice over the payload bytes.
//
// Payload is a sub-slice of the input passed to SplitOBUs and shares its
// backing array; copy it if you need to retain the bytes past the input's
// lifetime.
type OBU struct {
	// Header is the decoded OBU header fields.
	Header header.OBUHeader
	// Offset is the byte offset of the first header byte in the input
	// stream.
	Offset int
	// Total is the number of input bytes consumed by this OBU, header +
	// optional leb128 size + payload.
	Total int
	// Payload references the OBU's payload bytes. When HasSize is false
	// the payload extends to the end of the supplied buffer.
	Payload []byte
}

// ParseOBUHeader decodes a single OBU header (1 or 2 bytes) from buf and
// returns the parsed header along with the number of header bytes consumed.
//
// It does NOT consume the optional leb128 size field; use ReadOBU to parse a
// header followed by its size + payload.
func ParseOBUHeader(buf []byte, opts ParseOptions) (header.OBUHeader, int, error) {
	if len(buf) == 0 {
		return header.OBUHeader{}, 0, ErrShortBuffer
	}
	gb := bitstream.NewGetBits(buf)
	forbidden := gb.Bit()
	if opts.StrictStdCompliance && forbidden != 0 {
		return header.OBUHeader{}, 0, ErrForbiddenBit
	}
	t := header.OBUType(gb.F(4))
	hasExt := gb.Bit() != 0
	hasSize := gb.Bit() != 0
	reserved := gb.Bit()
	if opts.StrictStdCompliance && reserved != 0 {
		return header.OBUHeader{}, 0, ErrReservedBit
	}
	h := header.OBUHeader{
		Type:         t,
		HasExtension: hasExt,
		HasSize:      hasSize,
		HeaderSize:   1,
	}
	if hasExt {
		if len(buf) < 2 {
			return header.OBUHeader{}, 0, ErrShortBuffer
		}
		h.TemporalID = uint8(gb.F(3))
		h.SpatialID = uint8(gb.F(2))
		extReserved := gb.F(3)
		if opts.StrictStdCompliance && extReserved != 0 {
			return header.OBUHeader{}, 0, ErrReservedBit
		}
		h.HeaderSize = 2
	}
	// gb.Err() is unreachable here: the only fields read are 1 byte
	// (header) plus optionally a second byte gated by the len(buf) < 2
	// check above; both paths stay strictly within the buffer.
	return h, h.HeaderSize, nil
}

// ReadOBU parses one OBU starting at the beginning of buf. It returns the
// decoded OBU together with the number of bytes consumed. When the OBU
// header signals no length field, the OBU's payload extends to the end of
// buf, mirroring dav1d's behaviour for the final OBU of a temporal unit.
func ReadOBU(buf []byte, opts ParseOptions) (OBU, error) {
	h, hdrSize, err := ParseOBUHeader(buf, opts)
	if err != nil {
		return OBU{}, err
	}
	rest := buf[hdrSize:]
	var payloadStart, payloadLen int
	if h.HasSize {
		size, n, err := readLeb128(rest)
		if err != nil {
			return OBU{}, err
		}
		if size > uint64(len(rest)-n) {
			return OBU{}, ErrSizeOverflow
		}
		payloadStart = hdrSize + n
		payloadLen = int(size)
	} else {
		payloadStart = hdrSize
		payloadLen = len(rest)
	}
	return OBU{
		Header:  h,
		Offset:  0,
		Total:   payloadStart + payloadLen,
		Payload: buf[payloadStart : payloadStart+payloadLen],
	}, nil
}

// SplitOBUs walks buf and returns every OBU it contains. It is intended for
// inputs that pack multiple OBUs back-to-back (the typical container layout,
// AV1 Annex B sub-stream excluded).
//
// Every OBU in buf MUST advertise its size via obu_has_size_field. dav1d's
// dav1d_parse_obus runs once per input buffer and lets the caller advance
// the slice; here we surface the multi-OBU iteration as a single call, so we
// need an explicit size for every unit to know where the next one starts.
func SplitOBUs(buf []byte, opts ParseOptions) ([]OBU, error) {
	var out []OBU
	for off := 0; off < len(buf); {
		o, err := ReadOBU(buf[off:], opts)
		if err != nil {
			return out, fmt.Errorf("obu at offset %d: %w", off, err)
		}
		if !o.Header.HasSize {
			return out, fmt.Errorf("obu at offset %d: missing size field, cannot split multi-OBU stream", off)
		}
		o.Offset = off
		// o.Total is always >= 1 (header is at least one byte), so the
		// loop is guaranteed to make progress.
		out = append(out, o)
		off += o.Total
	}
	return out, nil
}

// readLeb128 decodes a leb128-encoded length from the start of buf. It
// returns the decoded value, the number of consumed bytes, and an error if
// the encoding is invalid or overflows 32 bits.
//
// Mirrors AV1 5.9.29 leb128() and dav1d_get_uleb128.
func readLeb128(buf []byte) (uint64, int, error) {
	var v uint64
	for i := 0; i < 8; i++ {
		if i >= len(buf) {
			return 0, 0, ErrShortBuffer
		}
		b := buf[i]
		v |= uint64(b&0x7F) << uint(7*i)
		if b&0x80 == 0 {
			if v > 0xFFFFFFFF {
				return 0, 0, ErrLebOverflow
			}
			return v, i + 1, nil
		}
	}
	return 0, 0, ErrLebOverflow
}
