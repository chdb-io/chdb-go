//go:build darwin && arm64

// Package engine carries the libchdb build for darwin/arm64 so that a Go
// program depending on it needs nothing installed on the machine.
//
// The payload lives in data/ as zstd-compressed parts. Splitting is not
// optional: GitHub rejects any file over 100 MiB and the largest platform is
// within a couple of megabytes of that. Regenerate with
// scripts/package-engine.sh.
//
// Only tagged releases of this module carry the payload. The default branch
// keeps it out so that cloning the repository does not pull a few hundred
// megabytes per engine version, none of which git can delta-compress.
package engine

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
)

//go:embed data
var payload embed.FS

// ErrNoPayload reports a build made from a revision that carries no engine.
var ErrNoPayload = errors.New("chdb engine: this build carries no payload, use a tagged release of this module")

// Open returns the decompressed library bytes. The caller writes them to disk;
// a shared library can only be loaded from a real file.
func Open() (io.ReadCloser, error) {
	parts, err := partNames()
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, ErrNoPayload
	}

	readers := make([]io.Reader, 0, len(parts))
	for _, name := range parts {
		f, err := payload.Open(name)
		if err != nil {
			return nil, fmt.Errorf("chdb engine: %s: %w", name, err)
		}
		readers = append(readers, f)
	}

	decoder, err := zstd.NewReader(io.MultiReader(readers...))
	if err != nil {
		return nil, fmt.Errorf("chdb engine: %w", err)
	}
	return decoder.IOReadCloser(), nil
}

// partNames returns the payload parts in the order they must be concatenated.
// Lexical order is the right order because the generator numbers parts with a
// fixed width.
func partNames() ([]string, error) {
	entries, err := fs.ReadDir(payload, "data")
	if err != nil {
		return nil, fmt.Errorf("chdb engine: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "libchdb.zst.part") {
			names = append(names, "data/"+entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
