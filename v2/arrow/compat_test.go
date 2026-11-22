//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"errors"
	"testing"

	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

func TestCompatibleVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"26.9.0", false},
		{"26.11.0-rc.2", false},
		{"26.11.0-dev", false},
		{"26.11.0-rc.3", true},
		{"v26.11.0-rc.4", true},
		{"26.11.0", true},
		{"26.11.1", true},
		{"26.12.0", true},
		{"unknown", false},
	} {
		if got := compatibleVersion(tc.version); got != tc.want {
			t.Errorf("compatibleVersion(%q) = %t, want %t", tc.version, got, tc.want)
		}
	}
}

func TestRequireArrowEngineNeedsSymbolsAsWellAsRelease(t *testing.T) {
	c := nativearrow.Capabilities{EngineVersion: "26.11.0-rc.3", Input: true, TableOutput: true}
	if err := requireArrowEngine(c, true, true, false); err != nil {
		t.Fatal(err)
	}
	if err := requireArrowEngine(c, false, false, true); !errors.Is(err, ErrArrowEngineIncompatible) {
		t.Fatalf("missing stream symbols = %v", err)
	}
}
