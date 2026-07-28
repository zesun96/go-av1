// Command av1-encbench compares go-av1 with SVT-AV1 over a CRF sweep.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zesun96/go-av1/internal/encoder/benchmark"
	"github.com/zesun96/go-av1/internal/encoder/ivf"
	"github.com/zesun96/go-av1/internal/encoder/y4m"
	"github.com/zesun96/go-av1/pkg/av1"
)

type result struct {
	Encoder  string  `json:"encoder"`
	CRF      int     `json:"crf"`
	Preset   int     `json:"preset"`
	Bytes    int64   `json:"bytes"`
	RateKbps float64 `json:"rate_kbps"`
	PSNR     float64 `json:"psnr"`
	Seconds  float64 `json:"seconds"`
}

type report struct {
	Input     string   `json:"input"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	Frames    int      `json:"frames"`
	Preset    int      `json:"preset"`
	CRFs      []int    `json:"crfs"`
	Results   []result `json:"results"`
	BDRate    float64  `json:"go_bd_rate_percent"`
	Generated string   `json:"generated"`
	FFmpeg    string   `json:"ffmpeg"`
}

type suiteReport struct {
	Preset     int      `json:"preset"`
	CRFs       []int    `json:"crfs"`
	Sequences  []report `json:"sequences"`
	MeanBDRate float64  `json:"mean_go_bd_rate_percent"`
	Generated  string   `json:"generated"`
	FFmpeg     string   `json:"ffmpeg"`
}

var psnrPattern = regexp.MustCompile(`average:([0-9]+(?:\.[0-9]+)?)`)

func main() {
	input := flag.String("i", "", "input Y4M sequence, or comma-separated sequence list")
	outDir := flag.String("out", "encoder-benchmark", "output directory")
	ffmpeg := flag.String("ffmpeg", "ffmpeg", "ffmpeg executable with libsvtav1")
	preset := flag.Int("preset", 12, "go-av1 and SVT-AV1 preset")
	crfText := flag.String("crfs", "12,24,36,48", "comma-separated CRF sweep (at least four)")
	reuse := flag.Bool("reuse", false, "reuse a matching report.json when present")
	flag.Parse()
	inputs := parseInputs(*input)
	if len(inputs) == 0 {
		fatal(errors.New("usage: av1-encbench -i input.y4m [-out directory]"))
	}
	crfs, err := parseCRFs(*crfText)
	if err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fatal(err)
	}
	suite := suiteReport{
		Preset: *preset, CRFs: crfs,
		Generated: time.Now().UTC().Format(time.RFC3339), FFmpeg: *ffmpeg,
	}
	for _, inputPath := range inputs {
		sequenceOut := *outDir
		if len(inputs) > 1 {
			sequenceOut = filepath.Join(*outDir, sequenceName(inputPath))
		}
		rep, err := runBenchmark(inputPath, sequenceOut, *ffmpeg, *preset, crfs, *reuse)
		if err != nil {
			fatal(err)
		}
		suite.Sequences = append(suite.Sequences, rep)
		suite.MeanBDRate += rep.BDRate
		fmt.Printf("%s: go-av1 BD-rate relative to SVT-AV1 preset %d: %+.2f%%\n",
			inputPath, *preset, rep.BDRate)
	}
	suite.MeanBDRate /= float64(len(suite.Sequences))
	if len(suite.Sequences) > 1 {
		if err := writeSuiteReports(*outDir, suite); err != nil {
			fatal(err)
		}
		fmt.Printf("suite mean BD-rate relative to SVT-AV1 preset %d: %+.2f%%\n",
			*preset, suite.MeanBDRate)
	}
}

func runBenchmark(input, outDir, ffmpeg string, preset int, crfs []int, reuse bool) (report, error) {
	if reuse {
		if rep, ok := reusableReport(outDir, input, preset, crfs); ok {
			return rep, nil
		}
	}
	meta, err := inspectY4M(input)
	if err != nil {
		return report{}, err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return report{}, err
	}
	duration := float64(meta.frames*meta.fpsDen) / float64(meta.fpsNum)
	rep := report{
		Input: input, Width: meta.width, Height: meta.height, Frames: meta.frames,
		Preset: preset, CRFs: crfs, Generated: time.Now().UTC().Format(time.RFC3339),
		FFmpeg: ffmpeg,
	}
	goCurve, svtCurve := make([]benchmark.Point, 0, len(crfs)), make([]benchmark.Point, 0, len(crfs))
	for _, crf := range crfs {
		goPath := filepath.Join(outDir, fmt.Sprintf("go-p%d-crf%d.ivf", preset, crf))
		start := time.Now()
		if err := encodeGo(input, goPath, preset, crf); err != nil {
			return report{}, err
		}
		goResult, err := measure(ffmpeg, input, goPath, "go-av1", preset, crf, duration, time.Since(start))
		if err != nil {
			return report{}, err
		}
		rep.Results = append(rep.Results, goResult)
		goCurve = append(goCurve, benchmark.Point{RateKbps: goResult.RateKbps, PSNR: goResult.PSNR})

		svtPath := filepath.Join(outDir, fmt.Sprintf("svt-p%d-crf%d.ivf", preset, crf))
		start = time.Now()
		if output, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
			"-i", input, "-an", "-c:v", "libsvtav1", "-preset", strconv.Itoa(preset),
			"-crf", strconv.Itoa(crf), "-f", "ivf", "-y", svtPath).CombinedOutput(); err != nil {
			return report{}, fmt.Errorf("SVT-AV1 CRF %d: %w\n%s", crf, err, output)
		}
		svtResult, err := measure(ffmpeg, input, svtPath, "SVT-AV1", preset, crf, duration, time.Since(start))
		if err != nil {
			return report{}, err
		}
		rep.Results = append(rep.Results, svtResult)
		svtCurve = append(svtCurve, benchmark.Point{RateKbps: svtResult.RateKbps, PSNR: svtResult.PSNR})
		fmt.Printf("CRF %d: go %.2f dB %.1f kbps; SVT %.2f dB %.1f kbps\n",
			crf, goResult.PSNR, goResult.RateKbps, svtResult.PSNR, svtResult.RateKbps)
	}
	rep.BDRate, err = benchmark.BDRate(svtCurve, goCurve)
	if err != nil {
		return report{}, err
	}
	if err := writeReports(outDir, rep); err != nil {
		return report{}, err
	}
	return rep, nil
}

func reusableReport(outDir, input string, preset int, crfs []int) (report, bool) {
	data, err := os.ReadFile(filepath.Join(outDir, "report.json"))
	if err != nil {
		return report{}, false
	}
	var rep report
	if json.Unmarshal(data, &rep) != nil || rep.Input != input || rep.Preset != preset ||
		len(rep.CRFs) != len(crfs) {
		return report{}, false
	}
	for i := range crfs {
		if rep.CRFs[i] != crfs[i] {
			return report{}, false
		}
	}
	return rep, true
}

func parseInputs(value string) []string {
	var out []string
	for _, field := range strings.Split(value, ",") {
		if path := strings.TrimSpace(field); path != "" {
			out = append(out, path)
		}
	}
	return out
}

func sequenceName(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	name = regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(name, "-")
	if name == "" {
		return "sequence"
	}
	return name
}

type y4mMeta struct {
	width, height, fpsNum, fpsDen, frames int
}

func inspectY4M(path string) (y4mMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return y4mMeta{}, err
	}
	defer f.Close()
	reader, err := y4m.NewReader(f)
	if err != nil {
		return y4mMeta{}, err
	}
	meta := y4mMeta{
		width: reader.Header.Width, height: reader.Header.Height,
		fpsNum: reader.Header.FrameRate[0], fpsDen: reader.Header.FrameRate[1],
	}
	for {
		_, err := reader.ReadFrame()
		if err == io.EOF {
			break
		}
		if err != nil {
			return y4mMeta{}, err
		}
		meta.frames++
	}
	if meta.frames == 0 || meta.fpsNum <= 0 || meta.fpsDen <= 0 {
		return y4mMeta{}, errors.New("benchmark: empty or invalid Y4M")
	}
	return meta, nil
}

func encodeGo(input, output string, preset, crf int) error {
	in, err := os.Open(input)
	if err != nil {
		return err
	}
	defer in.Close()
	reader, err := y4m.NewReader(in)
	if err != nil {
		return err
	}
	h := reader.Header
	enc, err := av1.NewEncoder(av1.EncoderOptions{
		Width: h.Width, Height: h.Height, BitDepth: h.BitDepth,
		FrameRateNum: h.FrameRate[0], FrameRateDen: h.FrameRate[1],
		Preset: preset, CRF: crf,
	})
	if err != nil {
		return err
	}
	defer enc.Close()
	out, err := os.Create(output)
	if err != nil {
		return err
	}
	defer out.Close()
	writer := ivf.NewWriter(out, h.Width, h.Height, h.FrameRate[1], h.FrameRate[0])
	for {
		frame, err := reader.ReadFrame()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := enc.SendPicture(&av1.Picture{
			Y: frame.Y, U: frame.Cb, V: frame.Cr,
			Width: h.Width, Height: h.Height, BitDepth: h.BitDepth,
			StrideY: h.Width, StrideUV: (h.Width + 1) / 2, Chroma: av1.Chroma420,
		}); err != nil {
			return err
		}
		packet, err := enc.ReceivePacket()
		if err != nil {
			return err
		}
		if err := writer.WriteFrame(packet.Data, uint64(packet.PTS)); err != nil {
			return err
		}
	}
	return nil
}

func measure(ffmpeg, input, encoded, encoderName string, preset, crf int,
	duration float64, elapsed time.Duration,
) (result, error) {
	info, err := os.Stat(encoded)
	if err != nil {
		return result{}, err
	}
	output, commandErr := exec.Command(ffmpeg, "-hide_banner", "-i", encoded, "-i", input,
		"-lavfi", "[0:v][1:v]psnr", "-f", "null", "-").CombinedOutput()
	match := psnrPattern.FindSubmatch(output)
	if commandErr != nil || len(match) != 2 {
		return result{}, fmt.Errorf("PSNR %s: %w\n%s", encoded, commandErr, output)
	}
	psnr, err := strconv.ParseFloat(string(match[1]), 64)
	if err != nil {
		return result{}, err
	}
	return result{
		Encoder: encoderName, CRF: crf, Preset: preset, Bytes: info.Size(),
		RateKbps: float64(info.Size()*8) / duration / 1000,
		PSNR:     psnr, Seconds: elapsed.Seconds(),
	}, nil
}

func parseCRFs(value string) ([]int, error) {
	fields := strings.Split(value, ",")
	if len(fields) < 4 {
		return nil, errors.New("benchmark: at least four CRFs are required")
	}
	out := make([]int, 0, len(fields))
	for _, field := range fields {
		value, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || value < 0 || value > 63 {
			return nil, fmt.Errorf("benchmark: invalid CRF %q", field)
		}
		out = append(out, value)
	}
	return out, nil
}

func writeReports(outDir string, rep report) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "report.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# Encoder comparison\n\n")
	fmt.Fprintf(&md, "- Input: `%s` (%dx%d, %d frames)\n", rep.Input, rep.Width, rep.Height, rep.Frames)
	fmt.Fprintf(&md, "- Preset: %d\n", rep.Preset)
	fmt.Fprintf(&md, "- go-av1 BD-rate relative to SVT-AV1: **%+.2f%%**\n\n", rep.BDRate)
	fmt.Fprintln(&md, "| Encoder | CRF | Rate (kbps) | PSNR (dB) | Time (s) |")
	fmt.Fprintln(&md, "|---|---:|---:|---:|---:|")
	for _, row := range rep.Results {
		fmt.Fprintf(&md, "| %s | %d | %.2f | %.3f | %.3f |\n",
			row.Encoder, row.CRF, row.RateKbps, row.PSNR, row.Seconds)
	}
	return os.WriteFile(filepath.Join(outDir, "report.md"), []byte(md.String()), 0o644)
}

func writeSuiteReports(outDir string, suite suiteReport) error {
	data, err := json.MarshalIndent(suite, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "suite-report.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	var md strings.Builder
	fmt.Fprintln(&md, "# Encoder suite comparison")
	fmt.Fprintln(&md)
	fmt.Fprintf(&md, "- Preset: %d\n", suite.Preset)
	fmt.Fprintf(&md, "- Arithmetic mean go-av1 BD-rate relative to SVT-AV1: **%+.2f%%**\n\n",
		suite.MeanBDRate)
	fmt.Fprintln(&md, "| Sequence | Size | Frames | BD-rate |")
	fmt.Fprintln(&md, "|---|---:|---:|---:|")
	for _, sequence := range suite.Sequences {
		fmt.Fprintf(&md, "| %s | %dx%d | %d | %+.2f%% |\n",
			sequence.Input, sequence.Width, sequence.Height, sequence.Frames, sequence.BDRate)
	}
	return os.WriteFile(filepath.Join(outDir, "suite-report.md"), []byte(md.String()), 0o644)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
