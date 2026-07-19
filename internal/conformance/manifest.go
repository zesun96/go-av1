package conformance

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const ManifestSchemaVersion = 1

// Manifest describes a reproducible, externally stored conformance corpus.
type Manifest struct {
	SchemaVersion int      `json:"schema_version"`
	Corpus        string   `json:"corpus"`
	Vectors       []Vector `json:"vectors"`
}

// Vector identifies one input and its expected native reconstruction.
type Vector struct {
	Name             string           `json:"name"`
	Path             string           `json:"path"`
	SHA256           string           `json:"sha256"`
	Frames           int              `json:"frames"`
	Profile          int              `json:"profile"`
	BitDepth         int              `json:"bit_depth"`
	Chroma           string           `json:"chroma"`
	Tags             []string         `json:"tags,omitempty"`
	Limit            int              `json:"limit,omitempty"`
	ExpectedStatus   string           `json:"expected_status"`
	ExpectedFrameMD5 []ReferenceFrame `json:"expected_frame_md5,omitempty"`
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
var md5Pattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
var vectorNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ReadManifest decodes and validates a manifest without accessing its inputs.
func ReadManifest(r io.Reader) (Manifest, error) {
	var manifest Manifest
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("conformance: decode manifest: %w", err)
	}
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return Manifest{}, fmt.Errorf("conformance: manifest schema %d is unsupported", manifest.SchemaVersion)
	}
	if manifest.Corpus == "" || len(manifest.Vectors) == 0 {
		return Manifest{}, fmt.Errorf("conformance: manifest corpus and vectors are required")
	}
	names := make(map[string]struct{}, len(manifest.Vectors))
	for i, vector := range manifest.Vectors {
		if !vectorNamePattern.MatchString(vector.Name) || vector.Path == "" || !sha256Pattern.MatchString(vector.SHA256) {
			return Manifest{}, fmt.Errorf("conformance: vector %d has invalid name, path, or SHA-256", i)
		}
		if _, exists := names[vector.Name]; exists {
			return Manifest{}, fmt.Errorf("conformance: duplicate vector name %q", vector.Name)
		}
		names[vector.Name] = struct{}{}
		if vector.Frames < 0 || vector.Limit < 0 {
			return Manifest{}, fmt.Errorf("conformance: vector %q has a negative frame count or limit", vector.Name)
		}
		if vector.ExpectedStatus != "pass" && vector.ExpectedStatus != "unsupported" {
			return Manifest{}, fmt.Errorf("conformance: vector %q has invalid expected_status %q", vector.Name, vector.ExpectedStatus)
		}
		lastIndex := -1
		for _, expected := range vector.ExpectedFrameMD5 {
			if expected.Index <= lastIndex || expected.Width <= 0 || expected.Height <= 0 || !md5Pattern.MatchString(expected.FrameMD5) {
				return Manifest{}, fmt.Errorf("conformance: vector %q has invalid expected frame %d", vector.Name, expected.Index)
			}
			lastIndex = expected.Index
		}
	}
	return manifest, nil
}

// CompareExpected checks the declared frame count and sparse reference hashes.
func CompareExpected(frames []FrameDigest, vector Vector) Comparison {
	expectedCount := vector.Frames
	if vector.Limit > 0 && (expectedCount == 0 || vector.Limit < expectedCount) {
		expectedCount = vector.Limit
	}
	comparedHashes := 0
	for _, expected := range vector.ExpectedFrameMD5 {
		if expectedCount == 0 || expected.Index < expectedCount {
			comparedHashes++
		}
	}
	result := Comparison{Passed: true, ComparedFrames: comparedHashes}
	if expectedCount > 0 && len(frames) != expectedCount {
		result.Passed = false
		result.FirstDifference = &Difference{Frame: min(len(frames), expectedCount), Kind: "frame_count", GoValue: fmt.Sprintf("%d", len(frames)), Dav1dValue: fmt.Sprintf("%d", expectedCount)}
		return result
	}
	for _, expected := range vector.ExpectedFrameMD5 {
		if expectedCount > 0 && expected.Index >= expectedCount {
			continue
		}
		if expected.Index >= len(frames) {
			result.Passed = false
			result.FirstDifference = &Difference{Frame: expected.Index, Kind: "missing_frame", GoValue: "missing", Dav1dValue: expected.FrameMD5}
			return result
		}
		actual := frames[expected.Index]
		if actual.Width != expected.Width || actual.Height != expected.Height {
			result.Passed = false
			result.FirstDifference = &Difference{Frame: expected.Index, Kind: "dimensions", GoValue: fmt.Sprintf("%dx%d", actual.Width, actual.Height), Dav1dValue: fmt.Sprintf("%dx%d", expected.Width, expected.Height)}
			return result
		}
		if !strings.EqualFold(actual.FrameMD5, expected.FrameMD5) {
			result.Passed = false
			result.FirstDifference = &Difference{Frame: expected.Index, Kind: "frame_md5", GoValue: actual.FrameMD5, Dav1dValue: expected.FrameMD5}
			return result
		}
	}
	return result
}
