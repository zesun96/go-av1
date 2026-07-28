package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseInputs(t *testing.T) {
	got := parseInputs(" first.y4m,second.y4m ,, third.y4m ")
	if len(got) != 3 || got[0] != "first.y4m" || got[2] != "third.y4m" {
		t.Fatalf("parseInputs=%q", got)
	}
}

func TestWriteSuiteReports(t *testing.T) {
	dir := t.TempDir()
	suite := suiteReport{
		Preset: 12, MeanBDRate: 24.5,
		Sequences: []report{
			{Input: "a.y4m", Width: 64, Height: 64, Frames: 6, BDRate: 20},
			{Input: "b.y4m", Width: 96, Height: 64, Frames: 8, BDRate: 29},
		},
	}
	if err := writeSuiteReports(dir, suite); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "suite-report.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "+24.50%") || !strings.Contains(text, "b.y4m") {
		t.Fatalf("suite report:\n%s", text)
	}
}
