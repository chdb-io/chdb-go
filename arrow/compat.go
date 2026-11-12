//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

// ErrArrowEngineIncompatible reports that libchdb's symbols or release are
// older than the tested Arrow ownership contract. The main module's existing
// non-Arrow queries remain available with those engines.
var ErrArrowEngineIncompatible = errors.New("chdbarrow: requires chdb-core v26.11.0-rc.3 or later with the Arrow C ABI")

func requireArrowEngine(c nativearrow.Capabilities, input, table, reader bool) error {
	if !compatibleVersion(c.EngineVersion) || (input && !c.Input) ||
		(table && !c.TableOutput) || (reader && !c.ReaderOutput) {
		return fmt.Errorf("%w; loaded %q (input=%t table=%t reader=%t)",
			ErrArrowEngineIncompatible, c.EngineVersion, c.Input, c.TableOutput, c.ReaderOutput)
	}
	return nil
}

func compatibleVersion(version string) bool {
	version = strings.TrimPrefix(version, "v")
	core, suffix, hasSuffix := strings.Cut(version, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	var got [3]int
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return false
		}
		got[i] = n
	}
	minimum := [3]int{26, 11, 0}
	for i := range got {
		if got[i] > minimum[i] {
			return !hasSuffix || validRC(suffix)
		}
		if got[i] < minimum[i] {
			return false
		}
	}
	return !hasSuffix || validRC(suffix) && rcNumber(suffix) >= 3
}

func validRC(suffix string) bool { return rcNumber(suffix) >= 0 }

func rcNumber(suffix string) int {
	value, ok := strings.CutPrefix(suffix, "rc.")
	if !ok || value == "" {
		return -1
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return -1
	}
	return n
}
