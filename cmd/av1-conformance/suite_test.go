package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zesun96/go-av1/internal/conformance"
)

func TestDiscoverVectorsSortsAndReadsIVF(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestIVF(t, filepath.Join(root, "z.ivf"), 3)
	writeTestIVF(t, filepath.Join(root, "sub", "a.IVF"), 2)
	if err := os.WriteFile(filepath.Join(root, "bad.ivf"), []byte("damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := discoverVectors(root, "test", regexp.MustCompile(`(?i)\.ivf$`))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Vectors) != 3 || manifest.Vectors[0].Path != "bad.ivf" || manifest.Vectors[0].Frames != 0 || manifest.Vectors[1].Path != "sub/a.IVF" || manifest.Vectors[1].Frames != 2 {
		t.Fatalf("vectors = %+v", manifest.Vectors)
	}
}

func TestDiscoverVectorsClassifiesHighBitDepth(t *testing.T) {
	root := t.TempDir()
	writeTestIVF(t, filepath.Join(root, "sample-b8-00.ivf"), 1)
	writeTestIVF(t, filepath.Join(root, "sample-b10-00.ivf"), 1)
	writeTestIVF(t, filepath.Join(root, "sample-b12-00.ivf"), 1)
	manifest, err := discoverVectors(root, "test", regexp.MustCompile(`-b(?:8|10|12)-`))
	if err != nil {
		t.Fatal(err)
	}
	wantStatus := []string{"unsupported", "unsupported", "pass"}
	wantTag := []string{"bitdepth-10", "bitdepth-12", "bitdepth-8"}
	for i, vector := range manifest.Vectors {
		if vector.ExpectedStatus != wantStatus[i] || !containsString(vector.Tags, wantTag[i]) {
			t.Errorf("vector %q: status=%q tags=%v, want %q and %q", vector.Name, vector.ExpectedStatus, vector.Tags, wantStatus[i], wantTag[i])
		}
	}
}

func TestDiscoverVectorsAppliesIncludeToRelativePath(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestIVF(t, filepath.Join(root, "keep", "one.ivf"), 1)
	writeTestIVF(t, filepath.Join(root, "two.ivf"), 1)
	manifest, err := discoverVectors(root, "test", regexp.MustCompile(`^keep/`))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Vectors) != 1 || manifest.Vectors[0].Path != "keep/one.ivf" {
		t.Fatalf("vectors = %+v", manifest.Vectors)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func writeTestIVF(t *testing.T, path string, frames uint32) {
	t.Helper()
	header := make([]byte, 32)
	copy(header, "DKIF")
	binary.LittleEndian.PutUint16(header[6:8], 32)
	copy(header[8:12], "AV01")
	binary.LittleEndian.PutUint16(header[12:14], 16)
	binary.LittleEndian.PutUint16(header[14:16], 16)
	binary.LittleEndian.PutUint32(header[16:20], 30)
	binary.LittleEndian.PutUint32(header[20:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], frames)
	if err := os.WriteFile(path, header, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWriteMarkdownIncludesFirstSample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.md")
	suite := suiteReport{
		Corpus:  "smoke",
		Summary: suiteSummary{Total: 1, Failed: 1},
		Results: []vectorResult{{
			Name: "broken", Status: "fail", FramesActual: 2, FramesExpected: 3,
			Comparison: &conformance.Comparison{FirstDifference: &conformance.Difference{
				Frame: 1, Kind: "frame_md5", Sample: &conformance.SampleDifference{Plane: "U", X: 2, Y: 3},
			}},
		}},
	}
	if err := writeMarkdown(path, suite); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "Total: 1") || !strings.Contains(text, "frame 1: frame_md5 U(2,3)") {
		t.Fatalf("Markdown report:\n%s", text)
	}
}
