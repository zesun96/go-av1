package conformance

import (
	"strings"
	"testing"
)

func TestReadManifestAndCompareExpected(t *testing.T) {
	manifest, err := ReadManifest(strings.NewReader(`{
  "schema_version": 1,
  "corpus": "smoke",
  "vectors": [{
    "name": "odd", "path": "odd.ivf",
    "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "frames": 1, "profile": 0, "bit_depth": 8, "chroma": "4:2:0",
    "expected_status": "pass",
    "expected_frame_md5": [{"index": 0, "width": 3, "height": 3, "frame_md5": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]
  }]
}`))
	if err != nil {
		t.Fatal(err)
	}
	got := CompareExpected([]FrameDigest{{Width: 3, Height: 3, FrameMD5: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}, manifest.Vectors[0])
	if !got.Passed || got.ComparedFrames != 1 {
		t.Fatalf("comparison = %+v", got)
	}
}

func TestReadManifestRejectsUnknownField(t *testing.T) {
	_, err := ReadManifest(strings.NewReader(`{"schema_version":1,"corpus":"x","vectors":[],"extra":true}`))
	if err == nil {
		t.Fatal("expected validation error")
	}
}
