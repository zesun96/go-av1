package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zesun96/go-av1/internal/conformance"
)

func TestWriteReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	want := report{
		SchemaVersion: reportSchemaVersion,
		Decoder:       "go-av1 test",
		Input:         "sample.ivf",
		InputSHA256:   "abc",
		Frames: []conformance.FrameDigest{{
			Index: 0, Width: 3, Height: 3, BitDepth: 8, Chroma: "4:2:0",
			YMD5: "y", UMD5: "u", VMD5: "v", FrameMD5: "frame",
		}},
	}
	if err := writeReport(path, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got report
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.SchemaVersion != reportSchemaVersion || len(got.Frames) != 1 || got.Frames[0].FrameMD5 != "frame" {
		t.Fatalf("report round trip = %+v", got)
	}
}
