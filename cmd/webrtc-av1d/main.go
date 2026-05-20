// Command webrtc-av1d is a WebRTC server that receives AV1 video from a
// browser, saves the raw OBU data as an IVF file, and attempts to decode
// each frame with the go-av1 decoder pipeline.
//
// Usage:
//
//	webrtc-av1d [-port 8080] [-out output.ivf]
//
// The server exposes two HTTP endpoints:
//
//	GET  /        → serves static/index.html (browser frontend)
//	POST /offer   → WebRTC SDP signalling (JSON body {sdp, type})
package main

import (
	"embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/zesun96/go-av1/pkg/av1"
)

//go:embed static/index.html
var staticFS embed.FS

// ─── flags ───────────────────────────────────────────────────────────────────

var (
	flagPort = flag.Int("port", 8080, "HTTP listen port")
	flagOut  = flag.String("out", "output.ivf", "output IVF file path")
	flagYUV  = flag.String("yuv", "", "if non-empty, save decoded frames as raw YUV420 to this file (playable with ffplay -f rawvideo -pixel_format yuv420p -video_size WxH)")
)

// ─── main ────────────────────────────────────────────────────────────────────

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Printf("webrtc-av1d starting (go-av1 %s, pion/webrtc v4)", av1.Version)

	// Prepare IVF writer (lazy-init on first frame so we know the dimensions).
	ivfw := &ivfWriter{path: *flagOut}

	// Prepare YUV writer (optional).
	var yuv *yuvWriter
	if *flagYUV != "" {
		yuv = &yuvWriter{path: *flagYUV}
		log.Printf("YUV output will be written to: %s", *flagYUV)
	}

	// Prepare go-av1 decoder.
	dec, err := av1.NewDecoder(av1.DecoderOptions{Threads: runtime.NumCPU()})
	if err != nil {
		log.Fatalf("decoder init: %v", err)
	}
	defer dec.Close()

	// Global state shared between requests.
	srv := &server{
		dec:  dec,
		ivfw: ivfw,
		yuv:  yuv,
	}

	http.HandleFunc("/", srv.handleIndex)
	http.HandleFunc("/offer", srv.handleOffer)

	addr := fmt.Sprintf(":%d", *flagPort)
	log.Printf("listening on http://localhost%s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("http: %v", err)
	}
}

// ─── server ──────────────────────────────────────────────────────────────────

type server struct {
	dec  av1.Decoder
	ivfw *ivfWriter
	yuv  *yuvWriter

	mu         sync.Mutex
	frameCount int64
}

// handleIndex serves the single-page frontend from static/index.html.
//
// Resolution order:
//  1. ./static/index.html relative to the current working directory
//  2. ./static/index.html relative to the running executable's directory
//  3. ./static/index.html relative to this source file's directory
//     (works with `go run` where the binary lives in a temp dir)
//  4. embedded copy from staticFS (always available)
func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	for _, dir := range candidateStaticDirs() {
		p := filepath.Join(dir, "static", "index.html")
		if _, err := os.Stat(p); err == nil {
			http.ServeFile(w, r, p)
			return
		}
	}
	// Fallback: serve embedded copy.
	data, err := fs.ReadFile(staticFS, "static/index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data) //nolint:errcheck
}

// candidateStaticDirs returns directories where static/index.html may live.
func candidateStaticDirs() []string {
	var dirs []string
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, cwd)
	}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		dirs = append(dirs, filepath.Dir(file))
	}
	return dirs
}

// sdpMessage is the JSON body exchanged with the browser.
type sdpMessage struct {
	SDP  string `json:"sdp"`
	Type string `json:"type"`
}

// handleOffer processes a WebRTC offer and returns an answer.
func (s *server) handleOffer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	var offer sdpMessage
	if err := json.NewDecoder(r.Body).Decode(&offer); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Build a MediaEngine with only AV1.
	me := &webrtc.MediaEngine{}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeAV1,
			ClockRate:   90000,
			Channels:    0,
			SDPFmtpLine: "",
		},
		PayloadType: 96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		http.Error(w, "register codec: "+err.Error(), http.StatusInternalServerError)
		return
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(me))
	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	})
	if err != nil {
		http.Error(w, "new peer connection: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Register track handler.
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		log.Printf("track received: codec=%s ssrc=%d", track.Codec().MimeType, track.SSRC())
		s.consumeAV1Track(track)
	})

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("ICE state: %s", state)
		if state == webrtc.ICEConnectionStateFailed || state == webrtc.ICEConnectionStateClosed {
			pc.Close() //nolint:errcheck
		}
	})

	// Set remote description.
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offer.SDP,
	}); err != nil {
		http.Error(w, "set remote: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Create and set local description.
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		http.Error(w, "create answer: "+err.Error(), http.StatusInternalServerError)
		return
	}
	gatherDone := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		http.Error(w, "set local: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Wait for ICE gathering to complete (trickle=false).
	select {
	case <-gatherDone:
	case <-time.After(10 * time.Second):
		log.Println("ICE gathering timeout")
	}

	local := pc.LocalDescription()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sdpMessage{ //nolint:errcheck
		SDP:  local.SDP,
		Type: local.Type.String(),
	})
	log.Println("answer sent to browser")
}

// ─── AV1 track consumer ──────────────────────────────────────────────────────

// consumeAV1Track reads RTP packets from track, reassembles fragmented AV1
// OBUs per RFC 9321, ensures every OBU carries a size field, writes the
// resulting temporal units to the IVF file, and feeds them to the decoder.
func (s *server) consumeAV1Track(track *webrtc.TrackRemote) {
	var (
		tuBuf      []byte // complete OBUs for the current temporal unit
		fragBuf    []byte // ongoing fragmented OBU (header from first fragment + payload chunks)
		inFragment bool   // true while reassembling a fragmented OBU (Y=1)
		pts        uint64
		ptsBase    uint64 // first RTP timestamp (for PTS normalization)
		ptsBaseSet bool
	)

	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("rtp read: %v", err)
			}
			break
		}

		payload := pkt.Payload
		if len(payload) < 1 {
			continue
		}

		aggHdr := payload[0]
		payload = payload[1:]

		zBit := aggHdr&0x80 != 0 // Z: first OBU continues from previous packet
		yBit := aggHdr&0x40 != 0 // Y: last OBU continues in next packet
		nBit := aggHdr&0x08 != 0 // N: new temporal unit

		// RFC 9321 §4.3: W is the number of OBU elements when non-zero.
		// W=0 is a special "legacy" mode: elements are delimited by LEB128
		// size prefixes and we parse until the payload is exhausted.
		// W=1,2,3: exactly W elements; all but the last have a size prefix.
		w := int((aggHdr >> 4) & 0x03)

		// New temporal unit: flush previous TU.
		if nBit && len(tuBuf) > 0 {
			s.processTemporalUnit(tuBuf, pts)
			tuBuf = tuBuf[:0]
		}

		log.Printf("rtp aggHdr=0x%02x Z=%v Y=%v N=%v W=%d marker=%v len=%d",
			aggHdr, zBit, yBit, nBit, w, pkt.Marker, len(payload))

		// Parse each OBU element in the packet.
		// When w==0 (legacy mode) we loop until the payload is exhausted,
		// reading a LEB128 size before every element.
		for i := 0; len(payload) > 0; i++ {
			isFirst := i == 0
			// In fixed-count mode (w>0) the last element has no size prefix;
			// in legacy mode (w==0) every element has one.
			isLast := (w > 0) && (i == w-1)

			var elemData []byte
			if isLast {
				// Last element: consume the rest of the payload.
				elemData = payload
				payload = nil
			} else if w > 0 && i < w-1 {
				// Non-last element in fixed-count mode: read LEB128 size.
				sz, n := leb128(payload)
				if n == 0 || int(sz) > len(payload)-n {
					elemData = payload
					payload = nil
				} else {
					elemData = payload[n : n+int(sz)]
					payload = payload[n+int(sz):]
				}
			} else {
				// Legacy mode (w==0): every element preceded by LEB128 size.
				sz, n := leb128(payload)
				if n == 0 || int(sz) > len(payload)-n {
					elemData = payload
					payload = nil
				} else {
					elemData = payload[n : n+int(sz)]
					payload = payload[n+int(sz):]
				}
			}
			if len(elemData) == 0 {
				if w > 0 && i >= w-1 {
					break
				}
				continue
			}

			// --- Handle fragment continuation (Z=1 on first element) ---
			if isFirst && zBit && inFragment {
				fragBuf = append(fragBuf, elemData...)
				if !(isLast && yBit) {
					// Fragment complete – add size field and flush to TU.
					tuBuf = append(tuBuf, ensureOBUSizeField(fragBuf)...)
					fragBuf = nil
					inFragment = false
				}
				continue
			}

			// If a previous fragment was left open unexpectedly, flush it.
			if inFragment {
				tuBuf = append(tuBuf, ensureOBUSizeField(fragBuf)...)
				fragBuf = nil
				inFragment = false
			}

			// --- Handle new fragment start (Y=1 on last element) ---
			if isLast && yBit {
				fragBuf = append([]byte{}, elemData...)
				inFragment = true
				continue
			}

			// --- Complete OBU ---
			tuBuf = append(tuBuf, ensureOBUSizeField(elemData)...)
		}

		rtpTS := uint64(pkt.Timestamp)
		if !ptsBaseSet {
			ptsBase = rtpTS
			ptsBaseSet = true
		}
		pts = rtpTS - ptsBase // normalize to start from 0

		// Marker bit = end of temporal unit.
		if pkt.Marker {
			if inFragment {
				tuBuf = append(tuBuf, ensureOBUSizeField(fragBuf)...)
				fragBuf = nil
				inFragment = false
			}
			if len(tuBuf) > 0 {
				log.Printf("flushing TU: %d bytes, pts=%d", len(tuBuf), pts)
				s.processTemporalUnit(tuBuf, pts)
				tuBuf = tuBuf[:0]
			}
		}
	}

	// Flush remaining data.
	if inFragment && len(fragBuf) > 0 {
		tuBuf = append(tuBuf, ensureOBUSizeField(fragBuf)...)
	}
	if len(tuBuf) > 0 {
		s.processTemporalUnit(tuBuf, pts)
	}

	log.Printf("track ended, total frames decoded: %d", atomic.LoadInt64(&s.frameCount))
}

// processTemporalUnit writes one temporal unit to the IVF file and feeds it
// to the go-av1 decoder.
func (s *server) processTemporalUnit(payload []byte, pts uint64) {
	// Write to IVF.
	if err := s.ivfw.WriteFrame(payload, pts); err != nil {
		log.Printf("ivf write: %v", err)
	}

	// Feed to decoder.
	if err := s.dec.SendData(payload); err != nil {
		log.Printf("decoder send: %v", err)
		return
	}

	for {
		pic, err := s.dec.GetPicture()
		if errors.Is(err, av1.ErrAgain) {
			break
		}
		if err != nil {
			log.Printf("decoder get: %v", err)
			break
		}
		n := atomic.AddInt64(&s.frameCount, 1)

		// Quick sanity check: compute mean luma value of the first row.
		yMean := yuvMeanLuma(pic)
		log.Printf("frame %d decoded: %dx%d chroma=%s yMean=%.1f",
			n, pic.Width, pic.Height, pic.Chroma, yMean)

		// Save to YUV file if requested.
		if s.yuv != nil {
			if err := s.yuv.WriteFrame(pic); err != nil {
				log.Printf("yuv write: %v", err)
			}
		}

		pic.Release()
	}
}

// ─── AV1 RTP payload helpers (RFC 9321) ──────────────────────────────────────

// AV1 RTP aggregation header (1 byte):
//
//	bit 7: Z – first OBU continues from a previous RTP packet
//	bit 6: Y – last OBU will continue in the next RTP packet
//	bit 5-4: W – number of OBU elements (0 means 1 element)
//	bit 3: N – new temporal unit
//	bit 2-0: reserved

// ensureOBUSizeField ensures the OBU has obu_has_size_field=1 with a proper
// LEB128-encoded size.  In the RTP payload the size field may be absent
// (obu_has_size_field=0) because RTP framing already conveys the size, but an
// AV1 bitstream inside an IVF file requires every OBU to carry its own size.
func ensureOBUSizeField(obu []byte) []byte {
	if len(obu) == 0 {
		return obu
	}

	// OBU header byte (AV1 spec §5.2):
	//   bit 7: forbidden
	//   bits 6-3: obu_type
	//   bit 2: obu_extension_flag
	//   bit 1: obu_has_size_field
	//   bit 0: reserved
	hdr0 := obu[0]
	hasSize := hdr0&0x02 != 0 // bit 1: obu_has_size_field
	hasExt := hdr0&0x04 != 0  // bit 2: obu_extension_flag

	if hasSize {
		return obu // already has size field
	}

	hdrSize := 1
	if hasExt {
		hdrSize = 2
	}
	if len(obu) < hdrSize {
		return obu
	}

	// Set obu_has_size_field bit (bit 1).
	newHdr0 := hdr0 | 0x02

	// Payload size = total OBU length minus header bytes.
	payloadSize := uint64(len(obu) - hdrSize)

	var sizeBuf [8]byte
	sizeLen := encodeLEB128(sizeBuf[:], payloadSize)

	result := make([]byte, 0, hdrSize+sizeLen+int(payloadSize))
	result = append(result, newHdr0)
	if hasExt {
		result = append(result, obu[1])
	}
	result = append(result, sizeBuf[:sizeLen]...)
	result = append(result, obu[hdrSize:]...)
	return result
}

// encodeLEB128 writes v in LEB128 encoding into buf and returns bytes written.
func encodeLEB128(buf []byte, v uint64) int {
	n := 0
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		buf[n] = b
		n++
		if v == 0 {
			break
		}
	}
	return n
}

// leb128 reads a variable-length unsigned integer encoded in LEB128.
// Returns (value, bytesConsumed). bytesConsumed is 0 on error.
func leb128(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 8; i++ {
		v |= uint64(b[i]&0x7F) << (7 * uint(i))
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

// ─── IVF writer ──────────────────────────────────────────────────────────────

// ivfWriter writes a minimal IVF container (AV01 fourcc, little-endian).
// The file header is written lazily on the first frame because we don't know
// the width/height until we receive data.  We use placeholder 0/0 since the
// IVF muxer header is informational only.
//
// Time base: we write num=1, den=30 (30 fps) and use a monotonically
// increasing frame counter as PTS so that ffplay computes the correct
// frame duration (1/30 s) and plays the video at the right speed.
type ivfWriter struct {
	path     string
	mu       sync.Mutex
	f        *os.File
	frameSeq uint64
}

const (
	ivfFileHeaderSize  = 32
	ivfFrameHeaderSize = 12
)

// ensureOpen opens the file and writes the IVF file header once.
func (w *ivfWriter) ensureOpen() error {
	if w.f != nil {
		return nil
	}
	f, err := os.Create(w.path)
	if err != nil {
		return err
	}
	var hdr [ivfFileHeaderSize]byte
	copy(hdr[0:4], "DKIF")
	binary.LittleEndian.PutUint16(hdr[4:6], 0)                 // version
	binary.LittleEndian.PutUint16(hdr[6:8], ivfFileHeaderSize) // header length
	copy(hdr[8:12], "AV01")                                    // fourcc
	binary.LittleEndian.PutUint16(hdr[12:14], 0)               // width (placeholder)
	binary.LittleEndian.PutUint16(hdr[14:16], 0)               // height (placeholder)
	binary.LittleEndian.PutUint32(hdr[16:20], 1)               // timebase num  (1/30 s per frame)
	binary.LittleEndian.PutUint32(hdr[20:24], 30)              // timebase den  (30 fps)
	binary.LittleEndian.PutUint32(hdr[24:28], 0)               // frame count (unknown)
	binary.LittleEndian.PutUint32(hdr[28:32], 0)               // reserved
	if _, err := f.Write(hdr[:]); err != nil {
		f.Close()
		return err
	}
	w.f = f
	log.Printf("IVF output opened: %s", w.path)
	return nil
}

// WriteFrame appends one temporal unit to the IVF file.
// pts is ignored; we use the internal frame counter so that
// ffplay sees a clean 0,1,2,… sequence at the declared 30 fps.
func (w *ivfWriter) WriteFrame(payload []byte, _ uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureOpen(); err != nil {
		return err
	}

	var rec [ivfFrameHeaderSize]byte
	binary.LittleEndian.PutUint32(rec[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint64(rec[4:12], w.frameSeq) // PTS = frame index

	if _, err := w.f.Write(rec[:]); err != nil {
		return err
	}
	if _, err := w.f.Write(payload); err != nil {
		return err
	}
	w.frameSeq++
	return nil
}

// Close flushes and closes the IVF file.
func (w *ivfWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// ─── YUV writer ──────────────────────────────────────────────────────────────

// yuvWriter appends raw planar YUV frames to a file.  Each frame is written
// as packed rows (stride padding is stripped) in Y-then-U-then-V order,
// exactly what ffplay expects with -f rawvideo -pixel_format yuv420p.
//
// To play the resulting file:
//
//	ffplay -f rawvideo -pixel_format yuv420p -video_size WxH output.yuv
type yuvWriter struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

func (w *yuvWriter) ensureOpen() error {
	if w.f != nil {
		return nil
	}
	f, err := os.Create(w.path)
	if err != nil {
		return err
	}
	w.f = f
	log.Printf("YUV output opened: %s", w.path)
	return nil
}

// WriteFrame writes one decoded picture as a packed planar YUV frame,
// stripping any stride alignment padding so each row is exactly Width (or
// ChromaWidth) bytes wide.
func (w *yuvWriter) WriteFrame(pic *av1.Picture) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureOpen(); err != nil {
		return err
	}

	// Write Y plane row by row (strip stride padding).
	for row := 0; row < pic.Height; row++ {
		start := row * pic.StrideY
		end := start + pic.Width
		if end > len(pic.Y) {
			end = len(pic.Y)
		}
		if _, err := w.f.Write(pic.Y[start:end]); err != nil {
			return err
		}
	}

	// Write U and V planes (chroma subsampled rows).
	cw := pic.ChromaWidth()
	ch := pic.ChromaHeight()
	for row := 0; row < ch; row++ {
		uStart := row * pic.StrideUV
		uEnd := uStart + cw
		if uEnd > len(pic.U) {
			uEnd = len(pic.U)
		}
		if _, err := w.f.Write(pic.U[uStart:uEnd]); err != nil {
			return err
		}
	}
	for row := 0; row < ch; row++ {
		vStart := row * pic.StrideUV
		vEnd := vStart + cw
		if vEnd > len(pic.V) {
			vEnd = len(pic.V)
		}
		if _, err := w.f.Write(pic.V[vStart:vEnd]); err != nil {
			return err
		}
	}
	return nil
}

// Close flushes and closes the YUV file.
func (w *yuvWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// ─── Pixel verification helper ───────────────────────────────────────────────

// yuvMeanLuma returns the mean luma (Y) value of the picture as a float.
// A value well above 0 and below 255 indicates real decoded pixel content.
func yuvMeanLuma(pic *av1.Picture) float64 {
	if len(pic.Y) == 0 || pic.Width == 0 || pic.Height == 0 {
		return 0
	}
	var sum uint64
	count := uint64(pic.Width * pic.Height)
	for row := 0; row < pic.Height; row++ {
		base := row * pic.StrideY
		for col := 0; col < pic.Width; col++ {
			sum += uint64(pic.Y[base+col])
		}
	}
	return float64(sum) / float64(count)
}
