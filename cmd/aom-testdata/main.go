// Command aom-testdata downloads individually named libaom test vectors.
package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const defaultBaseURL = "https://storage.googleapis.com/aom-test-data"

type vector struct {
	Name string
	SHA1 string
}

func main() {
	manifest := flag.String("manifest", "", "path to AOM test/test-data.sha1")
	out := flag.String("out", "test/conformance/vectors/aom", "download directory")
	baseURL := flag.String("base-url", defaultBaseURL, "test data bucket URL")
	include := flag.String("include", `(?i)\.ivf$`, "regular expression selecting filenames")
	list := flag.Bool("list", false, "list selected files without downloading")
	flag.Parse()
	if *manifest == "" {
		fmt.Fprintln(os.Stderr, "usage: aom-testdata -manifest path/to/aom/test/test-data.sha1 [-out directory]")
		os.Exit(2)
	}
	pattern, err := regexp.Compile(*include)
	if err != nil {
		fmt.Fprintf(os.Stderr, "include pattern: %v\n", err)
		os.Exit(2)
	}
	f, err := os.Open(*manifest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open manifest: %v\n", err)
		os.Exit(1)
	}
	vectors, err := parseManifest(f, pattern)
	_ = f.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *list {
		for _, item := range vectors {
			fmt.Printf("%s  %s\n", item.SHA1, item.Name)
		}
		return
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	if err := downloadVectors(context.Background(), client, *baseURL, *out, vectors, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var manifestLine = regexp.MustCompile(`^([0-9a-fA-F]{40})\s+\*?(.+?)\s*$`)

func parseManifest(r io.Reader, include *regexp.Regexp) ([]vector, error) {
	var vectors []vector
	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		match := manifestLine.FindStringSubmatch(text)
		if match == nil {
			return nil, fmt.Errorf("aom-testdata: invalid manifest line %d", line)
		}
		name := match[2]
		if filepath.Base(name) != name || name == "." || name == ".." {
			return nil, fmt.Errorf("aom-testdata: unsafe filename %q", name)
		}
		if include.MatchString(name) {
			vectors = append(vectors, vector{Name: name, SHA1: strings.ToLower(match[1])})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("aom-testdata: read manifest: %w", err)
	}
	if len(vectors) == 0 {
		return nil, errors.New("aom-testdata: no files matched the include pattern")
	}
	return vectors, nil
}

func downloadVectors(ctx context.Context, client *http.Client, baseURL, outDir string, vectors []vector, progress io.Writer) error {
	baseURL = strings.TrimRight(baseURL, "/")
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return fmt.Errorf("aom-testdata: invalid base URL: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("aom-testdata: create output directory: %w", err)
	}
	for i, item := range vectors {
		destination := filepath.Join(outDir, item.Name)
		if existing, err := fileSHA1(destination); err == nil {
			if existing == item.SHA1 {
				fmt.Fprintf(progress, "[%d/%d] %s: verified\n", i+1, len(vectors), item.Name)
				continue
			}
			return fmt.Errorf("aom-testdata: existing %s has SHA-1 %s, expected %s", destination, existing, item.SHA1)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fmt.Fprintf(progress, "[%d/%d] %s: downloading\n", i+1, len(vectors), item.Name)
		if err := downloadOne(ctx, client, baseURL+"/"+url.PathEscape(item.Name), destination, item.SHA1); err != nil {
			return err
		}
	}
	return nil
}

func downloadOne(ctx context.Context, client *http.Client, sourceURL, destination, expected string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return fmt.Errorf("aom-testdata: create request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("aom-testdata: download %s: %w", filepath.Base(destination), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("aom-testdata: download %s: HTTP %s", filepath.Base(destination), resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".aom-testdata-*")
	if err != nil {
		return fmt.Errorf("aom-testdata: create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	h := sha1.New()
	_, copyErr := io.Copy(io.MultiWriter(tmp, h), resp.Body)
	closeErr := tmp.Close()
	if copyErr != nil {
		return fmt.Errorf("aom-testdata: write %s: %w", filepath.Base(destination), copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("aom-testdata: close %s: %w", filepath.Base(destination), closeErr)
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("aom-testdata: downloaded %s has SHA-1 %s, expected %s", filepath.Base(destination), actual, expected)
	}
	if err := os.Rename(tmpName, destination); err != nil {
		return fmt.Errorf("aom-testdata: install %s: %w", filepath.Base(destination), err)
	}
	return nil
}

func fileSHA1(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("aom-testdata: hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
