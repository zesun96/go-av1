package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zesun96/go-av1/internal/conformance"
	"github.com/zesun96/go-av1/pkg/av1"
	"github.com/zesun96/go-av1/pkg/ivf"
)

type suiteReport struct {
	SchemaVersion int            `json:"schema_version"`
	Corpus        string         `json:"corpus"`
	Results       []vectorResult `json:"results"`
	Summary       suiteSummary   `json:"summary"`
}

type suiteSummary struct {
	Total       int `json:"total"`
	Passed      int `json:"passed"`
	Failed      int `json:"failed"`
	Unsupported int `json:"unsupported"`
}

type vectorResult struct {
	Name           string                  `json:"name"`
	Tags           []string                `json:"tags,omitempty"`
	Status         string                  `json:"status"`
	ExpectedStatus string                  `json:"expected_status"`
	FramesExpected int                     `json:"frames_expected"`
	FramesActual   int                     `json:"frames_actual"`
	Comparison     *conformance.Comparison `json:"comparison,omitempty"`
	GoError        string                  `json:"go_error,omitempty"`
	ElapsedNS      int64                   `json:"elapsed_ns"`
}

func runManifest(manifestPath, reportPath, markdownPath, dav1d string, globalLimit int) error {
	f, err := os.Open(manifestPath)
	if err != nil {
		return fmt.Errorf("open manifest: %w", err)
	}
	manifest, err := conformance.ReadManifest(f)
	_ = f.Close()
	if err != nil {
		return err
	}
	return runSuite(manifest, filepath.Dir(manifestPath), reportPath, markdownPath, dav1d, globalLimit)
}

func runVectorDirectory(root, corpus, reportPath, markdownPath, dav1d string, globalLimit int, include *regexp.Regexp) error {
	manifest, err := discoverVectors(root, corpus, include)
	if err != nil {
		return err
	}
	return runSuite(manifest, root, reportPath, markdownPath, dav1d, globalLimit)
}

func discoverVectors(root, corpus string, include *regexp.Regexp) (conformance.Manifest, error) {
	manifest := conformance.Manifest{SchemaVersion: conformance.ManifestSchemaVersion, Corpus: corpus}
	names := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".ivf") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if include != nil && !include.MatchString(filepath.ToSlash(relative)) {
			return nil
		}
		name := vectorName(relative)
		if previous, exists := names[name]; exists {
			return fmt.Errorf("vector name collision %q: %s and %s", name, previous, relative)
		}
		names[name] = relative
		sha, err := fileSHA256(path)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		demuxer, demuxErr := ivf.NewDemuxer(f, false)
		_ = f.Close()
		frames := 0
		if demuxErr == nil {
			frames = int(demuxer.Header().FrameCount)
		}
		manifest.Vectors = append(manifest.Vectors, conformance.Vector{
			Name: name, Path: filepath.ToSlash(relative), SHA256: sha,
			Frames: frames, Tags: vectorTags(name), ExpectedStatus: vectorExpectedStatus(name),
		})
		return nil
	})
	if err != nil {
		return conformance.Manifest{}, fmt.Errorf("discover vectors: %w", err)
	}
	if len(manifest.Vectors) == 0 {
		return conformance.Manifest{}, fmt.Errorf("no IVF vectors found under %s", root)
	}
	sort.Slice(manifest.Vectors, func(i, j int) bool { return manifest.Vectors[i].Path < manifest.Vectors[j].Path })
	return manifest, nil
}

func vectorTags(name string) []string {
	tags := []string{"directory"}
	switch {
	case strings.Contains(strings.ToLower(name), "-b8-"):
		tags = append(tags, "bitdepth-8")
	case strings.Contains(strings.ToLower(name), "-b10-"):
		tags = append(tags, "bitdepth-10")
	case strings.Contains(strings.ToLower(name), "-b12-"):
		tags = append(tags, "bitdepth-12")
	}
	return tags
}

func vectorExpectedStatus(name string) string {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "-b10-") || strings.Contains(lower, "-b12-") {
		return "unsupported"
	}
	return "pass"
}

func vectorName(relative string) string {
	name := strings.TrimSuffix(filepath.ToSlash(relative), filepath.Ext(relative))
	var out strings.Builder
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			out.WriteRune(r)
		} else {
			out.WriteByte('_')
		}
	}
	return out.String()
}

func runSuite(manifest conformance.Manifest, baseDir, reportPath, markdownPath, dav1d string, globalLimit int) error {
	detailDir, err := os.MkdirTemp("", "go-av1-conformance-suite-")
	if err != nil {
		return fmt.Errorf("create suite temp dir: %w", err)
	}
	defer os.RemoveAll(detailDir)

	suite := suiteReport{SchemaVersion: reportSchemaVersion, Corpus: manifest.Corpus}
	for _, vector := range manifest.Vectors {
		input := vector.Path
		if !filepath.IsAbs(input) {
			input = filepath.Join(baseDir, filepath.FromSlash(input))
		}
		limit := vector.Limit
		if globalLimit > 0 && (limit == 0 || globalLimit < limit) {
			limit = globalLimit
		}
		started := time.Now()
		item := vectorResult{Name: vector.Name, Tags: vector.Tags, ExpectedStatus: vector.ExpectedStatus, FramesExpected: expectedFrameCount(vector, limit)}
		actualSHA, hashErr := fileSHA256(input)
		if hashErr != nil {
			item.GoError = hashErr.Error()
		} else if !strings.EqualFold(actualSHA, vector.SHA256) {
			item.GoError = fmt.Sprintf("input SHA-256 %s, expected %s", actualSHA, vector.SHA256)
		} else {
			detailPath := filepath.Join(detailDir, vector.Name+".json")
			runErr := run(input, detailPath, dav1d, limit)
			var detail report
			if data, readErr := os.ReadFile(detailPath); readErr == nil {
				if jsonErr := json.Unmarshal(data, &detail); jsonErr != nil {
					item.GoError = jsonErr.Error()
				} else {
					item.FramesActual = len(detail.Frames)
					item.Comparison = detail.Comparison
					if dav1d == "" {
						limitedVector := vector
						limitedVector.Limit = limit
						comparison := conformance.CompareExpected(detail.Frames, limitedVector)
						item.Comparison = &comparison
					}
				}
			}
			if runErr != nil && item.GoError == "" && item.Comparison == nil {
				item.GoError = runErr.Error()
			}
			if errors.Is(runErr, av1.ErrUnsupported) && vector.ExpectedStatus == "unsupported" {
				item.Status = "unsupported"
				item.GoError = ""
			}
		}
		if item.Status == "" {
			switch {
			case item.GoError != "":
				item.Status = "fail"
			case item.Comparison != nil && !item.Comparison.Passed:
				item.Status = "fail"
			case vector.ExpectedStatus == "unsupported":
				item.Status = "fail"
				item.GoError = "expected unsupported result, but decoding passed"
			default:
				item.Status = "pass"
			}
		}
		item.ElapsedNS = time.Since(started).Nanoseconds()
		suite.Results = append(suite.Results, item)
		addSummary(&suite.Summary, item.Status)
	}

	if err := writeJSON(reportPath, suite); err != nil {
		return err
	}
	if markdownPath != "" {
		if err := writeMarkdown(markdownPath, suite); err != nil {
			return err
		}
	}
	if suite.Summary.Failed > 0 {
		return fmt.Errorf("conformance suite failed: %d of %d vectors", suite.Summary.Failed, suite.Summary.Total)
	}
	return nil
}

func expectedFrameCount(vector conformance.Vector, limit int) int {
	if limit > 0 && (vector.Frames == 0 || limit < vector.Frames) {
		return limit
	}
	return vector.Frames
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open input: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash input: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func addSummary(summary *suiteSummary, status string) {
	summary.Total++
	switch status {
	case "pass":
		summary.Passed++
	case "unsupported":
		summary.Unsupported++
	default:
		summary.Failed++
	}
}

func writeJSON(path string, value any) error {
	var w io.Writer = os.Stdout
	var f *os.File
	if path != "-" {
		var err error
		f, err = os.Create(path)
		if err != nil {
			return fmt.Errorf("create report: %w", err)
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func writeMarkdown(path string, suite suiteReport) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create Markdown report: %w", err)
	}
	defer f.Close()
	fmt.Fprintf(f, "# AV1 Conformance: %s\n\n", suite.Corpus)
	fmt.Fprintf(f, "Total: %d | Passed: %d | Failed: %d | Unsupported: %d\n\n", suite.Summary.Total, suite.Summary.Passed, suite.Summary.Failed, suite.Summary.Unsupported)
	fmt.Fprintln(f, "| Vector | Status | Frames | First difference | Time |")
	fmt.Fprintln(f, "|---|---:|---:|---|---:|")
	for _, result := range suite.Results {
		difference := "-"
		if result.Comparison != nil && result.Comparison.FirstDifference != nil {
			diff := result.Comparison.FirstDifference
			difference = fmt.Sprintf("frame %d: %s", diff.Frame, diff.Kind)
			if diff.Sample != nil {
				difference += fmt.Sprintf(" %s(%d,%d)", diff.Sample.Plane, diff.Sample.X, diff.Sample.Y)
			}
		} else if result.GoError != "" {
			difference = strings.ReplaceAll(result.GoError, "|", "\\|")
		}
		fmt.Fprintf(f, "| %s | %s | %d/%d | %s | %.3fs |\n", result.Name, result.Status, result.FramesActual, result.FramesExpected, difference, float64(result.ElapsedNS)/1e9)
	}
	return nil
}
