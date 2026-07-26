package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSummarizeUsesMedianAndMeasuredPackets(t *testing.T) {
	input := inputInfo{Width: 1920, Height: 1080, Measured: 120}
	got := summarize("decoder", "decoder.exe", "hash", "version",
		[]time.Duration{3 * time.Second, time.Second, 2 * time.Second}, input)
	if got.MedianMS != 2000 {
		t.Fatalf("median = %v, want 2000", got.MedianMS)
	}
	if math.Abs(got.PacketsPerSecond-60) > 0.001 {
		t.Fatalf("packets/s = %v, want 60", got.PacketsPerSecond)
	}
}

func TestReplaceEnvIsCaseInsensitive(t *testing.T) {
	got := replaceEnv([]string{"Path=old", "KEEP=yes"}, "PATH", "new")
	if len(got) != 2 || got[0] != "KEEP=yes" || got[1] != "PATH=new" {
		t.Fatalf("environment = %#v", got)
	}
}

func TestWriteReports(t *testing.T) {
	dir := t.TempDir()
	rep := report{
		GeneratedAt: "2026-07-26T00:00:00+08:00",
		OS:          "windows", Arch: "amd64", GoRuntime: "go1.24",
		Input: inputInfo{Path: "sample.ivf", SHA256: "abc", Width: 16, Height: 16, Measured: 2, IVFPackets: 2},
		Runs:  1, Threads: 1, Filters: "all",
		Results:     []decoderResult{{Name: "go-av1", MedianMS: 1, MeanMS: 1, MinMS: 1, MaxMS: 1}},
		GoOverDav1d: 2,
	}
	jsonPath := filepath.Join(dir, "report.json")
	mdPath := filepath.Join(dir, "report.md")
	if err := writeJSON(jsonPath, rep); err != nil {
		t.Fatal(err)
	}
	if err := writeMarkdown(mdPath, rep); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{jsonPath, mdPath} {
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			t.Fatalf("report %s: data=%q err=%v", path, data, err)
		}
	}
}

func TestWriteMarkdownMarksDynamicMegapixelsUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.md")
	rep := report{
		Input:   inputInfo{Measured: 2, IVFPackets: 3},
		Results: []decoderResult{{Name: "go-av1"}},
	}
	if err := writeMarkdown(path, rep); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !containsAll(text, "dynamic/unspecified", "| n/a |") {
		t.Fatalf("Markdown report:\n%s", text)
	}
}

func containsAll(text string, values ...string) bool {
	for _, value := range values {
		if !strings.Contains(text, value) {
			return false
		}
	}
	return true
}
