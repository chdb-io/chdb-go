package durable

import (
	"errors"
	"strings"
	"testing"
)

func TestWALSegmentRoundTrip(t *testing.T) {
	statements := []string{
		"INSERT INTO events VALUES (1)",
		// A newline, a quote and a backslash inside SQL must survive, because
		// the format is line-oriented and these are what break a line-oriented
		// format that was not thought about.
		"INSERT INTO events VALUES ('a\nb', 'c\"d', 'e\\f')",
		// Not escaped, because the escaped form is a worse document for no
		// benefit.
		"INSERT INTO events SELECT * FROM t WHERE a < 1 AND b > 2 AND c = 'x&y'",
	}
	data, err := encodeWALSegment(statements)
	if err != nil {
		t.Fatal(err)
	}
	if data[len(data)-1] != '\n' {
		t.Fatal("a segment must end with a newline")
	}
	// encoding/json escapes <, > and & by default. The escaped form parses
	// identically, but a WAL line is the most likely thing an operator reads
	// by hand, so the encoder turns it off.
	if strings.Contains(string(data), `\u003c`) || strings.Contains(string(data), `\u0026`) {
		t.Errorf("HTML escaping is on:\n%s", data)
	}

	decoded, err := decodeWALSegment(data, "wal/1-1-abcd1234.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(statements) {
		t.Fatalf("decoded %d statements, want %d", len(decoded), len(statements))
	}
	for i := range statements {
		if decoded[i] != statements[i] {
			t.Errorf("statement %d came back as %q, want %q", i, decoded[i], statements[i])
		}
	}
}

// The budget the object layer applies before executing has to agree with the
// encoder exactly. A disagreement of one byte per line puts the segment
// boundary in the wrong place.
func TestWALLineBytesAgreesWithTheEncoder(t *testing.T) {
	statements := []string{
		"INSERT INTO t VALUES (1)",
		"INSERT INTO t VALUES ('unicode: 数据库')",
		"INSERT INTO t VALUES ('a\nb')",
	}
	var budget int64
	for _, sql := range statements {
		n, err := walLineBytes(sql)
		if err != nil {
			t.Fatal(err)
		}
		budget += n
	}
	data, err := encodeWALSegment(statements)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != budget {
		t.Fatalf("the encoder produced %d bytes and the budget said %d", len(data), budget)
	}
}

func TestWALSegmentIsEmptyForNoStatements(t *testing.T) {
	data, err := encodeWALSegment(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("an empty segment is %d bytes", len(data))
	}
	decoded, err := decodeWALSegment(data, "wal/1-1-abcd1234.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 0 {
		t.Fatalf("decoded %d statements from nothing", len(decoded))
	}
}

// A tolerant reader here would be the worst kind of bug: it would skip a
// statement and hand back a database that looks fine and is missing a write.
func TestDecodeWALSegmentIsStrict(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"no terminating newline", `{"sql":"INSERT INTO t VALUES (1)"}`},
		{"a blank line", "{\"sql\":\"a\"}\n\n{\"sql\":\"b\"}\n"},
		{"not JSON", "{\"sql\":\"a\"}\nnot json\n"},
		{"an array instead of an object", "[\"a\"]\n"},
		{"no sql field", "{\"statement\":\"a\"}\n"},
		{"sql is not a string", "{\"sql\":42}\n"},
		{"two values on one line", "{\"sql\":\"a\"} {\"sql\":\"b\"}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeWALSegment([]byte(tc.data), "wal/1-1-abcd1234.jsonl")
			if err == nil {
				t.Fatal("a malformed segment must not decode")
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("category is %q, want corrupt: %v", CategoryOf(err), err)
			}
		})
	}
}

func TestWALLimitsAreRefusedNotSplit(t *testing.T) {
	oversized := strings.Repeat("x", MaxSQLBytes+1)
	if err := assertStatementWithinLimit(oversized); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("category is %q, want limit_exceeded", CategoryOf(err))
	}
	if _, err := encodeWALSegment([]string{oversized}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("encoding an oversized statement gave %q", CategoryOf(err))
	}

	// A segment over the ceiling is refused rather than split, because a
	// segment boundary is a commit boundary.
	chunk := strings.Repeat("y", 1<<20)
	var statements []string
	for int64(len(statements))*(1<<20) <= MaxWALSegmentBytes {
		statements = append(statements, chunk)
	}
	if _, err := encodeWALSegment(statements); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("encoding an oversized segment gave %q", CategoryOf(err))
	}
}

func TestObjectKeyValidation(t *testing.T) {
	valid := []string{
		"head.json",
		"wal/3-9-acde5678.jsonl",
		"checkpoints/3-8-acde1234.tar.gz",
		"a/b/c",
	}
	for _, key := range valid {
		if !IsValidObjectKey(key) {
			t.Errorf("%q should be a valid key", key)
		}
	}

	invalid := []string{
		"",
		"/absolute",
		"trailing/",
		"double//slash",
		"..",
		"../escape",
		"a/../b",
		"a/./b",
		`back\slash`,
		"nul\x00byte",
	}
	for _, key := range invalid {
		if IsValidObjectKey(key) {
			t.Errorf("%q should be refused", key)
		}
	}
}

func TestMintedKeysAreUniquePerAttempt(t *testing.T) {
	// Uniqueness per attempt is what makes an ambiguous upload resolvable
	// rather than destructive: a retry can never overwrite the bytes the first
	// try published.
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		key := walKey(3, 9)
		if seen[key] {
			t.Fatalf("%s was minted twice", key)
		}
		seen[key] = true
		if !IsValidObjectKey(key) {
			t.Fatalf("%s is not a valid key", key)
		}
		if !strings.HasPrefix(key, "wal/3-9-") || !strings.HasSuffix(key, ".jsonl") {
			t.Fatalf("%s does not match the frozen shape", key)
		}
	}
	checkpoint := checkpointKey(3, 8)
	if !strings.HasPrefix(checkpoint, "checkpoints/3-8-") || !strings.HasSuffix(checkpoint, ".tar.gz") {
		t.Fatalf("%s does not match the frozen shape", checkpoint)
	}
}

func TestObjectIDMustBeOneSegment(t *testing.T) {
	if err := validateObjectID("tenant-123"); err != nil {
		t.Fatalf("a flat id should be accepted: %v", err)
	}
	// A hierarchical id would let one object's prefix contain another's.
	for _, bad := range []string{"", ".", "..", "tenant/user", `tenant\user`, "nul\x00"} {
		if err := validateObjectID(bad); err == nil {
			t.Errorf("%q should be refused as an object id", bad)
		}
	}
}
