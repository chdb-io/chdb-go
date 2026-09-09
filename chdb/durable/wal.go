package durable

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// WAL segment encoding and decoding (contract §4.4).
//
// The format is deliberately dull: UTF-8 JSONL, one {"sql": "..."} object per
// line, newline-terminated, replayed in manifest order then line order. Being
// dull is what lets four language bindings agree on it.
//
// Two limits are enforced on the write side and tolerated on the read side, as
// the contract requires: a reader must be able to load anything a conforming
// writer could have produced, so it refuses only what is over the *frozen*
// ceiling, never a lower local preference.
//
// What this file does not do is make statements deterministic. now(), rand()
// and reads of mutable external sources are replayed verbatim and will produce
// whatever they produce; V1 promises ordered replay of the original statement
// text and nothing more. Materialising those values before calling Execute is
// the caller's job.

// encodeWALLine renders one statement exactly as a segment holds it, without
// the terminating newline.
//
// HTML escaping is turned off so that SQL containing <, > or & is stored as
// written. The escaped form parses identically, but a WAL line is the most
// likely thing an operator ever reads by hand, and < in place of < makes
// that harder for no benefit.
func encodeWALLine(sql string) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]string{"sql": sql}); err != nil {
		return nil, wrapError(CategoryCorrupt, err, "durable: a statement cannot be encoded as JSON")
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// walLineBytes is how many bytes a statement will occupy in a segment,
// counting its newline.
//
// It must stay exactly consistent with encodeWALSegment: it is what the object
// layer budgets against before executing, and a budget that disagrees with the
// encoder by even one byte per line puts the boundary in the wrong place.
// encodeWALSegment writes each line followed by a newline, so the per-
// statement cost is additive and this can be summed. A test asserts the two
// agree.
func walLineBytes(sql string) (int64, error) {
	line, err := encodeWALLine(sql)
	if err != nil {
		return 0, err
	}
	return int64(len(line)) + 1, nil
}

// assertStatementWithinLimit refuses a statement that could not be written
// into a conforming segment.
func assertStatementWithinLimit(sql string) error {
	if int64(len(sql)) > MaxSQLBytes {
		return &Error{
			Category: CategoryLimitExceeded,
			Message: fmt.Sprintf("durable: statement is %d UTF-8 bytes, over the V1 per-statement "+
				"limit of %d", len(sql), MaxSQLBytes),
			Limit:    MaxSQLBytes,
			Observed: int64(len(sql)),
		}
	}
	return nil
}

// encodeWALSegment encodes buffered statements into one segment.
//
// It refuses an oversized segment rather than splitting: a segment boundary is
// a commit boundary, so splitting silently would turn one caller-visible flush
// into two, and a crash between them would commit a prefix the caller was
// never told about.
func encodeWALSegment(statements []string) ([]byte, error) {
	var buf bytes.Buffer
	for _, sql := range statements {
		if err := assertStatementWithinLimit(sql); err != nil {
			return nil, err
		}
		line, err := encodeWALLine(sql)
		if err != nil {
			return nil, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if int64(buf.Len()) > MaxWALSegmentBytes {
		return nil, &Error{
			Category: CategoryLimitExceeded,
			Message: fmt.Sprintf("durable: WAL segment would be %d bytes, over the V1 limit of %d; "+
				"flush more often", buf.Len(), MaxWALSegmentBytes),
			Limit:    MaxWALSegmentBytes,
			Observed: int64(buf.Len()),
		}
	}
	return buf.Bytes(), nil
}

// decodeWALSegment decodes a verified segment into its statements.
//
// It is strict on every count the contract names: exactly one JSON object per
// line, a string sql, and a terminating newline. A tolerant reader here would
// be the worst kind of bug — it would skip a statement and hand back a
// database that looks fine and is missing a write.
func decodeWALSegment(data []byte, key string) ([]string, error) {
	if int64(len(data)) > MaxWALSegmentBytes {
		return nil, &Error{
			Category: CategoryLimitExceeded,
			Message: fmt.Sprintf("durable: WAL segment %s is %d bytes, over the V1 limit of %d",
				key, len(data), MaxWALSegmentBytes),
			Limit:    MaxWALSegmentBytes,
			Observed: int64(len(data)),
		}
	}
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, newError(CategoryCorrupt,
			"durable: WAL segment %s does not end with a newline; it may be truncated", key)
	}
	lines := strings.Split(string(data[:len(data)-1]), "\n")
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		var record map[string]any
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.UseNumber()
		if err := decoder.Decode(&record); err != nil {
			return nil, wrapError(CategoryCorrupt, err,
				"durable: WAL segment %s line %d is not a JSON object", key, i+1)
		}
		if decoder.More() {
			return nil, newError(CategoryCorrupt,
				"durable: WAL segment %s line %d holds more than one JSON value", key, i+1)
		}
		sql, ok := record["sql"].(string)
		if !ok {
			return nil, newError(CategoryCorrupt,
				`durable: WAL segment %s line %d has no string "sql" field`, key, i+1)
		}
		// The per-statement ceiling is part of the frozen format, so it binds
		// the reader too. A conforming writer cannot produce a larger
		// statement, and replaying one from a segment that somehow holds it
		// would run SQL the limit exists to prevent — the segment ceiling
		// alone leaves room for a single statement twice the allowed size.
		if err := assertStatementWithinLimit(sql); err != nil {
			return nil, err
		}
		out = append(out, sql)
	}
	return out, nil
}
