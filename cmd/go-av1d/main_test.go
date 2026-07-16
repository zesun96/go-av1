package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zesun96/go-av1/pkg/av1"
	"github.com/zesun96/go-av1/pkg/ivf"
)

func TestParseInloopFilters(t *testing.T) {
	tests := []struct {
		in   string
		want av1.InloopFilter
		ok   bool
	}{
		{in: "all", want: av1.InloopFilterAll, ok: true},
		{in: "none", want: 0, ok: true},
		{in: "deblock,cdef", want: av1.InloopFilterDeblock | av1.InloopFilterCDEF, ok: true},
		{in: "restoration", want: av1.InloopFilterRestoration, ok: true},
		{in: "deblock,unknown", want: 0, ok: false},
	}
	for _, tc := range tests {
		got, err := parseInloopFilters(tc.in)
		if tc.ok && err != nil {
			t.Fatalf("%q: unexpected error: %v", tc.in, err)
		}
		if !tc.ok {
			if err == nil {
				t.Fatalf("%q: expected error", tc.in)
			}
			continue
		}
		if got != tc.want {
			t.Fatalf("%q: got %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestWriteY4MFrameUsesIVFFrameRate(t *testing.T) {
	pic := &av1.Picture{
		Y: []byte{1, 2, 3, 4}, U: []byte{5}, V: []byte{6},
		StrideY: 2, StrideUV: 1, Width: 2, Height: 2, Chroma: av1.Chroma420,
	}
	var out bytes.Buffer
	writeY4MFrame(&out, pic, true, ivf.FileHeader{TimebaseNum: 90000, TimebaseDen: 1})
	if !strings.HasPrefix(out.String(), "YUV4MPEG2 W2 H2 F90000:1 ") {
		t.Fatalf("unexpected Y4M header: %q", strings.SplitN(out.String(), "\n", 2)[0])
	}
}
