// Command av1-benchcmp performs a controlled end-to-end comparison between
// prebuilt go-av1d and dav1d command-line decoders.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/zesun96/go-av1/pkg/ivf"
)

type config struct {
	Input       string
	GoDecoder   string
	Dav1d       string
	Dav1dLibDir string
	Runs        int
	Warmup      int
	Threads     int
	Limit       int
	JSONPath    string
	Markdown    string
}

type inputInfo struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Bytes      int64  `json:"bytes"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	IVFPackets int    `json:"ivf_packets"`
	Measured   int    `json:"measured_packets"`
}

type decoderResult struct {
	Name             string    `json:"name"`
	Executable       string    `json:"executable"`
	ExecutableSHA256 string    `json:"executable_sha256"`
	Version          string    `json:"version"`
	SamplesMS        []float64 `json:"samples_ms"`
	MedianMS         float64   `json:"median_ms"`
	MeanMS           float64   `json:"mean_ms"`
	MinMS            float64   `json:"min_ms"`
	MaxMS            float64   `json:"max_ms"`
	PacketsPerSecond float64   `json:"packets_per_second"`
	NominalMPPerSec  float64   `json:"nominal_megapixels_per_second"`
}

type report struct {
	SchemaVersion int             `json:"schema_version"`
	GeneratedAt   string          `json:"generated_at"`
	OS            string          `json:"os"`
	Arch          string          `json:"arch"`
	CPU           string          `json:"cpu"`
	GoRuntime     string          `json:"go_runtime"`
	Input         inputInfo       `json:"input"`
	Runs          int             `json:"runs"`
	Warmup        int             `json:"warmup"`
	Threads       int             `json:"threads"`
	Filters       string          `json:"filters"`
	FilmGrain     bool            `json:"film_grain"`
	Results       []decoderResult `json:"results"`
	GoOverDav1d   float64         `json:"go_over_dav1d"`
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.Input, "input", "", "benchmark input in IVF format")
	flag.StringVar(&cfg.GoDecoder, "go-decoder", "", "path to prebuilt go-av1d executable")
	flag.StringVar(&cfg.Dav1d, "dav1d", "", "path to dav1d executable")
	flag.StringVar(&cfg.Dav1dLibDir, "dav1d-libdir", "", "directory containing the dav1d shared library")
	flag.IntVar(&cfg.Runs, "runs", 7, "number of measured runs per decoder")
	flag.IntVar(&cfg.Warmup, "warmup", 2, "number of warmup runs per decoder")
	flag.IntVar(&cfg.Threads, "threads", 1, "decoder threads and Go GOMAXPROCS")
	flag.IntVar(&cfg.Limit, "limit", 0, "maximum IVF packets to decode (0 = complete input)")
	flag.StringVar(&cfg.JSONPath, "json", "", "write the machine-readable report to this path")
	flag.StringVar(&cfg.Markdown, "markdown", "", "write a Markdown summary to this path")
	flag.Parse()

	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "av1-benchcmp:", err)
		os.Exit(1)
	}
}

func run(cfg config) error {
	if cfg.Input == "" || cfg.GoDecoder == "" || cfg.Dav1d == "" {
		return errors.New("-input, -go-decoder, and -dav1d are required")
	}
	if cfg.Runs < 1 || cfg.Warmup < 0 || cfg.Threads < 1 || cfg.Limit < 0 {
		return errors.New("runs and threads must be positive; warmup and limit cannot be negative")
	}

	var err error
	if cfg.Input, err = filepath.Abs(cfg.Input); err != nil {
		return err
	}
	if cfg.GoDecoder, err = executablePath(cfg.GoDecoder); err != nil {
		return fmt.Errorf("go decoder: %w", err)
	}
	if cfg.Dav1d, err = executablePath(cfg.Dav1d); err != nil {
		return fmt.Errorf("dav1d: %w", err)
	}
	if cfg.Dav1dLibDir == "" {
		candidate := filepath.Clean(filepath.Join(filepath.Dir(cfg.Dav1d), "..", "src"))
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			cfg.Dav1dLibDir = candidate
		}
	} else if cfg.Dav1dLibDir, err = filepath.Abs(cfg.Dav1dLibDir); err != nil {
		return err
	}

	input, err := inspectInput(cfg.Input, cfg.Limit)
	if err != nil {
		return err
	}
	goSHA, err := fileSHA256(cfg.GoDecoder)
	if err != nil {
		return err
	}
	davSHA, err := fileSHA256(cfg.Dav1d)
	if err != nil {
		return err
	}
	goVersion := commandVersion(cfg.GoDecoder, []string{"-version"}, nil)
	davEnv := withLibraryPath(os.Environ(), cfg.Dav1dLibDir)
	davVersion := commandVersion(cfg.Dav1d, []string{"--version"}, davEnv)

	goArgs := []string{
		"-i", cfg.Input,
		"-threads", fmt.Sprint(cfg.Threads),
		"-filters", "all",
	}
	davArgs := []string{
		"-i", cfg.Input,
		"-o", os.DevNull,
		"--muxer", "null",
		"--threads", fmt.Sprint(cfg.Threads),
		"--framedelay", "1",
		"--filmgrain", "0",
		"--inloopfilters", "all",
		"--quiet",
	}
	if cfg.Limit > 0 {
		goArgs = append(goArgs, "-limit", fmt.Sprint(cfg.Limit))
		davArgs = append(davArgs, "--limit", fmt.Sprint(cfg.Limit))
	}
	goEnv := replaceEnv(os.Environ(), "GOMAXPROCS", fmt.Sprint(cfg.Threads))

	type runner struct {
		name string
		path string
		args []string
		env  []string
	}
	runners := []runner{
		{name: "go-av1", path: cfg.GoDecoder, args: goArgs, env: goEnv},
		{name: "dav1d", path: cfg.Dav1d, args: davArgs, env: davEnv},
	}
	samples := make([][]time.Duration, len(runners))
	totalRounds := cfg.Warmup + cfg.Runs
	for round := 0; round < totalRounds; round++ {
		order := []int{0, 1}
		if round%2 != 0 {
			order[0], order[1] = order[1], order[0]
		}
		for _, idx := range order {
			elapsed, runErr := timeCommand(runners[idx].path, runners[idx].args, runners[idx].env)
			if runErr != nil {
				return fmt.Errorf("%s round %d: %w", runners[idx].name, round+1, runErr)
			}
			if round >= cfg.Warmup {
				samples[idx] = append(samples[idx], elapsed)
				fmt.Fprintf(os.Stderr, "%s sample %d/%d: %.3fs\n",
					runners[idx].name, len(samples[idx]), cfg.Runs, elapsed.Seconds())
			}
		}
	}

	results := make([]decoderResult, 2)
	results[0] = summarize("go-av1", cfg.GoDecoder, goSHA, goVersion, samples[0], input)
	results[1] = summarize("dav1d", cfg.Dav1d, davSHA, davVersion, samples[1], input)
	rep := report{
		SchemaVersion: 1,
		GeneratedAt:   time.Now().Format(time.RFC3339),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		CPU:           strings.TrimSpace(os.Getenv("PROCESSOR_IDENTIFIER")),
		GoRuntime:     runtime.Version(),
		Input:         input,
		Runs:          cfg.Runs,
		Warmup:        cfg.Warmup,
		Threads:       cfg.Threads,
		Filters:       "all",
		FilmGrain:     false,
		Results:       results,
		GoOverDav1d:   results[0].MedianMS / results[1].MedianMS,
	}

	fmt.Printf("go-av1 median %.3fs (%.2f packets/s)\n", results[0].MedianMS/1000, results[0].PacketsPerSecond)
	fmt.Printf("dav1d   median %.3fs (%.2f packets/s)\n", results[1].MedianMS/1000, results[1].PacketsPerSecond)
	fmt.Printf("ratio: go-av1 is %.2fx dav1d wall time\n", rep.GoOverDav1d)

	if cfg.JSONPath != "" {
		if err := writeJSON(cfg.JSONPath, rep); err != nil {
			return err
		}
	}
	if cfg.Markdown != "" {
		if err := writeMarkdown(cfg.Markdown, rep); err != nil {
			return err
		}
	}
	return nil
}

func executablePath(path string) (string, error) {
	if strings.ContainsAny(path, `/\`) || filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", err
		}
		return abs, nil
	}
	return exec.LookPath(path)
}

func inspectInput(path string, limit int) (inputInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return inputInfo{}, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return inputInfo{}, err
	}
	demuxer, err := ivf.NewDemuxer(f, true)
	if err != nil {
		return inputInfo{}, fmt.Errorf("inspect IVF: %w", err)
	}
	packets := 0
	for {
		_, _, readErr := demuxer.ReadFrame()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return inputInfo{}, fmt.Errorf("inspect IVF packet %d: %w", packets, readErr)
		}
		packets++
	}
	measured := packets
	if limit > 0 && limit < measured {
		measured = limit
	}
	hash, err := fileSHA256(path)
	if err != nil {
		return inputInfo{}, err
	}
	header := demuxer.Header()
	return inputInfo{
		Path: path, SHA256: hash, Bytes: stat.Size(),
		Width: int(header.Width), Height: int(header.Height),
		IVFPackets: packets, Measured: measured,
	}, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func commandVersion(path string, args, env []string) string {
	cmd := exec.Command(path, args...)
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func timeCommand(path string, args, env []string) (time.Duration, error) {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if len(detail) > 500 {
			detail = detail[len(detail)-500:]
		}
		return 0, fmt.Errorf("%v: %s", err, detail)
	}
	return elapsed, nil
}

func summarize(name, executable, executableSHA, version string, samples []time.Duration, input inputInfo) decoderResult {
	values := make([]float64, len(samples))
	var sum float64
	minimum := math.Inf(1)
	maximum := 0.0
	for i, sample := range samples {
		values[i] = float64(sample) / float64(time.Millisecond)
		sum += values[i]
		minimum = math.Min(minimum, values[i])
		maximum = math.Max(maximum, values[i])
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	median := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		median = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
	}
	seconds := median / 1000
	return decoderResult{
		Name: name, Executable: executable, ExecutableSHA256: executableSHA,
		Version: version, SamplesMS: values, MedianMS: median,
		MeanMS: sum / float64(len(values)), MinMS: minimum, MaxMS: maximum,
		PacketsPerSecond: float64(input.Measured) / seconds,
		NominalMPPerSec:  float64(input.Measured*input.Width*input.Height) / 1e6 / seconds,
	}
}

func writeJSON(path string, rep report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return err
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func writeMarkdown(path string, rep report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# AV1 Decoder Performance Comparison\n\n")
	fmt.Fprintf(&b, "Generated: %s\n\n", rep.GeneratedAt)
	fmt.Fprintf(&b, "- Environment: `%s/%s`, `%s`, `%s`\n", rep.OS, rep.Arch, rep.GoRuntime, rep.CPU)
	fmt.Fprintf(&b, "- Input: `%s`\n", rep.Input.Path)
	fmt.Fprintf(&b, "- SHA-256: `%s`\n", rep.Input.SHA256)
	if rep.Input.Width > 0 && rep.Input.Height > 0 {
		fmt.Fprintf(&b, "- Stream: %dx%d, %d/%d IVF packets measured\n", rep.Input.Width, rep.Input.Height, rep.Input.Measured, rep.Input.IVFPackets)
	} else {
		fmt.Fprintf(&b, "- Stream: dynamic/unspecified IVF dimensions, %d/%d packets measured\n", rep.Input.Measured, rep.Input.IVFPackets)
	}
	fmt.Fprintf(&b, "- Configuration: %d thread(s), filters all, film grain disabled, %d warmup + %d measured runs\n\n", rep.Threads, rep.Warmup, rep.Runs)
	fmt.Fprintf(&b, "| Decoder | Median | Mean | Min | Max | Packets/s | Nominal MP/s |\n")
	fmt.Fprintf(&b, "|---|---:|---:|---:|---:|---:|---:|\n")
	for _, result := range rep.Results {
		megapixels := "n/a"
		if rep.Input.Width > 0 && rep.Input.Height > 0 {
			megapixels = fmt.Sprintf("%.2f", result.NominalMPPerSec)
		}
		fmt.Fprintf(&b, "| %s | %.3fs | %.3fs | %.3fs | %.3fs | %.2f | %s |\n",
			result.Name, result.MedianMS/1000, result.MeanMS/1000, result.MinMS/1000,
			result.MaxMS/1000, result.PacketsPerSecond, megapixels)
	}
	fmt.Fprintf(&b, "\n`go-av1` uses **%.2fx** the dav1d median wall time.\n\n", rep.GoOverDav1d)
	for _, result := range rep.Results {
		fmt.Fprintf(&b, "- `%s` version: `%s`\n", result.Name, strings.ReplaceAll(result.Version, "\n", " "))
		fmt.Fprintf(&b, "- `%s` executable SHA-256: `%s`\n", result.Name, result.ExecutableSHA256)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func replaceEnv(env []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(strings.ToUpper(entry), prefix) {
			continue
		}
		out = append(out, entry)
	}
	return append(out, key+"="+value)
}

func withLibraryPath(env []string, dir string) []string {
	if dir == "" {
		return env
	}
	key := "PATH"
	if runtime.GOOS != "windows" {
		key = "LD_LIBRARY_PATH"
	}
	current := ""
	for _, entry := range env {
		if strings.HasPrefix(strings.ToUpper(entry), strings.ToUpper(key)+"=") {
			current = strings.SplitN(entry, "=", 2)[1]
			break
		}
	}
	value := dir
	if current != "" {
		value += string(os.PathListSeparator) + current
	}
	return replaceEnv(env, key, value)
}
