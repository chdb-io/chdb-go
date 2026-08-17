// Command enginetag prints the version to tag a platform engine module with.
//
//	go run ./scripts/enginetag v26.7.0        ->  v0.260700.1
//	go run ./scripts/enginetag v26.7.0 2      ->  v0.260700.2
//	go run ./scripts/enginetag v26.7.3-rc.1   ->  v0.260703.0-rc.1.1
//
// It exists so scripts/package-engine.sh can print the exact tag it derived
// instead of describing the rule and leaving the arithmetic to whoever is
// publishing. The rule itself lives in internal/enginetag, which is where it is
// tested; nothing here reimplements it.
//
// It also checks a tag against the module it names, which is the one mistake the
// rest of this cannot prevent — a tag typed by hand rather than copied:
//
//	go run ./scripts/enginetag -verify lib/darwin-arm64/v0.260700.1
package main

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"

	"github.com/chdb-io/chdb-go/v2/internal/enginetag"
)

func main() {
	args := os.Args[1:]
	if len(args) == 2 && args[0] == "-verify" {
		if err := verify(args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: enginetag <chdb-core-tag> [packaging counter, default 1]")
		fmt.Fprintln(os.Stderr, "       enginetag -verify lib/<platform>/<module version>")
		os.Exit(2)
	}

	counter := 1
	if len(args) == 2 {
		n, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "packaging counter %q is not a number\n", args[1])
			os.Exit(2)
		}
		counter = n
	}

	version, err := enginetag.Module(args[0], counter)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(version)
}

var versionConstRe = regexp.MustCompile(`(?m)^\s*Version\s*=\s*"([^"]*)"`)

// verify checks that a lib/<platform>/<version> tag names the engine the module
// at that path was actually generated from. The two are written by one run of
// package-engine.sh and cannot disagree if the printed command was used, so what
// this catches is the tag being retyped — which matters more here than it would
// elsewhere, because the module proxy serves a tag's bytes forever and a wrong
// one can only be superseded, never fixed.
func verify(tag string) error {
	version := path.Base(tag)
	moddir := path.Dir(tag)
	if path.Dir(moddir) != "lib" {
		return fmt.Errorf("tag %q is not of the form lib/<platform>/<module version>", tag)
	}

	wantEngine, counter, err := enginetag.Engine(version)
	if err != nil {
		return fmt.Errorf("tag %s: %w", tag, err)
	}

	if path.Base(moddir) == "embedded" {
		return verifyDispatch(tag, moddir, version)
	}

	file := path.Join(moddir, "engine_data.go")
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	m := versionConstRe.FindSubmatch(data)
	if m == nil {
		return fmt.Errorf("%s declares no Version constant", file)
	}
	gotEngine := string(m[1])

	if gotEngine == "" {
		return fmt.Errorf("%s carries no engine — this revision has the placeholder "+
			"metadata, so it is not the one to tag %s", file, tag)
	}
	if gotEngine != wantEngine {
		return fmt.Errorf("tag %s names engine %s, and %s was generated from %s",
			tag, wantEngine, file, gotEngine)
	}

	fmt.Printf("%s: engine %s, packaging %d\n", tag, gotEngine, counter)
	return nil
}

var requireRe = regexp.MustCompile(`(?m)^\s*(github\.com/chdb-io/chdb-go/lib/[a-z0-9-]+)\s+(\S+)`)

// verifyDispatch checks lib/embedded, which carries no engine of its own — it only
// says which version of the platform modules to use. So the thing to check is that
// its tag and those versions agree: tagging it v0.260700.2 while its go.mod still
// points at v0.260700.1 would publish a version claiming an engine packaging it
// does not bring.
func verifyDispatch(tag, moddir, version string) error {
	file := path.Join(moddir, "go.mod")
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}

	found := 0
	for _, m := range requireRe.FindAllStringSubmatch(string(data), -1) {
		mod, got := m[1], m[2]
		if path.Base(mod) == "embedded" {
			continue
		}
		found++
		if got != version {
			return fmt.Errorf("tag %s does not match what %s requires:\n  %s %s\n\n"+
				"the dispatch module's version says which platform modules it brings, so the "+
				"two have to be the same", tag, file, mod, got)
		}
	}
	if found != 4 {
		return fmt.Errorf("%s requires %d platform modules, expected 4 — a platform that is "+
			"missing here is one this module silently does not cover", file, found)
	}

	fmt.Printf("%s: dispatches to 4 platform modules, all at %s\n", tag, version)
	return nil
}
