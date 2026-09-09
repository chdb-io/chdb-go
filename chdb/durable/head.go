package durable

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// head.json parsing, strict validation and unknown-field-preserving
// write-back (contract §4.2, §4.3, §4.5).
//
// The single most important thing this file does is *not* rebuild the head
// from scratch on every write. A writer that constructs a fresh document each
// time silently deletes every field it does not know about, which turns the
// whole named-feature mechanism into a lie: a future revision's state would
// survive exactly until an older writer touched the object. So the parsed raw
// JSON travels alongside the typed view, and serializeHead patches the known
// fields onto a copy of it.
//
// The second thing is that "preserve unknown fields" is not "be lenient".
// Known fields are validated strictly — a wrong type is corrupt, not a
// best-effort coercion — because a head that does not mean what it says is
// more dangerous than one that fails to load.
//
// Numbers are decoded as json.Number rather than float64 throughout. An
// unknown field holding a large integer or a long decimal would otherwise come
// back as a float and be written out in a different, lossy spelling — silently
// corrupting the one thing round-tripping exists to protect.

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// maxSafeInteger is the largest integer every JSON implementation can hold
// exactly. The contract requires all integers in a head to stay inside it, so
// a value beyond it is refused here rather than read differently by the next
// binding to open the object.
const maxSafeInteger = int64(1)<<53 - 1

func corrupt(format string, args ...any) *Error {
	return newError(CategoryCorrupt, "durable: head.json "+fmt.Sprintf(format, args...))
}

func asObject(v any) (map[string]any, bool) {
	obj, ok := v.(map[string]any)
	return obj, ok
}

// required reads a field that must be present.
//
// A plain lookup cannot tell an absent property from an explicit null, and for
// this document that distinction carries meaning: a released lease is
// *explicitly* three nulls, while a lease missing those keys is a truncated
// write. Treating the second as the first hands the object to a new writer on
// the strength of a corrupt head.
func required(obj map[string]any, key, where string) (any, error) {
	v, ok := obj[key]
	if !ok {
		return nil, corrupt("%s is required", where)
	}
	return v, nil
}

func safeInt(v any, where string) (int64, error) {
	num, ok := v.(json.Number)
	if !ok {
		return 0, corrupt("%s must be a non-negative safe integer", where)
	}
	n, err := num.Int64()
	if err != nil || n < 0 || n > maxSafeInteger {
		return 0, corrupt("%s must be a non-negative safe integer", where)
	}
	return n, nil
}

func nonEmptyString(v any, where string) (string, error) {
	s, ok := v.(string)
	if !ok || s == "" {
		return "", corrupt("%s must be a non-empty string", where)
	}
	return s, nil
}

func stringArray(v any, where string) ([]string, error) {
	items, ok := v.([]any)
	if !ok {
		return nil, corrupt("%s must be an array of strings", where)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, corrupt("%s must be an array of strings", where)
		}
		out = append(out, s)
	}
	return out, nil
}

func parseRef(v any, where string) (ObjectRef, error) {
	obj, ok := asObject(v)
	if !ok {
		return ObjectRef{}, corrupt("%s must be an object", where)
	}
	rawKey, err := required(obj, "key", where+".key")
	if err != nil {
		return ObjectRef{}, err
	}
	key, ok := rawKey.(string)
	if !ok || !IsValidObjectKey(key) {
		return ObjectRef{}, corrupt("%s.key is not a valid relative object key: %v", where, rawKey)
	}
	rawSize, err := required(obj, "size", where+".size")
	if err != nil {
		return ObjectRef{}, err
	}
	size, err := safeInt(rawSize, where+".size")
	if err != nil {
		return ObjectRef{}, err
	}
	rawSum, err := required(obj, "sha256", where+".sha256")
	if err != nil {
		return ObjectRef{}, err
	}
	sum, ok := rawSum.(string)
	if !ok || !sha256Pattern.MatchString(sum) {
		return ObjectRef{}, corrupt("%s.sha256 must be 64 lowercase hex characters", where)
	}
	return ObjectRef{Key: key, Size: size, SHA256: sum}, nil
}

// parseProtocol reads the negotiation block.
//
// A missing block reads as the V1 baseline with no features. That is the
// documented default rather than a rejection so that objects written before
// the block existed stay readable; every other shape is validated strictly.
// An absent key takes its default, while a key that is *present* takes its
// value — including an explicit null, which then fails validation. The
// defaults exist for older documents, not as a way to launder bad data.
func parseProtocol(v any, present bool) (Protocol, error) {
	if !present {
		return Protocol{Version: ProtocolVersion, ReaderFeatures: []string{}, WriterFeatures: []string{}}, nil
	}
	obj, ok := asObject(v)
	if !ok {
		return Protocol{}, corrupt("protocol must be an object")
	}
	out := Protocol{Version: ProtocolVersion, ReaderFeatures: []string{}, WriterFeatures: []string{}}
	if raw, ok := obj["version"]; ok {
		n, err := safeInt(raw, "protocol.version")
		if err != nil {
			return Protocol{}, err
		}
		out.Version = int(n)
	}
	if raw, ok := obj["reader_features"]; ok {
		features, err := stringArray(raw, "protocol.reader_features")
		if err != nil {
			return Protocol{}, err
		}
		out.ReaderFeatures = features
	}
	if raw, ok := obj["writer_features"]; ok {
		features, err := stringArray(raw, "protocol.writer_features")
		if err != nil {
			return Protocol{}, err
		}
		out.WriterFeatures = features
	}
	return out, nil
}

// parseEngine reads the engine identity.
//
// The block itself has no default — a head that records no engine cannot
// establish compatibility at all, so an absent one is a refusal rather than a
// wildcard. The two compatibility fields do have defaults, and both are the
// conservative reading of an object written before they existed:
//
//   - backup_format defaults to the V1 baseline, which is what such an object
//     necessarily used.
//   - min_reader defaults to version. That reproduces the old exact-match
//     behaviour's lower bound: only a reader at or above the producer may open
//     it. Defaulting it to something older would retroactively widen an
//     object's audience on the strength of a field its writer never wrote.
func parseEngine(v any) (EngineIdentity, error) {
	obj, ok := asObject(v)
	if !ok {
		return EngineIdentity{}, corrupt("engine must be an object")
	}
	rawVersion, err := required(obj, "version", "engine.version")
	if err != nil {
		return EngineIdentity{}, err
	}
	version, err := nonEmptyString(rawVersion, "engine.version")
	if err != nil {
		return EngineIdentity{}, err
	}
	rawName, err := required(obj, "name", "engine.name")
	if err != nil {
		return EngineIdentity{}, err
	}
	name, err := nonEmptyString(rawName, "engine.name")
	if err != nil {
		return EngineIdentity{}, err
	}
	out := EngineIdentity{Name: name, Version: version, BackupFormat: BackupFormatBaseline, MinReader: version}
	if raw, ok := obj["backup_format"]; ok {
		n, err := safeInt(raw, "engine.backup_format")
		if err != nil {
			return EngineIdentity{}, err
		}
		out.BackupFormat = int(n)
	}
	if raw, ok := obj["min_reader"]; ok {
		s, err := nonEmptyString(raw, "engine.min_reader")
		if err != nil {
			return EngineIdentity{}, err
		}
		out.MinReader = s
	}
	return out, nil
}

// parseLease reads the writer lease.
//
// The lease is either fully held or fully released. A partial form — an owner
// with no expiry, an expiry with no instance — is rejected rather than
// normalised, because each half implies a different answer to "may I take this
// over", and guessing is how two writers end up believing they are the one
// writer.
func parseLease(v any) (Lease, error) {
	obj, ok := asObject(v)
	if !ok {
		return Lease{}, corrupt("lease must be an object")
	}
	rawGeneration, err := required(obj, "generation", "lease.generation")
	if err != nil {
		return Lease{}, err
	}
	generation, err := safeInt(rawGeneration, "lease.generation")
	if err != nil {
		return Lease{}, err
	}
	owner, err := required(obj, "owner", "lease.owner")
	if err != nil {
		return Lease{}, err
	}
	instance, err := required(obj, "instance", "lease.instance")
	if err != nil {
		return Lease{}, err
	}
	expiresAt, err := required(obj, "expires_at", "lease.expires_at")
	if err != nil {
		return Lease{}, err
	}

	if owner == nil && instance == nil && expiresAt == nil {
		return Lease{Generation: generation}, nil
	}

	ownerStr, ownerOK := owner.(string)
	instanceStr, instanceOK := instance.(string)
	expiresNum, expiresOK := expiresAt.(json.Number)
	if !ownerOK || !instanceOK || !expiresOK {
		return Lease{}, corrupt("lease must be either fully released (owner, instance and " +
			"expires_at all null) or fully held (owner and instance strings, expires_at a number)")
	}
	expires, err := expiresNum.Float64()
	if err != nil || math.IsInf(expires, 0) || math.IsNaN(expires) {
		return Lease{}, corrupt("lease.expires_at must be finite")
	}
	return Lease{
		Generation: generation,
		Owner:      &ownerStr,
		Instance:   &instanceStr,
		ExpiresAt:  &expires,
	}, nil
}

func parseManifest(v any) (Manifest, error) {
	obj, ok := asObject(v)
	if !ok {
		return Manifest{}, corrupt("manifest must be an object")
	}
	rawDB, err := required(obj, "db", "manifest.db")
	if err != nil {
		return Manifest{}, err
	}
	db, err := nonEmptyString(rawDB, "manifest.db")
	if err != nil {
		return Manifest{}, err
	}
	// base is the only field the protocol allows to be null; the rest are
	// required and may not be, so an absent or nulled wal is corrupt rather
	// than an empty replay list.
	rawBase, err := required(obj, "base", "manifest.base")
	if err != nil {
		return Manifest{}, err
	}
	var base *ObjectRef
	if rawBase != nil {
		ref, err := parseRef(rawBase, "manifest.base")
		if err != nil {
			return Manifest{}, err
		}
		base = &ref
	}
	rawWAL, err := required(obj, "wal", "manifest.wal")
	if err != nil {
		return Manifest{}, err
	}
	items, ok := rawWAL.([]any)
	if !ok {
		return Manifest{}, corrupt("manifest.wal must be an array")
	}
	wal := make([]ObjectRef, 0, len(items))
	for i, item := range items {
		ref, err := parseRef(item, fmt.Sprintf("manifest.wal[%d]", i))
		if err != nil {
			return Manifest{}, err
		}
		wal = append(wal, ref)
	}
	rawSeq, err := required(obj, "seq", "manifest.seq")
	if err != nil {
		return Manifest{}, err
	}
	seq, err := safeInt(rawSeq, "manifest.seq")
	if err != nil {
		return Manifest{}, err
	}
	return Manifest{DB: db, Base: base, WAL: wal, Seq: seq}, nil
}

// parseHead validates head bytes strictly and keeps the raw JSON for
// round-trip.
func parseHead(data []byte) (Head, map[string]any, error) {
	if int64(len(data)) > MaxHeadBytes {
		return Head{}, nil, &Error{
			Category: CategoryLimitExceeded,
			Message: fmt.Sprintf("durable: head.json is %d bytes, over the V1 limit of %d",
				len(data), MaxHeadBytes),
			Limit:    MaxHeadBytes,
			Observed: int64(len(data)),
		}
	}
	// The protocol says UTF-8. Bytes that are not UTF-8 are corrupt rather
	// than silently repaired: replacing a malformed byte with U+FFFD would let
	// invalid data inside an *unknown* field parse cleanly and then be written
	// back mangled.
	if !utf8.Valid(data) {
		return Head{}, nil, corrupt("is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var raw any
	if err := decoder.Decode(&raw); err != nil {
		return Head{}, nil, wrapError(CategoryCorrupt, err, "durable: head.json is not valid JSON")
	}
	// Trailing content would mean two documents, and the second would be
	// dropped on write-back.
	if decoder.More() {
		return Head{}, nil, corrupt("holds more than one JSON document")
	}
	obj, ok := asObject(raw)
	if !ok {
		return Head{}, nil, corrupt("must be a JSON object")
	}

	rawProtocol, hasProtocol := obj["protocol"]
	protocol, err := parseProtocol(rawProtocol, hasProtocol)
	if err != nil {
		return Head{}, nil, err
	}
	rawEngine, err := required(obj, "engine", "engine")
	if err != nil {
		return Head{}, nil, err
	}
	engine, err := parseEngine(rawEngine)
	if err != nil {
		return Head{}, nil, err
	}
	rawLease, err := required(obj, "lease", "lease")
	if err != nil {
		return Head{}, nil, err
	}
	lease, err := parseLease(rawLease)
	if err != nil {
		return Head{}, nil, err
	}
	rawManifest, err := required(obj, "manifest", "manifest")
	if err != nil {
		return Head{}, nil, err
	}
	manifest, err := parseManifest(rawManifest)
	if err != nil {
		return Head{}, nil, err
	}
	return Head{Protocol: protocol, Engine: engine, Lease: lease, Manifest: manifest}, obj, nil
}

// mergeInto patches known fields onto whatever is stored under key, so an
// unrecognised sibling inside protocol, engine, lease or manifest survives.
func mergeInto(base map[string]any, key string, known map[string]any) {
	existing, ok := asObject(base[key])
	if !ok {
		base[key] = known
		return
	}
	merged := make(map[string]any, len(existing)+len(known))
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range known {
		merged[k] = v
	}
	base[key] = merged
}

// cloneJSON deep-copies a decoded document, so patching a candidate head
// cannot mutate the raw map the committed head still refers to.
func cloneJSON(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, item := range typed {
			out[k] = cloneJSON(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneJSON(item)
		}
		return out
	default:
		return v
	}
}

func refToJSON(ref ObjectRef) map[string]any {
	return map[string]any{
		"key":    ref.Key,
		"size":   json.Number(fmt.Sprint(ref.Size)),
		"sha256": ref.SHA256,
	}
}

// serializeHead renders head, preserving every unrecognised field of raw.
//
// manifest.base and manifest.wal are replaced wholesale rather than merged:
// they are this build's own state, and a stale unknown key inside a reference
// being rewritten would describe bytes that are no longer there.
func serializeHead(head Head, raw map[string]any) ([]byte, error) {
	// Refuse to emit a document this parser would reject. Writing one is
	// worse than failing here: head.json is created with a conditional create
	// and V1 has no destroy, so an object published with, say, an empty
	// manifest.db is corrupt on every subsequent open and cannot be removed.
	switch {
	case head.Manifest.DB == "":
		return nil, corrupt("manifest.db must be a non-empty string")
	case head.Engine.Name == "":
		return nil, corrupt("engine.name must be a non-empty string")
	case head.Engine.Version == "":
		return nil, corrupt("engine.version must be a non-empty string")
	case head.Engine.MinReader == "":
		return nil, corrupt("engine.min_reader must be a non-empty string")
	}

	base := map[string]any{}
	if raw != nil {
		base = cloneJSON(raw).(map[string]any)
	}

	readerFeatures := make([]any, len(head.Protocol.ReaderFeatures))
	for i, f := range head.Protocol.ReaderFeatures {
		readerFeatures[i] = f
	}
	writerFeatures := make([]any, len(head.Protocol.WriterFeatures))
	for i, f := range head.Protocol.WriterFeatures {
		writerFeatures[i] = f
	}
	mergeInto(base, "protocol", map[string]any{
		"version":         json.Number(fmt.Sprint(head.Protocol.Version)),
		"reader_features": readerFeatures,
		"writer_features": writerFeatures,
	})
	mergeInto(base, "engine", map[string]any{
		"name":          head.Engine.Name,
		"version":       head.Engine.Version,
		"backup_format": json.Number(fmt.Sprint(head.Engine.BackupFormat)),
		"min_reader":    head.Engine.MinReader,
	})
	lease := map[string]any{
		"generation": json.Number(fmt.Sprint(head.Lease.Generation)),
		"owner":      nil,
		"instance":   nil,
		"expires_at": nil,
	}
	if head.Lease.Owner != nil {
		lease["owner"] = *head.Lease.Owner
	}
	if head.Lease.Instance != nil {
		lease["instance"] = *head.Lease.Instance
	}
	if head.Lease.ExpiresAt != nil {
		lease["expires_at"] = json.Number(formatSeconds(*head.Lease.ExpiresAt))
	}
	mergeInto(base, "lease", lease)

	wal := make([]any, len(head.Manifest.WAL))
	for i, ref := range head.Manifest.WAL {
		wal[i] = refToJSON(ref)
	}
	manifest := map[string]any{
		"db":   head.Manifest.DB,
		"base": nil,
		"wal":  wal,
		"seq":  json.Number(fmt.Sprint(head.Manifest.Seq)),
	}
	if head.Manifest.Base != nil {
		manifest["base"] = refToJSON(*head.Manifest.Base)
	}
	mergeInto(base, "manifest", manifest)

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	// Leave <, > and & alone. The escaped form is valid JSON and every parser
	// reads it the same way, but an unknown field's value would come back out
	// spelled differently from how another binding wrote it, which makes a
	// round-trip harder to verify than it needs to be.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(base); err != nil {
		return nil, wrapError(CategoryCorrupt, err, "durable: head.json cannot be encoded")
	}
	// Encode appends a newline; the document itself is what gets stored.
	out := bytes.TrimRight(buf.Bytes(), "\n")
	if int64(len(out)) > MaxHeadBytes {
		return nil, &Error{
			Category: CategoryLimitExceeded,
			Message: fmt.Sprintf("durable: head.json would be %d bytes, over the V1 limit of %d; "+
				"checkpoint to truncate the WAL list", len(out), MaxHeadBytes),
			Limit:    MaxHeadBytes,
			Observed: int64(len(out)),
		}
	}
	return out, nil
}

// formatSeconds renders an epoch-seconds timestamp without an exponent.
//
// strconv's shortest form would write 1.7882304e+09, which is the same number
// but reads as a different kind of value in a document meant for humans and
// four languages. Trailing zeros are trimmed so a whole second stays whole.
func formatSeconds(v float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", v), "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}
