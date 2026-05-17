// Command go-av1d is the reference decoder CLI for go-av1.
//
// Intended invocation once milestones progress:
//
//	go-av1d -i input.ivf -o output.y4m
//
// At milestone M0 it only prints the project banner and exits, because the
// decoder pipeline is not implemented yet.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/zesun96/go-av1/pkg/av1"
)

func main() {
	in := flag.String("i", "", "input AV1 file (IVF or Annex-B)")
	out := flag.String("o", "", "output Y4M file (- for stdout)")
	threads := flag.Int("threads", 0, "worker threads (0 = NumCPU)")
	flag.Parse()

	fmt.Fprintf(os.Stderr, "go-av1d %s (M0 scaffold)\n", av1.Version)

	if *in == "" {
		fmt.Fprintln(os.Stderr, "usage: go-av1d -i <input> -o <output>")
		os.Exit(2)
	}

	_, err := av1.NewDecoder(av1.DecoderOptions{Threads: *threads})
	if err != nil {
		fmt.Fprintf(os.Stderr, "decoder: %v\n", err)
		fmt.Fprintf(os.Stderr, "(input=%q output=%q)\n", *in, *out)
		os.Exit(1)
	}
}
