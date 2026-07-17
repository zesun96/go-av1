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
	"context"
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
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
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
	flagYUV  = flag.String("yuv", "output.y4m", "path to save decoded frames; *.y4m emits YUV4MPEG2 (playable directly with `ffplay`), any other suffix emits raw planar YUV420 (needs `ffplay -f rawvideo -pixel_format yuv420p -framerate 30 -video_size WxH`); empty disables")
	flagRTP  = flag.Bool("rtp-log", false, "log every RTP AV1 aggregation header (very verbose)")
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
		dec:         dec,
		ivfw:        ivfw,
		yuv:         yuv,
		connections: make(map[*webrtc.PeerConnection]struct{}),
	}

	http.HandleFunc("/", srv.handleIndex)
	http.HandleFunc("/offer", srv.handleOffer)
	http.HandleFunc("/stats", srv.handleBrowserStats)

	addr := fmt.Sprintf(":%d", *flagPort)
	log.Printf("listening on http://localhost%s", addr)
	httpServer := &http.Server{Addr: addr}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.ListenAndServe()
	}()

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSignals()
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http: %v", err)
		}
	case <-signalCtx.Done():
		// Restore the default handler so a second Ctrl+C can force termination.
		stopSignals()
		log.Printf("shutdown requested; finalizing active recording")
		srv.stopping.Store(true)
		srv.closePeerConnections()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("http shutdown: %v", err)
		}
		// Decoding is intentionally asynchronous from RTP reception. Wait for
		// queued reference frames before touching the shared output writers; a
		// second Ctrl+C still uses the restored default handler to force exit.
		srv.waitForTracks(0)
		srv.closeTrackOutputs()
		log.Printf("shutdown complete")
	}
}

// ─── server ──────────────────────────────────────────────────────────────────

type server struct {
	dec  av1.Decoder
	ivfw *ivfWriter
	yuv  *yuvWriter

	mu           sync.Mutex
	frameCount   int64
	stopping     atomic.Bool
	connections  map[*webrtc.PeerConnection]struct{}
	connectionMu sync.Mutex
	trackMu      sync.Mutex
	activeTracks int
}

func (s *server) registerPeerConnection(pc *webrtc.PeerConnection) bool {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	if s.stopping.Load() {
		return false
	}
	if s.connections == nil {
		s.connections = make(map[*webrtc.PeerConnection]struct{})
	}
	s.connections[pc] = struct{}{}
	return true
}

func (s *server) unregisterPeerConnection(pc *webrtc.PeerConnection) {
	s.connectionMu.Lock()
	delete(s.connections, pc)
	s.connectionMu.Unlock()
}

func (s *server) closePeerConnections() {
	s.connectionMu.Lock()
	connections := make([]*webrtc.PeerConnection, 0, len(s.connections))
	for pc := range s.connections {
		connections = append(connections, pc)
	}
	s.connectionMu.Unlock()
	for _, pc := range connections {
		if err := pc.Close(); err != nil {
			log.Printf("peer connection close: %v", err)
		}
		s.unregisterPeerConnection(pc)
	}
}

func (s *server) trackStarted() bool {
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	if s.stopping.Load() {
		return false
	}
	s.activeTracks++
	return true
}

func (s *server) trackFinished() {
	s.trackMu.Lock()
	s.activeTracks--
	s.trackMu.Unlock()
}

func (s *server) waitForTracks(timeout time.Duration) bool {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		s.trackMu.Lock()
		active := s.activeTracks
		s.trackMu.Unlock()
		if active == 0 {
			return true
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
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

type browserStats struct {
	CaptureFPS *float64 `json:"captureFPS"`
	EncodeFPS  *float64 `json:"encodeFPS"`
	SendFPS    *float64 `json:"sendFPS"`
	Width      int      `json:"width"`
	Height     int      `json:"height"`
	Limitation string   `json:"limitation"`
}

func (s *server) handleBrowserStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var stats browserStats
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&stats); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if stats.Limitation == "" {
		stats.Limitation = "none"
	}
	log.Printf("browser stats: capture=%s fps encode=%s fps send=%s fps output=%dx%d limitation=%s",
		formatOptionalFPS(stats.CaptureFPS), formatOptionalFPS(stats.EncodeFPS),
		formatOptionalFPS(stats.SendFPS), stats.Width, stats.Height, stats.Limitation)
	w.WriteHeader(http.StatusNoContent)
}

func formatOptionalFPS(value *float64) string {
	if value == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.1f", *value)
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
	if !s.registerPeerConnection(pc) {
		pc.Close() //nolint:errcheck
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	negotiated := false
	defer func() {
		if !negotiated {
			s.unregisterPeerConnection(pc)
			pc.Close() //nolint:errcheck
		}
	}()

	// Register track handler.
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if !s.trackStarted() {
			pc.Close() //nolint:errcheck
			return
		}
		defer s.trackFinished()
		log.Printf("track received: codec=%s ssrc=%d", track.Codec().MimeType, track.SSRC())
		s.consumeAV1Track(track)
	})

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("ICE state: %s", state)
		if state == webrtc.ICEConnectionStateFailed || state == webrtc.ICEConnectionStateClosed {
			pc.Close() //nolint:errcheck
			s.unregisterPeerConnection(pc)
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
	negotiated = true
	log.Println("answer sent to browser")
}

// ─── AV1 track consumer ──────────────────────────────────────────────────────

// consumeAV1Track reads RTP packets from track, reassembles fragmented AV1
// OBUs per RFC 9321, ensures every OBU carries a size field, writes the
// resulting temporal units to the IVF file, and feeds them to the decoder.
func (s *server) consumeAV1Track(track *webrtc.TrackRemote) {
	decodeQueue := newTemporalUnitQueue()
	decodeDone := make(chan struct{})
	decodeStart := time.Now()
	go func() {
		defer close(decodeDone)
		for {
			tu, ok := decodeQueue.Pop()
			if !ok {
				return
			}
			s.decodeTemporalUnit(tu.payload, tu.pts)
		}
	}()
	defer func() {
		pending, peak := decodeQueue.Close()
		if pending > 0 {
			log.Printf("RTP track ended; draining %d queued temporal units", pending)
		}
		<-decodeDone
		log.Printf("track ended, total frames decoded: %d", atomic.LoadInt64(&s.frameCount))
		log.Printf("decode queue drained: peak=%d elapsed=%s", peak, time.Since(decodeStart).Round(time.Millisecond))
		s.closeTrackOutputs()
	}()

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
			if !errors.Is(err, io.EOF) && !s.stopping.Load() {
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
			s.recordTemporalUnit(decodeQueue, tuBuf, pts)
			tuBuf = tuBuf[:0]
		}

		if *flagRTP {
			log.Printf("rtp aggHdr=0x%02x Z=%v Y=%v N=%v W=%d marker=%v len=%d",
				aggHdr, zBit, yBit, nBit, w, pkt.Marker, len(payload))
		}

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

		rtpTS := pkt.Timestamp
		if !ptsBaseSet {
			ptsBase = uint64(rtpTS)
			ptsBaseSet = true
		}
		// RTP timestamps wrap at 32 bits. Subtraction in uint32 space keeps
		// the normalized timestamp correct across that wrap boundary.
		pts = uint64(rtpTS - uint32(ptsBase))

		// Marker bit = end of temporal unit.
		if pkt.Marker {
			if inFragment {
				tuBuf = append(tuBuf, ensureOBUSizeField(fragBuf)...)
				fragBuf = nil
				inFragment = false
			}
			if len(tuBuf) > 0 {
				log.Printf("flushing TU: %d bytes, pts=%d", len(tuBuf), pts)
				s.recordTemporalUnit(decodeQueue, tuBuf, pts)
				tuBuf = tuBuf[:0]
			}
		}
	}

	// Flush remaining data.
	if inFragment && len(fragBuf) > 0 {
		tuBuf = append(tuBuf, ensureOBUSizeField(fragBuf)...)
	}
	if len(tuBuf) > 0 {
		s.recordTemporalUnit(decodeQueue, tuBuf, pts)
	}
}

type temporalUnit struct {
	payload []byte
	pts     uint64
}

// temporalUnitQueue keeps RTP reception independent from sequential AV1
// decoding. Compressed temporal units are small compared with decoded frames,
// so an unbounded queue preserves every reference frame without backpressuring
// TrackRemote while a high-resolution frame is being reconstructed.
type temporalUnitQueue struct {
	mu     sync.Mutex
	ready  *sync.Cond
	items  []temporalUnit
	closed bool
	peak   int
}

func newTemporalUnitQueue() *temporalUnitQueue {
	q := &temporalUnitQueue{}
	q.ready = sync.NewCond(&q.mu)
	return q
}

func (q *temporalUnitQueue) Push(tu temporalUnit) (depth int, peak int, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return len(q.items), q.peak, false
	}
	q.items = append(q.items, tu)
	if len(q.items) > q.peak {
		q.peak = len(q.items)
	}
	q.ready.Signal()
	return len(q.items), q.peak, true
}

func (q *temporalUnitQueue) Pop() (temporalUnit, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.ready.Wait()
	}
	if len(q.items) == 0 {
		return temporalUnit{}, false
	}
	tu := q.items[0]
	q.items[0] = temporalUnit{}
	q.items = q.items[1:]
	return tu, true
}

func (q *temporalUnitQueue) Close() (pending int, peak int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.ready.Broadcast()
	return len(q.items), q.peak
}

func (s *server) closeTrackOutputs() {
	if s.yuv != nil {
		written := s.yuv.Written()
		if err := s.yuv.Close(); err != nil {
			log.Printf("yuv close: %v", err)
		} else if written > 0 {
			log.Printf("YUV output finalized: %s", s.yuv.path)
		}
	}
	written := s.ivfw.Written()
	if err := s.ivfw.Close(); err != nil {
		log.Printf("ivf close: %v", err)
	} else if written > 0 {
		log.Printf("IVF output finalized: %s", s.ivfw.path)
	}
}

// recordTemporalUnit persists the compressed data before handing an owned copy
// to the asynchronous decoder. IVF recording therefore remains complete even
// when reconstruction is slower than the incoming stream.
func (s *server) recordTemporalUnit(q *temporalUnitQueue, payload []byte, pts uint64) {
	if err := s.ivfw.WriteFrame(payload, pts); err != nil {
		log.Printf("ivf write: %v", err)
	}
	owned := append([]byte(nil), payload...)
	depth, _, ok := q.Push(temporalUnit{payload: owned, pts: pts})
	if !ok {
		log.Printf("decode queue closed before pts=%d could be queued", pts)
	} else if depth >= 30 && depth%30 == 0 {
		log.Printf("decoder is behind RTP reception: %d temporal units queued", depth)
	}
}

// decodeTemporalUnit runs only on the per-track decode worker. AV1 reference
// frames and decoded YUV output therefore retain bitstream order.
func (s *server) decodeTemporalUnit(payload []byte, pts uint64) {
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
			if err := s.yuv.WriteFrame(pic, pts); err != nil {
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
// WebRTC video RTP timestamps use a 90 kHz clock. Keeping those timestamps in
// IVF preserves the capture timing, including variable-rate desktop sharing.
type ivfWriter struct {
	path    string
	mu      sync.Mutex
	f       *os.File
	written uint64
}

const (
	ivfFileHeaderSize  = 32
	ivfFrameHeaderSize = 12
	rtpVideoClock      = 90000
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
	binary.LittleEndian.PutUint32(hdr[16:20], rtpVideoClock)   // frame-rate numerator
	binary.LittleEndian.PutUint32(hdr[20:24], 1)               // frame-rate denominator
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
func (w *ivfWriter) WriteFrame(payload []byte, pts uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureOpen(); err != nil {
		return err
	}

	var rec [ivfFrameHeaderSize]byte
	binary.LittleEndian.PutUint32(rec[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint64(rec[4:12], pts)

	if _, err := w.f.Write(rec[:]); err != nil {
		return err
	}
	if _, err := w.f.Write(payload); err != nil {
		return err
	}
	w.written++
	return nil
}

// Close flushes and closes the IVF file.
func (w *ivfWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	var finalizeErr error
	if _, err := w.f.Seek(24, io.SeekStart); err != nil {
		finalizeErr = err
	} else {
		var count [4]byte
		binary.LittleEndian.PutUint32(count[:], uint32(w.written))
		_, finalizeErr = w.f.Write(count[:])
	}
	closeErr := w.f.Close()
	w.f = nil
	w.written = 0
	return errors.Join(finalizeErr, closeErr)
}

func (w *ivfWriter) Written() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.written
}

// ─── YUV writer ──────────────────────────────────────────────────────────────

// yuvWriter appends decoded planar YUV frames to a file. Two output formats
// are supported and selected automatically by file extension:
//
//	*.y4m  →  YUV4MPEG2 container (carries width/height/fps), playable as:
//	          ffplay output.y4m
//
//	other  →  raw planar YUV420 frames (no header), playable as:
//	          ffplay -f rawvideo -pixel_format yuv420p \
//	                 -framerate 30 -video_size WxH output.yuv
//
// Each frame is written as packed rows (stride padding is stripped) in
// Y-then-U-then-V order, in 4:2:0 layout.
type yuvWriter struct {
	path       string
	mu         sync.Mutex
	f          *os.File
	activePath string
	isY4M      bool // true if output uses YUV4MPEG2 container
	headOK     bool // true after Y4M stream header has been emitted
	wrote      int  // number of frames written to the current segment
	totalWrote int  // number of frames written across all segments
	segment    int
	firstPTS   uint64
	lastPTS    uint64
	ptsSet     bool
	width      int
	height     int
	segments   []string
}

func (w *yuvWriter) ensureOpen() error {
	if w.f != nil {
		return nil
	}
	w.activePath = yuvSegmentPath(w.path, w.segment)
	f, err := os.Create(w.activePath)
	if err != nil {
		return err
	}
	w.f = f
	w.segments = append(w.segments, w.activePath)
	w.isY4M = strings.EqualFold(filepath.Ext(w.path), ".y4m")
	log.Printf("YUV output opened: %s (format=%s)", w.activePath, map[bool]string{true: "Y4M", false: "raw"}[w.isY4M])
	return nil
}

// WriteFrame writes one decoded picture, stripping any stride alignment
// padding so each row is exactly Width (or ChromaWidth) bytes wide.
// For Y4M output, the first call also emits the stream header.
func (w *yuvWriter) WriteFrame(pic *av1.Picture, pts uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureOpen(); err != nil {
		return err
	}
	if w.headOK && (pic.Width != w.width || pic.Height != w.height) {
		if !w.isY4M {
			return fmt.Errorf("raw YUV resolution changed from %dx%d to %dx%d", w.width, w.height, pic.Width, pic.Height)
		}
		if pic.Width > w.width || pic.Height > w.height {
			oldWidth, oldHeight := w.width, w.height
			if err := w.closeCurrentLocked(); err != nil {
				return err
			}
			w.segment++
			log.Printf("Y4M canvas exceeded: %dx%d -> %dx%d; starting segment %d",
				oldWidth, oldHeight, pic.Width, pic.Height, w.segment)
			if err := w.ensureOpen(); err != nil {
				return err
			}
		}
	}

	if !w.headOK {
		// Y4M only supports a constant frame rate. Use a fixed-width field so
		// Close can replace this initial value with the observed average rate.
		if w.isY4M {
			hdr := y4mHeader(pic.Width, pic.Height, 30, 1)
			if _, err := w.f.WriteString(hdr); err != nil {
				return err
			}
		}
		w.headOK = true
		w.width = pic.Width
		w.height = pic.Height
	}
	if !w.ptsSet {
		w.firstPTS = pts
		w.ptsSet = true
	}
	w.lastPTS = pts
	if w.isY4M {
		if _, err := w.f.WriteString("FRAME\n"); err != nil {
			return err
		}
	}

	if err := writeCenteredPlane(w.f, pic.Y, pic.StrideY, pic.Width, pic.Height, w.width, w.height, 16, true); err != nil {
		return fmt.Errorf("write Y plane: %w", err)
	}
	cw, ch := pic.ChromaWidth(), pic.ChromaHeight()
	canvasCW, canvasCH := (w.width+1)/2, (w.height+1)/2
	if err := writeCenteredPlane(w.f, pic.U, pic.StrideUV, cw, ch, canvasCW, canvasCH, 128, false); err != nil {
		return fmt.Errorf("write U plane: %w", err)
	}
	if err := writeCenteredPlane(w.f, pic.V, pic.StrideUV, cw, ch, canvasCW, canvasCH, 128, false); err != nil {
		return fmt.Errorf("write V plane: %w", err)
	}
	w.wrote++
	w.totalWrote++
	return nil
}

func writeCenteredPlane(dst io.Writer, src []byte, srcStride, srcW, srcH, dstW, dstH int, fill byte, alignEven bool) error {
	if srcW <= 0 || srcH <= 0 || srcStride < srcW || dstW < srcW || dstH < srcH {
		return fmt.Errorf("invalid plane geometry src=%dx%d stride=%d dst=%dx%d", srcW, srcH, srcStride, dstW, dstH)
	}
	if (srcH-1)*srcStride+srcW > len(src) {
		return io.ErrUnexpectedEOF
	}
	xOff := (dstW - srcW) / 2
	yOff := (dstH - srcH) / 2
	if alignEven {
		xOff &^= 1
		yOff &^= 1
	}
	rowBuf := make([]byte, dstW)
	for y := 0; y < dstH; y++ {
		for x := range rowBuf {
			rowBuf[x] = fill
		}
		if y >= yOff && y < yOff+srcH {
			srcOff := (y - yOff) * srcStride
			copy(rowBuf[xOff:xOff+srcW], src[srcOff:srcOff+srcW])
		}
		if _, err := dst.Write(rowBuf); err != nil {
			return err
		}
	}
	return nil
}

// Close flushes and closes the YUV file.
func (w *yuvWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	err := w.closeCurrentLocked()
	if len(w.segments) > 0 && strings.EqualFold(filepath.Ext(w.path), ".y4m") {
		err = errors.Join(err, writeFFPlayList(w.path, w.segments))
	}
	w.segment = 0
	w.totalWrote = 0
	w.segments = nil
	return err
}

func (w *yuvWriter) closeCurrentLocked() error {
	if w.f == nil {
		return nil
	}
	var finalizeErr error
	if w.isY4M && w.headOK && w.wrote > 1 && w.lastPTS > w.firstPTS {
		num := uint64(w.wrote-1) * rtpVideoClock
		den := w.lastPTS - w.firstPTS
		divisor := gcd64(num, den)
		num /= divisor
		den /= divisor
		if _, err := w.f.Seek(0, io.SeekStart); err != nil {
			finalizeErr = err
		} else if _, err := w.f.WriteString(y4mHeader(w.width, w.height, num, den)); err != nil {
			finalizeErr = err
		} else {
			log.Printf("Y4M timing: %d frames, %d/%d fps, %.3f seconds",
				w.wrote, num, den, float64(w.lastPTS-w.firstPTS)/rtpVideoClock)
		}
	}
	closeErr := w.f.Close()
	w.f = nil
	w.activePath = ""
	w.isY4M = false
	w.headOK = false
	w.wrote = 0
	w.ptsSet = false
	w.width = 0
	w.height = 0
	return errors.Join(finalizeErr, closeErr)
}

func (w *yuvWriter) Written() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.totalWrote
}

func yuvSegmentPath(path string, segment int) string {
	if segment == 0 {
		return path
	}
	ext := filepath.Ext(path)
	return fmt.Sprintf("%s-%03d%s", strings.TrimSuffix(path, ext), segment, ext)
}

func writeFFPlayList(y4mPath string, segments []string) error {
	stem := strings.TrimSuffix(y4mPath, filepath.Ext(y4mPath))
	listPath := stem + ".ffplay"
	var list strings.Builder
	list.WriteString("# Y4M segments in playback order\n")
	for _, segment := range segments {
		fmt.Fprintln(&list, filepath.Base(segment))
	}
	if err := os.WriteFile(listPath, []byte(list.String()), 0o644); err != nil {
		return fmt.Errorf("write ffplay list: %w", err)
	}

	cmdPath := stem + "-play.cmd"
	cmd := "@echo off\r\n" +
		"setlocal\r\n" +
		"echo Dynamic Y4M segments restart ffplay when the frame size changes.\r\n" +
		fmt.Sprintf("for /f \"usebackq eol=# delims=\" %%%%F in (\"%%~dp0%s\") do (\r\n", filepath.Base(listPath)) +
		"  ffplay -autoexit -loglevel warning \"%~dp0%%F\"\r\n" +
		"  if errorlevel 1 exit /b 1\r\n" +
		")\r\n"
	if err := os.WriteFile(cmdPath, []byte(cmd), 0o755); err != nil {
		return fmt.Errorf("write ffplay launcher: %w", err)
	}
	log.Printf("FFplay list finalized: %s (%d segments); run %s",
		listPath, len(segments), cmdPath)
	return nil
}

func y4mHeader(width, height int, fpsNum, fpsDen uint64) string {
	return fmt.Sprintf("YUV4MPEG2 W%d H%d F%010d:%010d Ip A1:1 C420\n",
		width, height, fpsNum, fpsDen)
}

func gcd64(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
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
