// Command go-av1d is the AV1 decoder CLI for go-av1.
//
// Usage:
//
//	go-av1d -i input.ivf [-o output.y4m]
package main

import (
	"crypto/md5"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"

	"github.com/zesun96/go-av1/pkg/av1"
	"github.com/zesun96/go-av1/pkg/ivf"
)

func main() {
	version := flag.Bool("version", false, "print version and exit")
	in := flag.String("i", "", "input AV1 file (IVF)")
	out := flag.String("o", "", "output Y4M file (- for stdout, empty = discard)")
	outputFrame := flag.Int("output-frame", -1, "only write this zero-based output frame (-1 = all)")
	frameMD5 := flag.String("framemd5", "", "write visible-plane frame MD5 checksums")
	threads := flag.Int("threads", 0, "worker threads (0 = NumCPU)")
	filters := flag.String("filters", "all", "in-loop filters: all, none, or comma-separated deblock,cdef,restoration")
	traceSymbols := flag.Bool("trace-symbols", false, "log tile syntax symbols and MSAC state")
	traceFrames := flag.Bool("trace-frames", false, "log frame headers and reference CDF updates")
	traceFrame := flag.Int("trace-frame", -1, "only trace this zero-based IVF frame (-1 = all)")
	outputInvisible := flag.Bool("output-invisible", false, "also output hidden reference frames")
	limit := flag.Int("limit", 0, "stop after decoding this many frames (0 = all)")
	memstats := flag.Bool("memstats", false, "report decode allocation totals")
	cpuProfile := flag.String("cpuprofile", "", "write decode-loop CPU profile")
	memProfile := flag.String("memprofile", "", "write post-decode heap profile")
	flag.Parse()

	if *version {
		fmt.Printf("go-av1d %s\n", av1.Version)
		return
	}

	fmt.Fprintf(os.Stderr, "go-av1d %s (M6 pipeline)\n", av1.Version)

	if *in == "" {
		fmt.Fprintln(os.Stderr, "usage: go-av1d -i <input> [-o <output>]")
		os.Exit(2)
	}

	f, err := os.Open(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	dm, err := ivf.NewDemuxer(f, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demux: %v\n", err)
		os.Exit(1)
	}
	hdr := dm.Header()
	fmt.Fprintf(os.Stderr, "stream: %dx%d  fps: %d/%d\n",
		hdr.Width, hdr.Height, hdr.TimebaseNum, hdr.TimebaseDen)

	inloopFilters, err := parseInloopFilters(*filters)
	if err != nil {
		fmt.Fprintf(os.Stderr, "filters: %v\n", err)
		os.Exit(2)
	}

	var logger av1.Logger
	if *traceSymbols || *traceFrames {
		if *traceFrames {
			_ = os.Setenv("GOAV1_TRACE_FRAMES", "1")
			defer os.Unsetenv("GOAV1_TRACE_FRAMES") //nolint:errcheck
		}
	}
	if *traceSymbols {
		if *traceFrame < 0 {
			_ = os.Setenv("GOAV1_TRACE_SYMBOLS", "1")
		}
		logger = stderrLogger{}
	}
	if *traceFrames {
		logger = stderrLogger{}
	}
	dec, err := av1.NewDecoder(av1.DecoderOptions{
		Threads:          *threads,
		InloopFilters:    inloopFilters,
		InloopFiltersSet: true,
		OutputInvisible:  *outputInvisible,
		Logger:           logger,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "decoder: %v\n", err)
		os.Exit(1)
	}
	defer dec.Close()

	// Optionally open output writer.
	var w io.Writer
	if *out == "-" {
		w = os.Stdout
	} else if *out != "" {
		wf, err := os.Create(*out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "create output: %v\n", err)
			os.Exit(1)
		}
		defer wf.Close()
		w = wf
	}
	var md5w io.Writer
	if *frameMD5 != "" {
		mf, err := os.Create(*frameMD5)
		if err != nil {
			fmt.Fprintf(os.Stderr, "create framemd5: %v\n", err)
			os.Exit(1)
		}
		defer mf.Close()
		md5w = mf
	}

	frameCount := 0
	inputFrame := 0
	outputStarted := false
	var cpuProfileFile *os.File
	if *cpuProfile != "" {
		cpuProfileFile, err = os.Create(*cpuProfile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "create CPU profile: %v\n", err)
			os.Exit(1)
		}
		if err = pprof.StartCPUProfile(cpuProfileFile); err != nil {
			_ = cpuProfileFile.Close()
			fmt.Fprintf(os.Stderr, "start CPU profile: %v\n", err)
			os.Exit(1)
		}
	}
	var memBefore runtime.MemStats
	if *memstats {
		runtime.ReadMemStats(&memBefore)
	}
	for {
		if *limit > 0 && frameCount >= *limit {
			break
		}
		_, payload, err := dm.ReadFrame()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "read frame: %v\n", err)
			break
		}

		traceThisFrame := *traceSymbols && *traceFrame >= 0 && inputFrame == *traceFrame
		if traceThisFrame {
			_ = os.Setenv("GOAV1_TRACE_SYMBOLS", "1")
		}
		if err := dec.SendData(payload); err != nil {
			fmt.Fprintf(os.Stderr, "send data: %v\n", err)
			if traceThisFrame {
				_ = os.Unsetenv("GOAV1_TRACE_SYMBOLS")
			}
			inputFrame++
			continue
		}

		for {
			pic, err := dec.GetPicture()
			if err != nil {
				break // ErrAgain or other
			}
			frameCount++
			if w != nil && (*outputFrame < 0 || frameCount-1 == *outputFrame) {
				writeY4MFrame(w, pic, !outputStarted, hdr)
				outputStarted = true
			}
			if md5w != nil {
				writeFrameMD5(md5w, pic, frameCount-1)
			}
			pic.Release()
		}
		if traceThisFrame {
			_ = os.Unsetenv("GOAV1_TRACE_SYMBOLS")
		}
		inputFrame++
	}

	// Flush remaining frames.
	_ = dec.Flush()
	if cpuProfileFile != nil {
		pprof.StopCPUProfile()
		_ = cpuProfileFile.Close()
	}

	fmt.Fprintf(os.Stderr, "decoded %d frames\n", frameCount)
	if *memstats {
		var memAfter runtime.MemStats
		runtime.ReadMemStats(&memAfter)
		totalAlloc := memAfter.TotalAlloc - memBefore.TotalAlloc
		mallocs := memAfter.Mallocs - memBefore.Mallocs
		var bytesPerFrame, mallocsPerFrame uint64
		if frameCount > 0 {
			bytesPerFrame = totalAlloc / uint64(frameCount)
			mallocsPerFrame = mallocs / uint64(frameCount)
		}
		fmt.Fprintf(os.Stderr,
			"memstats: total_alloc_bytes=%d mallocs=%d bytes_per_frame=%d mallocs_per_frame=%d\n",
			totalAlloc, mallocs, bytesPerFrame, mallocsPerFrame)
	}
	if *memProfile != "" {
		profileFile, profileErr := os.Create(*memProfile)
		if profileErr != nil {
			fmt.Fprintf(os.Stderr, "create memory profile: %v\n", profileErr)
			os.Exit(1)
		}
		runtime.GC()
		profileErr = pprof.WriteHeapProfile(profileFile)
		closeErr := profileFile.Close()
		if profileErr != nil {
			fmt.Fprintf(os.Stderr, "write memory profile: %v\n", profileErr)
			os.Exit(1)
		}
		if closeErr != nil {
			fmt.Fprintf(os.Stderr, "close memory profile: %v\n", closeErr)
			os.Exit(1)
		}
	}
}

func writeFrameMD5(w io.Writer, pic *av1.Picture, frame int) {
	h := md5.New()
	for row := 0; row < pic.Height; row++ {
		_, _ = h.Write(pic.Y[row*pic.StrideY : row*pic.StrideY+pic.Width])
	}
	cw, ch := pic.ChromaWidth(), pic.ChromaHeight()
	for row := 0; row < ch; row++ {
		_, _ = h.Write(pic.U[row*pic.StrideUV : row*pic.StrideUV+cw])
	}
	for row := 0; row < ch; row++ {
		_, _ = h.Write(pic.V[row*pic.StrideUV : row*pic.StrideUV+cw])
	}
	fmt.Fprintf(w, "%d,%dx%d,%x\n", frame, pic.Width, pic.Height, h.Sum(nil))
}

type stderrLogger struct{}

func (stderrLogger) Logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// writeY4MFrame writes a single raw YUV frame to w.
// On the first frame it emits the Y4M stream header.
func writeY4MFrame(w io.Writer, pic *av1.Picture, first bool, hdr ivf.FileHeader) {
	if first {
		fpsNum, fpsDen := hdr.TimebaseNum, hdr.TimebaseDen
		if fpsNum == 0 || fpsDen == 0 {
			fpsNum, fpsDen = 1, 1
		}
		fmt.Fprintf(w, "YUV4MPEG2 W%d H%d F%d:%d Ip A0:0 C420\n",
			pic.Width, pic.Height, fpsNum, fpsDen)
	}
	fmt.Fprint(w, "FRAME\n")
	// Write luma plane.
	for row := 0; row < pic.Height; row++ {
		w.Write(pic.Y[row*pic.StrideY : row*pic.StrideY+pic.Width]) //nolint:errcheck
	}
	// Write chroma planes.
	ch := pic.ChromaHeight()
	cw := pic.ChromaWidth()
	for row := 0; row < ch; row++ {
		w.Write(pic.U[row*pic.StrideUV : row*pic.StrideUV+cw]) //nolint:errcheck
	}
	for row := 0; row < ch; row++ {
		w.Write(pic.V[row*pic.StrideUV : row*pic.StrideUV+cw]) //nolint:errcheck
	}
}

func parseInloopFilters(s string) (av1.InloopFilter, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "", "all":
		return av1.InloopFilterAll, nil
	case "none":
		return 0, nil
	}

	var mask av1.InloopFilter
	for _, part := range strings.Split(s, ",") {
		switch strings.TrimSpace(part) {
		case "deblock":
			mask |= av1.InloopFilterDeblock
		case "cdef":
			mask |= av1.InloopFilterCDEF
		case "restoration":
			mask |= av1.InloopFilterRestoration
		case "":
			continue
		default:
			return 0, fmt.Errorf("unknown filter %q", part)
		}
	}
	return mask, nil
}
