// Command go-av1enc is the reference encoder CLI for go-av1.
//
// Intended invocation once milestones progress:
//
//	go-av1enc -i input.y4m -o output.ivf --preset 8
//
// At milestone M0 it only prints the project banner and exits.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/zesun96/go-av1/pkg/av1"
)

func main() {
	in := flag.String("i", "", "input Y4M file")
	out := flag.String("o", "", "output IVF file")
	preset := flag.Int("preset", 8, "encoder preset (0=slowest, 13=fastest)")
	crf := flag.Int("crf", 30, "constant rate factor")
	flag.Parse()

	fmt.Fprintf(os.Stderr, "go-av1enc %s (M0 scaffold)\n", av1.Version)

	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: go-av1enc -i <input.y4m> -o <output.ivf>")
		os.Exit(2)
	}

	_, err := av1.NewEncoder(av1.EncoderOptions{Preset: *preset, CRF: *crf})
	if err != nil {
		fmt.Fprintf(os.Stderr, "encoder: %v\n", err)
		fmt.Fprintf(os.Stderr, "(input=%q output=%q)\n", *in, *out)
		os.Exit(1)
	}
}
