package durable

import (
	"fmt"
	"strings"
)

// Version and feature negotiation (contract §4.3) and engine identity (§4.2).
//
// V1 uses named features rather than a monotonic minimum-version number. A
// monotonic number requires features to be linearly ordered, and with several
// bindings developed in parallel they are not: a client can implement B
// without A, and under a version floor it would be locked out of an object
// that only ever used B. Delta Lake moved off version numbers onto table
// features for exactly this reason.
//
// The asymmetry between the two feature lists is the whole point. An unknown
// *reader* feature means bytes in this object cannot be interpreted, so the
// object does not open at all. An unknown *writer* feature means only that
// writing correctly needs something this build cannot do — reading is still
// sound, so a read-only open is allowed and only the lease is refused.

func unknownFeatures(features, known []string) []string {
	var missing []string
	for _, f := range features {
		found := false
		for _, k := range known {
			if f == k {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, f)
		}
	}
	return missing
}

// assertReadable gates a read: it refuses a protocol version above this
// baseline, or any unrecognised reader feature, naming the offenders so the
// operator knows which build they need.
func assertReadable(head Head) error {
	if head.Protocol.Version > ProtocolVersion {
		return &Error{
			Category: CategoryProtocolUnsupported,
			Message: fmt.Sprintf("durable: object uses protocol version %d, this build implements %d",
				head.Protocol.Version, ProtocolVersion),
		}
	}
	if missing := unknownFeatures(head.Protocol.ReaderFeatures, KnownReaderFeatures); len(missing) > 0 {
		return &Error{
			Category: CategoryProtocolUnsupported,
			Message: fmt.Sprintf("durable: object requires reader features this build does not implement: %s",
				strings.Join(missing, ", ")),
			Features: missing,
		}
	}
	return nil
}

// assertWritable gates taking the writer lease. It assumes assertReadable has
// already passed, and only adds the writer-side feature check.
func assertWritable(head Head) error {
	if missing := unknownFeatures(head.Protocol.WriterFeatures, KnownWriterFeatures); len(missing) > 0 {
		return &Error{
			Category: CategoryProtocolUnsupported,
			Message: fmt.Sprintf("durable: object requires writer features this build does not implement: %s; "+
				"it can still be opened read-only", strings.Join(missing, ", ")),
			Features: missing,
		}
	}
	return nil
}

// runningEngine is what the engine in this process can offer, for the
// compatibility gate.
type runningEngine struct {
	// version is chdb_version() of the loaded library.
	version string
	// backupFormat is the highest archive-format generation it can restore.
	backupFormat int
}

// assertEngineCompatible applies the frozen engine gate:
//
//	backup_format > reader baseline   -> engine_incompatible
//	running_version < min_reader      -> engine_incompatible
//	otherwise                         -> open
//
// Note what is deliberately absent: a comparison against engine.version. That
// field records which build produced the object, for diagnosis, and is
// explicitly not a gate. An exact match would refuse every later release,
// which is the opposite of what the compatibility promise says — a newer
// chdb-core restores full backups made by an earlier one, so an object written
// by 26.7.2-rc.2 opens on 26.7.3 and everything after it.
//
// The two checks guard different failures and neither subsumes the other.
// min_reader catches a reader that is simply too old. backup_format is the
// escape hatch for the day the promise itself is withdrawn: version numbers
// keep increasing whether or not the format still works, so a broken format
// needs its own signal, or a reader would compare a larger version, conclude
// it is fine, and discover otherwise partway through RESTORE.
func assertEngineCompatible(head Head, running runningEngine) error {
	if head.Engine.Name != EngineName {
		return &Error{
			Category: CategoryEngineIncompatible,
			Message: fmt.Sprintf("durable: object was written by engine %q, not %q",
				head.Engine.Name, EngineName),
			Expected: head.Engine.Name,
			Actual:   EngineName,
		}
	}
	if head.Engine.BackupFormat > running.backupFormat {
		return &Error{
			Category: CategoryEngineIncompatible,
			Message: fmt.Sprintf("durable: object uses archive format generation %d, and this engine "+
				"restores up to %d; a newer chdb-core is required, since the format generation only "+
				"moves when older archives can no longer be restored",
				head.Engine.BackupFormat, running.backupFormat),
			Expected: fmt.Sprintf("%d", head.Engine.BackupFormat),
			Actual:   fmt.Sprintf("%d", running.backupFormat),
		}
	}
	cmp, err := CompareEngineVersions(running.version, head.Engine.MinReader)
	if err != nil {
		return err
	}
	if cmp < 0 {
		return &Error{
			Category: CategoryEngineIncompatible,
			Message: fmt.Sprintf("durable: object requires chdb %s or later to read, and this process "+
				"runs %s (written by %s)",
				head.Engine.MinReader, running.version, head.Engine.Version),
			Expected: head.Engine.MinReader,
			Actual:   running.version,
		}
	}
	return nil
}

// raiseCompatibilityFloor records this engine's requirements on a head being
// written, without ever relaxing what is already stored.
//
// The protocol says a writer must not lower either requirement. Taking the
// maximum rather than overwriting is what enforces that: an object whose base
// was produced by a newer engine keeps demanding that engine even if an older
// one somehow reaches a write, instead of quietly advertising itself as
// readable by a build that cannot restore it.
func raiseCompatibilityFloor(head Head, running runningEngine) (Head, error) {
	out := head
	out.Engine.Version = running.version
	if running.backupFormat > out.Engine.BackupFormat {
		out.Engine.BackupFormat = running.backupFormat
	}
	minReader, err := maxEngineVersion(out.Engine.MinReader, running.version)
	if err != nil {
		return Head{}, err
	}
	out.Engine.MinReader = minReader
	return out, nil
}
