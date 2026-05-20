// Command go-av1enc is the reference encoder CLI for go-av1.
//
// Usage:
//
//	go-av1enc -i input.y4m -o output.ivf [--crf 30]
//
// It reads a Y4M file, encodes each frame as AV1 intra-only key frames,
// and writes the output as an IVF container.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/zesun96/go-av1/internal/encoder/ivf"
	"github.com/zesun96/go-av1/internal/encoder/y4m"
	"github.com/zesun96/go-av1/pkg/av1"
)

func main() {
	in := flag.String("i", "", "input Y4M file")
	out := flag.String("o", "", "output IVF file")
	crf := flag.Int("crf", 30, "constant rate factor (0-63)")
	flag.Parse()

	fmt.Fprintf(os.Stderr, "go-av1enc %s (M10 intra-only encoder)\n", av1.Version)

	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: go-av1enc -i <input.y4m> -o <output.ivf> [--crf 30]")
		os.Exit(2)
	}

	// Open input Y4M
	inFile, err := os.Open(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot open input: %v\n", err)
		os.Exit(1)
	}
	defer inFile.Close()

	reader, err := y4m.NewReader(inFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	hdr := reader.Header
	fmt.Fprintf(os.Stderr, "input: %dx%d, %d-bit, %s, %d/%d fps\n",
		hdr.Width, hdr.Height, hdr.BitDepth, hdr.ChromaSS,
		hdr.FrameRate[0], hdr.FrameRate[1])

	// Create encoder
	enc, err := av1.NewEncoder(av1.EncoderOptions{
		Width:        hdr.Width,
		Height:       hdr.Height,
		FrameRateNum: hdr.FrameRate[0],
		FrameRateDen: hdr.FrameRate[1],
		BitDepth:     hdr.BitDepth,
		CRF:          *crf,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: encoder init: %v\n", err)
		os.Exit(1)
	}
	defer enc.Close()

	// Open output IVF
	outFile, err := os.Create(*out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot create output: %v\n", err)
		os.Exit(1)
	}
	defer outFile.Close()

	ivfW := ivf.NewWriter(outFile, hdr.Width, hdr.Height, hdr.FrameRate[1], hdr.FrameRate[0])

	// Encode loop
	frameCount := 0
	for {
		frame, err := reader.ReadFrame()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: reading frame %d: %v\n", frameCount, err)
			os.Exit(1)
		}

		// Build Picture for encoder
		pic := &av1.Picture{
			Y:        frame.Y,
			U:        frame.Cb,
			V:        frame.Cr,
			StrideY:  hdr.Width,
			StrideUV: hdr.Width / 2,
			Width:    hdr.Width,
			Height:   hdr.Height,
			BitDepth: hdr.BitDepth,
			Chroma:   av1.Chroma420,
		}

		if err := enc.SendPicture(pic); err != nil {
			fmt.Fprintf(os.Stderr, "error: encoding frame %d: %v\n", frameCount, err)
			os.Exit(1)
		}

		// Drain packets
		for {
			pkt, err := enc.ReceivePacket()
			if err != nil {
				break // ErrAgain
			}
			if err := ivfW.WriteFrame(pkt.Data, uint64(pkt.PTS)); err != nil {
				fmt.Fprintf(os.Stderr, "error: writing frame: %v\n", err)
				os.Exit(1)
			}
		}

		frameCount++
		if frameCount%10 == 0 {
			fmt.Fprintf(os.Stderr, "\rencoded %d frames...", frameCount)
		}
	}

	// Flush
	enc.Flush()
	for {
		pkt, err := enc.ReceivePacket()
		if err != nil {
			break
		}
		if err := ivfW.WriteFrame(pkt.Data, uint64(pkt.PTS)); err != nil {
			fmt.Fprintf(os.Stderr, "error: writing frame: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Fprintf(os.Stderr, "\rdone: %d frames encoded to %s\n", frameCount, *out)
}
