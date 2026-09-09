package durable

import (
	"errors"
	"testing"
)

func TestEngineVersionPrecedence(t *testing.T) {
	// The ordering that matters, in order. Each entry must precede the next.
	ascending := []string{
		"26.5.0",
		"26.7.0",
		"26.7.1-rc.1",
		"26.7.2-rc.1",
		"26.7.2-rc.2",
		"26.7.2-rc.2.1",
		"26.7.2",
		"26.7.3",
		"26.10.0",
		"27.0.0",
	}
	for i := 0; i < len(ascending)-1; i++ {
		lower, higher := ascending[i], ascending[i+1]
		cmp, err := CompareEngineVersions(lower, higher)
		if err != nil {
			t.Fatalf("comparing %s and %s: %v", lower, higher, err)
		}
		if cmp >= 0 {
			t.Errorf("%s should precede %s, got %d", lower, higher, cmp)
		}
		reverse, err := CompareEngineVersions(higher, lower)
		if err != nil {
			t.Fatal(err)
		}
		if reverse <= 0 {
			t.Errorf("%s should follow %s, got %d", higher, lower, reverse)
		}
	}
}

// A missing component is zero, so a two-part version and its three-part
// spelling are the same release. Ordering them apart would refuse an object
// over a difference in how its producer spelled its own version.
func TestEngineVersionTreatsMissingComponentsAsZero(t *testing.T) {
	cmp, err := CompareEngineVersions("26.7", "26.7.0")
	if err != nil {
		t.Fatal(err)
	}
	if cmp != 0 {
		t.Fatalf("26.7 and 26.7.0 are the same release, got %d", cmp)
	}
}

// Build metadata takes no part in precedence, per semver.
func TestEngineVersionIgnoresBuildMetadata(t *testing.T) {
	cmp, err := CompareEngineVersions("26.7.2+build.7", "26.7.2")
	if err != nil {
		t.Fatal(err)
	}
	if cmp != 0 {
		t.Fatalf("build metadata must not order versions, got %d", cmp)
	}
}

// Lexicographic comparison gets both of these backwards, which is the reason
// this file exists rather than a strings.Compare.
func TestEngineVersionIsNotLexicographic(t *testing.T) {
	for _, tc := range []struct{ lower, higher string }{
		{"26.7.0", "26.10.0"},
		{"26.7.2-rc.2", "26.7.2"},
	} {
		if tc.lower < tc.higher {
			t.Fatalf("test case %s < %s is not the lexicographic trap it is meant to be",
				tc.lower, tc.higher)
		}
		cmp, err := CompareEngineVersions(tc.lower, tc.higher)
		if err != nil {
			t.Fatal(err)
		}
		if cmp >= 0 {
			t.Errorf("%s should precede %s by release precedence", tc.lower, tc.higher)
		}
	}
}

// An unrecognised version is not evidence of compatibility, and treating it as
// one is how a reader restores an archive from a release nothing has tested it
// against.
func TestEngineVersionRefusesWhatItCannotOrder(t *testing.T) {
	for _, bad := range []string{"", "latest", "v26.7.2", "26.7.2-", "26..7"} {
		if _, err := CompareEngineVersions(bad, "26.7.2"); err == nil {
			t.Errorf("comparing %q should have been refused", bad)
		} else if !errors.Is(err, ErrEngineIncompatible) {
			t.Errorf("comparing %q gave category %q, want engine_incompatible",
				bad, CategoryOf(err))
		}
	}
}

func TestMaxEngineVersionTakesTheLater(t *testing.T) {
	got, err := maxEngineVersion("26.7.2-rc.2", "26.7.3")
	if err != nil {
		t.Fatal(err)
	}
	if got != "26.7.3" {
		t.Fatalf("max is %s, want 26.7.3", got)
	}
	// Equal releases return the first argument, which is the stored value —
	// so an equal comparison never rewrites what is already in the head.
	got, err = maxEngineVersion("26.7.2", "26.7.2")
	if err != nil {
		t.Fatal(err)
	}
	if got != "26.7.2" {
		t.Fatalf("max of equals is %s, want 26.7.2", got)
	}
}
