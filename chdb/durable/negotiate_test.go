package durable

import (
	"errors"
	"testing"
)

func headWith(engine EngineIdentity, protocol Protocol) Head {
	head := coldHead("mem", engine.Version, engine.BackupFormat)
	head.Engine = engine
	head.Protocol = protocol
	return head
}

func v1Protocol() Protocol {
	return Protocol{Version: 1, ReaderFeatures: []string{}, WriterFeatures: []string{}}
}

// The case the whole engine gate exists for: an object written by one release
// and opened by a later one. An exact-match check would refuse this, which is
// the opposite of what the compatibility promise says.
func TestEngineGateOpensAnObjectFromAnEarlierRelease(t *testing.T) {
	head := headWith(EngineIdentity{
		Name: EngineName, Version: "26.7.2-rc.2", BackupFormat: 1, MinReader: "26.7.2-rc.2",
	}, v1Protocol())

	for _, reader := range []string{"26.7.2-rc.2", "26.7.2", "26.7.3", "26.8.0", "27.0.0"} {
		if err := assertEngineCompatible(head, runningEngine{version: reader, backupFormat: 1}); err != nil {
			t.Errorf("chdb %s should be able to read an object written by 26.7.2-rc.2: %v", reader, err)
		}
	}
}

func TestEngineGateRefusesAReaderBelowMinReader(t *testing.T) {
	head := headWith(EngineIdentity{
		Name: EngineName, Version: "26.8.0", BackupFormat: 1, MinReader: "26.8.0",
	}, v1Protocol())

	err := assertEngineCompatible(head, runningEngine{version: "26.7.2-rc.2", backupFormat: 1})
	if !errors.Is(err, ErrEngineIncompatible) {
		t.Fatalf("category is %q, want engine_incompatible", CategoryOf(err))
	}
	var e *Error
	errors.As(err, &e)
	if e.Expected != "26.8.0" || e.Actual != "26.7.2-rc.2" {
		t.Errorf("the error should name both sides, got expected=%q actual=%q", e.Expected, e.Actual)
	}
}

// A newer archive format is the one signal that the compatibility promise has
// been withdrawn. Without it a reader compares a larger version, concludes it
// is fine, and discovers otherwise partway through RESTORE.
func TestEngineGateRefusesANewerArchiveFormat(t *testing.T) {
	head := headWith(EngineIdentity{
		Name: EngineName, Version: "27.0.0", BackupFormat: 2, MinReader: "26.7.2-rc.2",
	}, v1Protocol())

	err := assertEngineCompatible(head, runningEngine{version: "26.7.2-rc.2", backupFormat: 1})
	if !errors.Is(err, ErrEngineIncompatible) {
		t.Fatalf("category is %q, want engine_incompatible", CategoryOf(err))
	}
}

func TestEngineGateRefusesAnotherEngine(t *testing.T) {
	head := headWith(EngineIdentity{
		Name: "duckdb", Version: "1.0.0", BackupFormat: 1, MinReader: "1.0.0",
	}, v1Protocol())

	if err := assertEngineCompatible(head, runningEngine{version: "26.7.2-rc.2", backupFormat: 1}); !errors.Is(err, ErrEngineIncompatible) {
		t.Fatalf("category is %q, want engine_incompatible", CategoryOf(err))
	}
}

func TestProtocolGateRefusesAFutureVersion(t *testing.T) {
	head := headWith(EngineIdentity{
		Name: EngineName, Version: "26.7.2-rc.2", BackupFormat: 1, MinReader: "26.7.2-rc.2",
	}, Protocol{Version: 2, ReaderFeatures: []string{}, WriterFeatures: []string{}})

	if err := assertReadable(head); !errors.Is(err, ErrProtocolUnsupported) {
		t.Fatalf("category is %q, want protocol_unsupported", CategoryOf(err))
	}
}

// The asymmetry is the point: an unknown reader feature means bytes in the
// object cannot be interpreted at all, while an unknown writer feature only
// means writing correctly needs something this build cannot do.
func TestFeatureGatesAreAsymmetric(t *testing.T) {
	readerSide := headWith(EngineIdentity{
		Name: EngineName, Version: "26.7.2-rc.2", BackupFormat: 1, MinReader: "26.7.2-rc.2",
	}, Protocol{Version: 1, ReaderFeatures: []string{"preamble"}, WriterFeatures: []string{}})

	err := assertReadable(readerSide)
	if !errors.Is(err, ErrProtocolUnsupported) {
		t.Fatalf("an unknown reader feature must refuse the open, got %q", CategoryOf(err))
	}
	var e *Error
	errors.As(err, &e)
	if len(e.Features) != 1 || e.Features[0] != "preamble" {
		t.Errorf("the error should name the feature, got %v", e.Features)
	}

	writerSide := headWith(EngineIdentity{
		Name: EngineName, Version: "26.7.2-rc.2", BackupFormat: 1, MinReader: "26.7.2-rc.2",
	}, Protocol{Version: 1, ReaderFeatures: []string{}, WriterFeatures: []string{"data-wal"}})

	if err := assertReadable(writerSide); err != nil {
		t.Fatalf("an unknown writer feature must still read: %v", err)
	}
	if err := assertWritable(writerSide); !errors.Is(err, ErrProtocolUnsupported) {
		t.Fatalf("an unknown writer feature must refuse the lease, got %q", CategoryOf(err))
	}
}

// A writer records its own requirements on every head write and never lowers
// what is already stored. Overwriting rather than taking the maximum would let
// an older writer advertise an object as readable by a build that cannot
// restore its base.
func TestCompatibilityFloorOnlyRises(t *testing.T) {
	head := headWith(EngineIdentity{
		Name: EngineName, Version: "26.8.0", BackupFormat: 2, MinReader: "26.8.0",
	}, v1Protocol())

	raised, err := raiseCompatibilityFloor(head, runningEngine{version: "26.7.2-rc.2", backupFormat: 1})
	if err != nil {
		t.Fatal(err)
	}
	if raised.Engine.MinReader != "26.8.0" {
		t.Errorf("min_reader dropped to %q; an older writer must not lower it", raised.Engine.MinReader)
	}
	if raised.Engine.BackupFormat != 2 {
		t.Errorf("backup_format dropped to %d; an older writer must not lower it", raised.Engine.BackupFormat)
	}
	// engine.version is a record of who wrote last, not a floor, so it does
	// move.
	if raised.Engine.Version != "26.7.2-rc.2" {
		t.Errorf("engine.version is %q, want the writer that just wrote", raised.Engine.Version)
	}

	// And a newer writer does raise it.
	raised, err = raiseCompatibilityFloor(head, runningEngine{version: "27.0.0", backupFormat: 3})
	if err != nil {
		t.Fatal(err)
	}
	if raised.Engine.MinReader != "27.0.0" || raised.Engine.BackupFormat != 3 {
		t.Errorf("a newer writer should raise the floor, got %+v", raised.Engine)
	}
}
