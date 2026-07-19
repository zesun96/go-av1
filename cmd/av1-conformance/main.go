// Command av1-conformance produces native visible-plane hashes for an IVF stream.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/zesun96/go-av1/internal/conformance"
	"github.com/zesun96/go-av1/pkg/av1"
	"github.com/zesun96/go-av1/pkg/ivf"
)

const reportSchemaVersion = 1

type report struct {
	SchemaVersion int                       `json:"schema_version"`
	Decoder       string                    `json:"decoder"`
	Input         string                    `json:"input"`
	InputSHA256   string                    `json:"input_sha256"`
	IVF           ivfHeader                 `json:"ivf"`
	Frames        []conformance.FrameDigest `json:"frames"`
	Reference     *referenceReport          `json:"reference,omitempty"`
	Comparison    *conformance.Comparison   `json:"comparison,omitempty"`
}

type referenceReport struct {
	Decoder string                       `json:"decoder"`
	Frames  []conformance.ReferenceFrame `json:"frames"`
}

type ivfHeader struct {
	Width      uint16 `json:"width"`
	Height     uint16 `json:"height"`
	Rate       uint32 `json:"rate"`
	Scale      uint32 `json:"scale"`
	FrameCount uint32 `json:"declared_frame_count"`
}

func main() {
	input := flag.String("i", "", "input AV1 IVF file")
	reportPath := flag.String("report", "-", "JSON report path (- for stdout)")
	limit := flag.Int("limit", 0, "maximum output frames (0 = all)")
	dav1d := flag.String("dav1d", "", "optional dav1d executable for differential comparison")
	manifest := flag.String("manifest", "", "optional vector manifest for batch execution")
	vectors := flag.String("vectors", "", "optional directory of IVF vectors for dav1d comparison")
	include := flag.String("include", `(?i)\.ivf$`, "regular expression selecting relative paths in -vectors mode")
	corpus := flag.String("corpus", "directory vectors", "corpus label for -vectors mode")
	markdown := flag.String("markdown", "", "Markdown summary path (manifest mode)")
	flag.Parse()
	modes := 0
	for _, value := range []string{*input, *manifest, *vectors} {
		if value != "" {
			modes++
		}
	}
	if modes != 1 || *limit < 0 || (*vectors != "" && *dav1d == "") {
		fmt.Fprintln(os.Stderr, "usage: av1-conformance (-i input.ivf | -manifest vectors.json | -vectors directory -dav1d dav1d.exe) [-report report.json]")
		os.Exit(2)
	}
	var err error
	if *vectors != "" {
		pattern, compileErr := regexp.Compile(*include)
		if compileErr != nil {
			fmt.Fprintf(os.Stderr, "include pattern: %v\n", compileErr)
			os.Exit(2)
		}
		err = runVectorDirectory(*vectors, *corpus, *reportPath, *markdown, *dav1d, *limit, pattern)
	} else if *manifest != "" {
		err = runManifest(*manifest, *reportPath, *markdown, *dav1d, *limit)
	} else {
		err = run(*input, *reportPath, *dav1d, *limit)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(input, reportPath, dav1d string, limit int) error {
	f, err := os.Open(input)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer f.Close()

	inputHash := sha256.New()
	dm, err := ivf.NewDemuxer(io.TeeReader(f, inputHash), true)
	if err != nil {
		return fmt.Errorf("demux input: %w", err)
	}
	dec, err := av1.NewDecoder(av1.DecoderOptions{})
	if err != nil {
		return fmt.Errorf("create decoder: %w", err)
	}
	defer dec.Close()

	result := report{SchemaVersion: reportSchemaVersion, Decoder: "go-av1 " + av1.Version, Input: filepath.ToSlash(input)}
	hdr := dm.Header()
	result.IVF = ivfHeader{Width: hdr.Width, Height: hdr.Height, Rate: hdr.TimebaseNum, Scale: hdr.TimebaseDen, FrameCount: hdr.FrameCount}

	for limit == 0 || len(result.Frames) < limit {
		frameHeader, payload, readErr := dm.ReadFrame()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read IVF frame: %w", readErr)
		}
		if err := dec.SendData(payload); err != nil {
			return fmt.Errorf("decode input frame: %w", err)
		}
		for limit == 0 || len(result.Frames) < limit {
			pic, err := dec.GetPicture()
			if errors.Is(err, av1.ErrAgain) {
				break
			}
			if err != nil {
				return fmt.Errorf("get output picture: %w", err)
			}
			digest, digestErr := conformance.DigestPicture(pic, len(result.Frames))
			pic.Release()
			if digestErr != nil {
				return digestErr
			}
			// The decoder packet API does not carry container timestamps yet.
			// Synchronous IVF decoding associates output with the current packet.
			digest.PTS = int64(frameHeader.PTS)
			result.Frames = append(result.Frames, digest)
		}
	}
	if _, err := io.Copy(inputHash, f); err != nil {
		return fmt.Errorf("hash input: %w", err)
	}
	result.InputSHA256 = hex.EncodeToString(inputHash.Sum(nil))
	if err := dec.Flush(); err != nil {
		return fmt.Errorf("flush decoder: %w", err)
	}
	if dav1d != "" {
		referenceFrames, err := conformance.RunDav1d(dav1d, input, limit)
		if err != nil {
			return err
		}
		result.Reference = &referenceReport{Decoder: dav1d, Frames: referenceFrames}
		comparison := conformance.CompareFrames(result.Frames, referenceFrames)
		result.Comparison = &comparison
		if diff := comparison.FirstDifference; diff != nil && diff.Kind == "frame_md5" {
			goFrame, err := decodeNativeFrame(input, diff.Frame)
			if err == nil {
				var raw []byte
				raw, err = conformance.RunDav1dFrame(dav1d, input, diff.Frame, result.Frames)
				if err == nil {
					diff.Sample, err = conformance.FirstDifferentSample(goFrame, raw)
				}
			}
			if err != nil {
				diff.DiagnosticError = err.Error()
			}
		}
	}
	if err := writeReport(reportPath, result); err != nil {
		return err
	}
	if result.Comparison != nil && !result.Comparison.Passed {
		diff := result.Comparison.FirstDifference
		return fmt.Errorf("conformance mismatch at frame %d (%s); see %s", diff.Frame, diff.Kind, reportPath)
	}
	return nil
}

func decodeNativeFrame(input string, target int) (conformance.NativeFrame, error) {
	f, err := os.Open(input)
	if err != nil {
		return conformance.NativeFrame{}, fmt.Errorf("open input for diagnostics: %w", err)
	}
	defer f.Close()
	dm, err := ivf.NewDemuxer(f, true)
	if err != nil {
		return conformance.NativeFrame{}, fmt.Errorf("demux diagnostics input: %w", err)
	}
	dec, err := av1.NewDecoder(av1.DecoderOptions{})
	if err != nil {
		return conformance.NativeFrame{}, err
	}
	defer dec.Close()
	outputIndex := 0
	for {
		_, payload, err := dm.ReadFrame()
		if errors.Is(err, io.EOF) {
			return conformance.NativeFrame{}, fmt.Errorf("diagnostic frame %d not found", target)
		}
		if err != nil {
			return conformance.NativeFrame{}, err
		}
		if err := dec.SendData(payload); err != nil {
			return conformance.NativeFrame{}, err
		}
		for {
			pic, err := dec.GetPicture()
			if errors.Is(err, av1.ErrAgain) {
				break
			}
			if err != nil {
				return conformance.NativeFrame{}, err
			}
			if outputIndex == target {
				frame, copyErr := conformance.CopyPicture(pic)
				pic.Release()
				return frame, copyErr
			}
			outputIndex++
			pic.Release()
		}
	}
}

func writeReport(path string, result report) error {
	var w io.Writer = os.Stdout
	var f *os.File
	if path != "-" {
		var err error
		f, err = os.Create(path)
		if err != nil {
			return fmt.Errorf("create report: %w", err)
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}
