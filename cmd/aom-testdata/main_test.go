package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestParseManifestFiltersDecoderVectors(t *testing.T) {
	data := strings.Join([]string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa *clip.ivf",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb input.y4m",
		"cccccccccccccccccccccccccccccccccccccccc stream.obu",
	}, "\n")
	vectors, err := parseManifest(strings.NewReader(data), regexp.MustCompile(`\.(ivf|obu)$`))
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 2 || vectors[0].Name != "clip.ivf" || vectors[1].Name != "stream.obu" {
		t.Fatalf("vectors = %+v", vectors)
	}
}

func TestDownloadVectorsVerifiesAndReusesFile(t *testing.T) {
	contents := []byte("av1 test vector")
	digest := sha1.Sum(contents)
	wantSHA := hex.EncodeToString(digest[:])
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write(contents)
	}))
	defer server.Close()
	out := t.TempDir()
	vectors := []vector{{Name: "sample.ivf", SHA1: wantSHA}}
	if err := downloadVectors(context.Background(), server.Client(), server.URL, out, vectors, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := downloadVectors(context.Background(), server.Client(), server.URL, out, vectors, io.Discard); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("HTTP requests = %d, want 1", requests)
	}
	got, err := os.ReadFile(filepath.Join(out, "sample.ivf"))
	if err != nil || string(got) != string(contents) {
		t.Fatalf("downloaded data = %q, error = %v", got, err)
	}
}

func TestDownloadRejectsBadSHA1(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("wrong"))
	}))
	defer server.Close()
	err := downloadVectors(context.Background(), server.Client(), server.URL, t.TempDir(), []vector{{Name: "bad.ivf", SHA1: strings.Repeat("0", 40)}}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "downloaded bad.ivf has SHA-1") {
		t.Fatalf("error = %v", err)
	}
}
