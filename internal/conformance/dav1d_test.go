package conformance

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestRunDav1dFrameIntegration(t *testing.T) {
	executable := os.Getenv("GOAV1_TEST_DAV1D")
	input := os.Getenv("GOAV1_TEST_IVF")
	if executable == "" || input == "" {
		t.Skip("set GOAV1_TEST_DAV1D and GOAV1_TEST_IVF to run the dav1d integration test")
	}
	refs, err := RunDav1d(executable, input, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("reference frame count = %d", len(refs))
	}
	frames := []FrameDigest{
		{Width: refs[0].Width, Height: refs[0].Height, BitDepth: 8, Chroma: "4:2:0"},
		{Width: refs[1].Width, Height: refs[1].Height, BitDepth: 8, Chroma: "4:2:0"},
	}
	raw, err := RunDav1dFrame(executable, input, 1, frames)
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(raw)
	if got := hex.EncodeToString(sum[:]); got != refs[1].FrameMD5 {
		t.Fatalf("raw frame MD5 = %s, want %s", got, refs[1].FrameMD5)
	}
}

func TestReadDav1dFramesSortsAndParsesDynamicSizes(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"frame-000001-1920x1080.md5": "22222222222222222222222222222222\n",
		"frame-000000-640x360.md5":   "11111111111111111111111111111111\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	frames, err := readDav1dFrames(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || frames[0].Width != 640 || frames[1].Width != 1920 {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestCompareFramesFirstDifference(t *testing.T) {
	goFrames := []FrameDigest{
		{Width: 640, Height: 360, FrameMD5: "aa"},
		{Width: 1280, Height: 720, FrameMD5: "bb"},
	}
	refs := []ReferenceFrame{
		{Width: 640, Height: 360, FrameMD5: "AA"},
		{Width: 1920, Height: 1080, FrameMD5: "bb"},
	}
	got := CompareFrames(goFrames, refs)
	if got.Passed || got.FirstDifference == nil || got.FirstDifference.Frame != 1 || got.FirstDifference.Kind != "dimensions" {
		t.Fatalf("comparison = %+v", got)
	}
}

func TestCompareFramesCountDifference(t *testing.T) {
	got := CompareFrames([]FrameDigest{{FrameMD5: "aa"}}, nil)
	if got.Passed || got.ComparedFrames != 0 || got.FirstDifference.Kind != "frame_count" {
		t.Fatalf("comparison = %+v", got)
	}
}

func TestCompareFramesHashDifference(t *testing.T) {
	goFrames := []FrameDigest{{Width: 8, Height: 8, FrameMD5: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	refs := []ReferenceFrame{{Width: 8, Height: 8, FrameMD5: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
	got := CompareFrames(goFrames, refs)
	if got.Passed || got.FirstDifference == nil || got.FirstDifference.Frame != 0 || got.FirstDifference.Kind != "frame_md5" {
		t.Fatalf("comparison = %+v", got)
	}
}
