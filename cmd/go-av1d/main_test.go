package main

import (
	"testing"

	"github.com/zesun96/go-av1/pkg/av1"
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
