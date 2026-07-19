package conformance

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ReferenceFrame is a native-size whole-frame checksum produced by dav1d.
type ReferenceFrame struct {
	Index    int    `json:"index"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FrameMD5 string `json:"frame_md5"`
}

// Comparison records the first observable divergence from the reference.
type Comparison struct {
	Passed          bool        `json:"passed"`
	ComparedFrames  int         `json:"compared_frames"`
	FirstDifference *Difference `json:"first_difference,omitempty"`
}

// Difference identifies the first mismatching frame property.
type Difference struct {
	Frame           int               `json:"frame"`
	Kind            string            `json:"kind"`
	GoValue         string            `json:"go_value"`
	Dav1dValue      string            `json:"dav1d_value"`
	Sample          *SampleDifference `json:"sample,omitempty"`
	DiagnosticError string            `json:"diagnostic_error,omitempty"`
}

var dav1dFrameName = regexp.MustCompile(`^frame-(\d+)-(\d+)x(\d+)\.md5$`)

// RunDav1d invokes the reference decoder directly and collects per-frame MD5
// files. The filename carries dimensions, preserving dynamic-resolution output.
func RunDav1d(executable, input string, limit int) ([]ReferenceFrame, error) {
	tempDir, err := os.MkdirTemp("", "go-av1-dav1d-")
	if err != nil {
		return nil, fmt.Errorf("conformance: create dav1d temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	pattern := filepath.Join(tempDir, "frame-%06n-%wx%h.md5")
	// The "frame" muxer prefix is required even when the filename contains
	// %n: specifying a plain muxer name otherwise selects one output file.
	args := []string{"-i", input, "-o", pattern, "--muxer", "framemd5", "--quiet", "--filmgrain", "0", "--threads", "1", "--framedelay", "1"}
	if limit > 0 {
		args = append(args, "--limit", strconv.Itoa(limit))
	}
	cmd := exec.Command(executable, args...)
	cmd.Env = dav1dEnvironment(executable)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("conformance: dav1d failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return readDav1dFrames(tempDir)
}

// RunDav1dFrame decodes through target into an automatically removed binary
// file, then reads only the target frame. A file is required because dav1d's
// stdout remains in text mode on Windows and corrupts arbitrary YUV bytes.
func RunDav1dFrame(executable, input string, target int, frames []FrameDigest) ([]byte, error) {
	if target < 0 || target >= len(frames) {
		return nil, fmt.Errorf("conformance: target frame %d is out of range", target)
	}
	tempDir, err := os.MkdirTemp("", "go-av1-dav1d-raw-")
	if err != nil {
		return nil, fmt.Errorf("conformance: create dav1d raw temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)
	rawPath := filepath.Join(tempDir, "frames.yuv")
	args := []string{"-i", input, "-o", rawPath, "--muxer", "yuv", "--quiet", "--filmgrain", "0", "--threads", "1", "--framedelay", "1", "--limit", strconv.Itoa(target + 1)}
	cmd := exec.Command(executable, args...)
	cmd.Env = dav1dEnvironment(executable)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("conformance: dav1d raw output failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	f, err := os.Open(rawPath)
	if err != nil {
		return nil, fmt.Errorf("conformance: open dav1d raw output: %w", err)
	}
	defer f.Close()
	offset := int64(0)
	for i := 0; i < target; i++ {
		size, err := frameByteSize(frames[i])
		if err != nil {
			return nil, err
		}
		offset += int64(size)
	}
	size, err := frameByteSize(frames[target])
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("conformance: seek dav1d frame %d: %w", target, err)
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(f, raw); err != nil {
		return nil, fmt.Errorf("conformance: read dav1d frame %d: %w", target, err)
	}
	return raw, nil
}

func dav1dEnvironment(executable string) []string {
	env := os.Environ()
	exeDir := filepath.Dir(executable)
	search := []string{exeDir}
	buildLib := filepath.Clean(filepath.Join(exeDir, "..", "src"))
	if info, err := os.Stat(buildLib); err == nil && info.IsDir() {
		search = append(search, buildLib)
	}
	return append(env, "PATH="+strings.Join(search, string(os.PathListSeparator))+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func readDav1dFrames(dir string) ([]ReferenceFrame, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("conformance: read dav1d output: %w", err)
	}
	frames := make([]ReferenceFrame, 0, len(entries))
	for _, entry := range entries {
		match := dav1dFrameName.FindStringSubmatch(entry.Name())
		if entry.IsDir() || match == nil {
			continue
		}
		index, _ := strconv.Atoi(match[1])
		width, _ := strconv.Atoi(match[2])
		height, _ := strconv.Atoi(match[3])
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("conformance: read %s: %w", entry.Name(), err)
		}
		hash := strings.TrimSpace(string(data))
		if len(hash) != 32 {
			return nil, fmt.Errorf("conformance: invalid dav1d MD5 in %s: %q", entry.Name(), hash)
		}
		frames = append(frames, ReferenceFrame{Index: index, Width: width, Height: height, FrameMD5: strings.ToLower(hash)})
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i].Index < frames[j].Index })
	for i := range frames {
		if frames[i].Index != i {
			return nil, fmt.Errorf("conformance: missing dav1d frame %d", i)
		}
	}
	return frames, nil
}

// CompareFrames compares native dimensions and complete visible-frame hashes.
func CompareFrames(goFrames []FrameDigest, dav1dFrames []ReferenceFrame) Comparison {
	compared := min(len(goFrames), len(dav1dFrames))
	result := Comparison{Passed: true, ComparedFrames: compared}
	for i := 0; i < compared; i++ {
		if goFrames[i].Width != dav1dFrames[i].Width || goFrames[i].Height != dav1dFrames[i].Height {
			result.Passed = false
			result.FirstDifference = &Difference{Frame: i, Kind: "dimensions", GoValue: fmt.Sprintf("%dx%d", goFrames[i].Width, goFrames[i].Height), Dav1dValue: fmt.Sprintf("%dx%d", dav1dFrames[i].Width, dav1dFrames[i].Height)}
			return result
		}
		if !strings.EqualFold(goFrames[i].FrameMD5, dav1dFrames[i].FrameMD5) {
			result.Passed = false
			result.FirstDifference = &Difference{Frame: i, Kind: "frame_md5", GoValue: goFrames[i].FrameMD5, Dav1dValue: dav1dFrames[i].FrameMD5}
			return result
		}
	}
	if len(goFrames) != len(dav1dFrames) {
		result.Passed = false
		result.FirstDifference = &Difference{Frame: compared, Kind: "frame_count", GoValue: strconv.Itoa(len(goFrames)), Dav1dValue: strconv.Itoa(len(dav1dFrames))}
	}
	return result
}
