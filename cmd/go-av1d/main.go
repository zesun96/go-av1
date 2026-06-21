// Command go-av1d is the AV1 decoder CLI for go-av1.
//
// Usage:
//
//	go-av1d -i input.ivf [-o output.y4m]
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/zesun96/go-av1/pkg/av1"
	"github.com/zesun96/go-av1/pkg/ivf"
)

func main() {
	in := flag.String("i", "", "input AV1 file (IVF)")
	out := flag.String("o", "", "output Y4M file (- for stdout, empty = discard)")
	threads := flag.Int("threads", 0, "worker threads (0 = NumCPU)")
	filters := flag.String("filters", "all", "in-loop filters: all, none, or comma-separated deblock,cdef,restoration")
	flag.Parse()

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
		hdr.Width, hdr.Height, hdr.TimebaseDen, hdr.TimebaseNum)

	inloopFilters, err := parseInloopFilters(*filters)
	if err != nil {
		fmt.Fprintf(os.Stderr, "filters: %v\n", err)
		os.Exit(2)
	}

	dec, err := av1.NewDecoder(av1.DecoderOptions{
		Threads:          *threads,
		InloopFilters:    inloopFilters,
		InloopFiltersSet: true,
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

	frameCount := 0
	for {
		_, payload, err := dm.ReadFrame()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "read frame: %v\n", err)
			break
		}

		if err := dec.SendData(payload); err != nil {
			fmt.Fprintf(os.Stderr, "send data: %v\n", err)
			continue
		}

		for {
			pic, err := dec.GetPicture()
			if err != nil {
				break // ErrAgain or other
			}
			frameCount++
			if w != nil {
				writeY4MFrame(w, pic, frameCount == 1, hdr)
			}
			pic.Release()
		}
	}

	// Flush remaining frames.
	_ = dec.Flush()

	fmt.Fprintf(os.Stderr, "decoded %d frames\n", frameCount)
}

// writeY4MFrame writes a single raw YUV frame to w.
// On the first frame it emits the Y4M stream header.
func writeY4MFrame(w io.Writer, pic *av1.Picture, first bool, hdr ivf.FileHeader) {
	if first {
		var fpsDen, fpsNum uint32 = hdr.TimebaseDen, hdr.TimebaseNum
		if fpsNum == 0 {
			fpsNum, fpsDen = 1, 1
		}
		fmt.Fprintf(w, "YUV4MPEG2 W%d H%d F%d:%d Ip A0:0 C420\n",
			pic.Width, pic.Height, fpsDen, fpsNum)
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
