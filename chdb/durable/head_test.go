package durable

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A head as another binding would write it: different key order, extra
// whitespace, and two fields this build has never heard of.
const foreignHead = `{
  "manifest": {
    "seq": 9,
    "db": "mem",
    "wal": [
      {"sha256": "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
       "size": 127, "key": "wal/3-9-acde5678.jsonl"}
    ],
    "base": {"key": "checkpoints/3-8-acde1234.tar.gz", "size": 1048576,
             "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
    "future_manifest_field": {"nested": [1, 2, 3]}
  },
  "lease": {"expires_at": 1788230400.5, "owner": "worker-visible-name",
            "instance": "unique-live-instance-id", "generation": 3},
  "engine": {"name": "chdb", "version": "26.7.2-rc.2", "backup_format": 1,
             "min_reader": "26.7.2-rc.2"},
  "protocol": {"version": 1, "reader_features": [], "writer_features": []},
  "top_level_future_field": "keep me"
}`

func mustParse(t *testing.T, text string) (Head, map[string]any) {
	t.Helper()
	head, raw, err := parseHead([]byte(text))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return head, raw
}

func TestParseHeadReadsAnotherBindingsSpelling(t *testing.T) {
	head, _ := mustParse(t, foreignHead)

	if head.Manifest.DB != "mem" || head.Manifest.Seq != 9 {
		t.Fatalf("manifest is %+v", head.Manifest)
	}
	if head.Manifest.Base == nil || head.Manifest.Base.Size != 1048576 {
		t.Fatalf("base is %+v", head.Manifest.Base)
	}
	if len(head.Manifest.WAL) != 1 || head.Manifest.WAL[0].Key != "wal/3-9-acde5678.jsonl" {
		t.Fatalf("wal is %+v", head.Manifest.WAL)
	}
	if head.Lease.Generation != 3 || head.Lease.Owner == nil || *head.Lease.Owner != "worker-visible-name" {
		t.Fatalf("lease is %+v", head.Lease)
	}
	if head.Engine.MinReader != "26.7.2-rc.2" || head.Engine.BackupFormat != 1 {
		t.Fatalf("engine is %+v", head.Engine)
	}
}

// The whole named-feature mechanism rests on this: a writer that dropped
// fields it does not recognise would delete a future revision's state the
// first time an older build touched the object.
func TestSerializeHeadKeepsUnknownFields(t *testing.T) {
	head, raw := mustParse(t, foreignHead)

	// Move the object on the way a flush would, then write it back.
	head.Manifest.Seq = 10
	head.Lease.ExpiresAt = floatPtr(1788230500)

	data, err := serializeHead(head, raw)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("the written head is not JSON: %v", err)
	}

	if out["top_level_future_field"] != "keep me" {
		t.Errorf("a top-level unknown field was dropped: %v", out)
	}
	manifest, ok := out["manifest"].(map[string]any)
	if !ok {
		t.Fatalf("manifest is %T", out["manifest"])
	}
	if _, ok := manifest["future_manifest_field"]; !ok {
		t.Errorf("an unknown field inside manifest was dropped: %v", manifest)
	}
	if manifest["seq"].(float64) != 10 {
		t.Errorf("seq was not updated: %v", manifest["seq"])
	}

	// And it round-trips through the parser again, so the write is not merely
	// JSON but a valid head.
	if _, _, err := parseHead(data); err != nil {
		t.Fatalf("the written head does not parse: %v", err)
	}
}

// A large integer inside an unknown field must survive. Decoding numbers as
// float64 would rewrite it, which is the failure round-tripping exists to
// prevent and the least visible one.
func TestSerializeHeadKeepsUnknownNumbersExactly(t *testing.T) {
	text := strings.Replace(foreignHead, `"top_level_future_field": "keep me"`,
		`"future_counter": 9007199254740993, "future_precise": 0.10000000000000000555`, 1)
	head, raw := mustParse(t, text)
	data, err := serializeHead(head, raw)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	for _, want := range []string{"9007199254740993", "0.10000000000000000555"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%s was not written back verbatim:\n%s", want, data)
		}
	}
}

// A missing protocol block is the documented default so that an object written
// before the block existed stays readable.
func TestParseHeadDefaultsAMissingProtocol(t *testing.T) {
	text := strings.Replace(foreignHead,
		`"protocol": {"version": 1, "reader_features": [], "writer_features": []},`, "", 1)
	head, _ := mustParse(t, text)
	if head.Protocol.Version != ProtocolVersion {
		t.Fatalf("version is %d, want the baseline %d", head.Protocol.Version, ProtocolVersion)
	}
	if len(head.Protocol.ReaderFeatures) != 0 || len(head.Protocol.WriterFeatures) != 0 {
		t.Fatalf("features are %+v, want empty", head.Protocol)
	}
}

// min_reader defaults to the producer version, which reproduces the old
// exact-match behaviour's lower bound. Defaulting it lower would retroactively
// widen an object's audience on the strength of a field its writer never
// wrote.
func TestParseHeadDefaultsMinReaderToTheProducer(t *testing.T) {
	text := strings.Replace(foreignHead, `, "min_reader": "26.7.2-rc.2"`, "", 1)
	text = strings.Replace(text, `, "backup_format": 1`, "", 1)
	head, _ := mustParse(t, text)
	if head.Engine.MinReader != "26.7.2-rc.2" {
		t.Errorf("min_reader is %q, want the producer version", head.Engine.MinReader)
	}
	if head.Engine.BackupFormat != BackupFormatBaseline {
		t.Errorf("backup_format is %d, want the baseline", head.Engine.BackupFormat)
	}
}

func TestParseHeadRefusesCorruptDocuments(t *testing.T) {
	cases := []struct {
		name string
		edit func(string) string
	}{
		{"not an object", func(string) string { return `[]` }},
		{"not JSON", func(string) string { return `{oops` }},
		{"two documents", func(s string) string { return s + s }},
		{"no engine", func(s string) string {
			return strings.Replace(s, `"engine": {"name": "chdb", "version": "26.7.2-rc.2", "backup_format": 1,
             "min_reader": "26.7.2-rc.2"},`, "", 1)
		}},
		{"no lease", func(s string) string {
			return strings.Replace(s, `"lease": {"expires_at": 1788230400.5, "owner": "worker-visible-name",
            "instance": "unique-live-instance-id", "generation": 3},`, "", 1)
		}},
		// Half a lease implies a different answer to "may I take this over"
		// than either whole one, and guessing is how two writers end up
		// believing they are the one writer.
		{"half-released lease", func(s string) string {
			return strings.Replace(s, `"instance": "unique-live-instance-id"`, `"instance": null`, 1)
		}},
		{"lease missing expires_at", func(s string) string {
			return strings.Replace(s, `"expires_at": 1788230400.5,`, "", 1)
		}},
		{"nulled wal", func(s string) string {
			return strings.Replace(s, `"wal": [`, `"wal": null, "ignored": [`, 1)
		}},
		{"empty db", func(s string) string {
			return strings.Replace(s, `"db": "mem"`, `"db": ""`, 1)
		}},
		{"negative seq", func(s string) string {
			return strings.Replace(s, `"seq": 9`, `"seq": -1`, 1)
		}},
		{"seq beyond the safe integer range", func(s string) string {
			return strings.Replace(s, `"seq": 9`, `"seq": 9007199254740993`, 1)
		}},
		{"uppercase sha256", func(s string) string {
			return strings.Replace(s, "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
				"ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789", 1)
		}},
		{"short sha256", func(s string) string {
			return strings.Replace(s, "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
				"abcdef", 1)
		}},
		// A key steering a read outside the object is refused where it is
		// read, not where it is used.
		{"traversing wal key", func(s string) string {
			return strings.Replace(s, `"key": "wal/3-9-acde5678.jsonl"`,
				`"key": "../../etc/passwd"`, 1)
		}},
		{"absolute wal key", func(s string) string {
			return strings.Replace(s, `"key": "wal/3-9-acde5678.jsonl"`, `"key": "/etc/passwd"`, 1)
		}},
		{"size as a string", func(s string) string {
			return strings.Replace(s, `"size": 127`, `"size": "127"`, 1)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseHead([]byte(tc.edit(foreignHead)))
			if err == nil {
				t.Fatal("a corrupt head must not parse")
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("category is %q, want corrupt: %v", CategoryOf(err), err)
			}
		})
	}
}

func TestParseHeadRefusesNonUTF8(t *testing.T) {
	data := append([]byte(nil), foreignHead...)
	data = append(data, 0xff)
	if _, _, err := parseHead(data); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("category is %q, want corrupt", CategoryOf(err))
	}
}

func TestHeadLimits(t *testing.T) {
	oversized := append([]byte(`{"padding":"`), make([]byte, MaxHeadBytes)...)
	if _, _, err := parseHead(oversized); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("reading an oversized head gave %q, want limit_exceeded", CategoryOf(err))
	}

	// A writer must be stopped before it publishes a head nobody can read
	// back, because V1 has no way to remove one.
	head, _ := mustParse(t, foreignHead)
	ref := head.Manifest.WAL[0]
	// One reference encodes to a bit over 100 bytes, so this many is safely
	// past a 1 MiB head without depending on the exact figure.
	for i := 0; i < 20000; i++ {
		head.Manifest.WAL = append(head.Manifest.WAL, ref)
	}
	if _, err := serializeHead(head, nil); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("writing an oversized head gave %q, want limit_exceeded", CategoryOf(err))
	}
}

// A released lease is exactly three nulls, and that is what the next acquirer
// looks for.
func TestSerializeHeadWritesAReleasedLeaseAsNulls(t *testing.T) {
	head, raw := mustParse(t, foreignHead)
	head.Lease = Lease{Generation: head.Lease.Generation}
	data, err := serializeHead(head, raw)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, _, err := parseHead(data)
	if err != nil {
		t.Fatalf("a released lease does not parse: %v", err)
	}
	if reparsed.Lease.Owner != nil || reparsed.Lease.Instance != nil || reparsed.Lease.ExpiresAt != nil {
		t.Fatalf("lease is %+v, want fully released", reparsed.Lease)
	}
	if reparsed.Lease.Generation != 3 {
		t.Fatalf("generation is %d, want 3 — a release does not reset it", reparsed.Lease.Generation)
	}
}

// An expiry is a timestamp an operator reads. Written in exponent form it is
// the same number and a worse document, and json.Number is what keeps
// encoding/json from choosing the exponent for us.
func TestSerializeHeadWritesExpiryWithoutAnExponent(t *testing.T) {
	head, raw := mustParse(t, foreignHead)
	head.Lease.ExpiresAt = floatPtr(1788230400)
	data, err := serializeHead(head, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "1788230400") || strings.Contains(string(data), "e+") {
		t.Fatalf("expiry was written oddly:\n%s", data)
	}
}

func TestSerializeHeadRefusesADocumentItWouldReject(t *testing.T) {
	head, _ := mustParse(t, foreignHead)
	head.Manifest.DB = ""
	if _, err := serializeHead(head, nil); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("category is %q, want corrupt", CategoryOf(err))
	}
}

func TestColdHeadIsReadableByItself(t *testing.T) {
	head := coldHead("mem", "26.7.2-rc.2", BackupFormatBaseline)
	data, err := serializeHead(head, nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, err := parseHead(data)
	if err != nil {
		t.Fatalf("a cold head does not parse: %v", err)
	}
	if parsed.Manifest.Base != nil || len(parsed.Manifest.WAL) != 0 || parsed.Manifest.Seq != 0 {
		t.Fatalf("a cold manifest is %+v", parsed.Manifest)
	}
	if parsed.Lease.Generation != 1 {
		t.Fatalf("a cold lease starts at generation %d, want 1", parsed.Lease.Generation)
	}
}
