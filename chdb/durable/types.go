package durable

// The frozen wire types of Durable V1 (contract §4).
//
// Everything here is FROZEN: another binding has to be able to read what this
// one writes. The JSON is compared semantically, not byte for byte — key order
// and whitespace are free — but field names, types and meanings are not.

// ProtocolVersion is the baseline this build implements. A higher version in a
// head means "do not open".
const ProtocolVersion = 1

// V1 defines no non-empty feature names. Anything appearing in a head's
// feature lists is therefore from a future revision, and the negotiation rules
// apply: an unknown reader feature refuses the open outright, an unknown
// writer feature still permits a read-only open.
var (
	KnownReaderFeatures = []string{}
	KnownWriterFeatures = []string{}
)

// EngineName is what a chDB writer records in head.engine.name.
const EngineName = "chdb"

// BackupFormatBaseline is the archive-format generation this build
// understands. V1's baseline is 1.
//
// It exists to be the one explicit signal that the backward-compatibility
// promise has been withdrawn: core increments it when a later release can no
// longer restore earlier full backups, and a reader refuses anything above its
// own baseline. Without it a reader compares version numbers, sees a larger
// one, concludes it is fine, and walks into a RESTORE that fails halfway
// through recovery.
//
// The running engine's own value is not available yet — the C ABI exposes no
// accessor — so every engine reports this baseline until one does.
const BackupFormatBaseline = 1

// Frozen V1 limits (contract §4.4, §4.5).
const (
	// MaxSQLBytes is the ceiling on one statement, in UTF-8 bytes.
	MaxSQLBytes = 64 * 1024 * 1024
	// MaxWALSegmentBytes is the ceiling on one uncompressed WAL segment.
	MaxWALSegmentBytes = 128 * 1024 * 1024
	// MaxHeadBytes is the ceiling on head.json.
	MaxHeadBytes = 1024 * 1024
)

// Protocol is the version and feature negotiation block.
type Protocol struct {
	Version        int      `json:"version"`
	ReaderFeatures []string `json:"reader_features"`
	WriterFeatures []string `json:"writer_features"`
}

// EngineIdentity records which engine produced the object and what a reader
// needs in order to restore it.
type EngineIdentity struct {
	Name string `json:"name"`

	// Version is chdb_version() of the writer that last touched the object.
	// Recorded for diagnosis and audit; explicitly *not* the compatibility
	// gate. See negotiate.go.
	Version string `json:"version"`

	// BackupFormat is the archive-format generation. A reader refuses anything
	// above its own baseline.
	BackupFormat int `json:"backup_format"`

	// MinReader is the oldest chDB release that may read the current state. A
	// reader below it is refused; anything at or above it opens.
	MinReader string `json:"min_reader"`
}

// ObjectRef points at one immutable object. The size and digest are not
// decoration: base and WAL are verified against both before anything is
// restored or replayed, and they are what makes an ambiguous upload
// resolvable (contract §5.8).
type ObjectRef struct {
	// Key is relative to <namespace>/<object-id>/, "/"-separated, with no
	// leading slash.
	Key string `json:"key"`

	Size int64 `json:"size"`

	// SHA256 is the lowercase full hex digest.
	SHA256 string `json:"sha256"`
}

// Lease is the writer lease. A released lease is the all-null form: owner,
// instance and expiry absent while the generation stays, so the next acquirer
// knows what to increment past.
type Lease struct {
	Generation int64 `json:"generation"`

	// Owner is a human-visible name. Observability only; never an authority
	// check.
	Owner *string `json:"owner"`

	// Instance identifies one live instance. This, with the generation, is the
	// fence.
	Instance *string `json:"instance"`

	// ExpiresAt is epoch seconds, fractional allowed.
	ExpiresAt *float64 `json:"expires_at"`
}

// held reports whether a lease names a live owner.
func (l Lease) held() bool { return l.Owner != nil }

// instanceIs reports whether this lease belongs to the given instance.
func (l Lease) instanceIs(instance string) bool {
	return l.Instance != nil && *l.Instance == instance
}

// Manifest is the object's committed state: one database, one base, and the
// WAL segments to replay over it.
type Manifest struct {
	// DB is the one database this object holds.
	DB string `json:"db"`

	Base *ObjectRef `json:"base"`

	// WAL is ordered by replay order.
	WAL []ObjectRef `json:"wal"`

	Seq int64 `json:"seq"`
}

// Head is the typed view of head.json.
//
// Unknown fields are not represented here — they travel separately in
// headSnapshot.raw, because dropping them would silently strip a future
// revision's state (contract §4.2, §4.3).
type Head struct {
	Protocol Protocol       `json:"protocol"`
	Engine   EngineIdentity `json:"engine"`
	Lease    Lease          `json:"lease"`
	Manifest Manifest       `json:"manifest"`
}

// clone returns a deep copy, so a caller that mutates one field of a candidate
// head cannot reach into the committed one through a shared slice.
func (h Head) clone() Head {
	out := h
	out.Protocol.ReaderFeatures = append([]string(nil), h.Protocol.ReaderFeatures...)
	out.Protocol.WriterFeatures = append([]string(nil), h.Protocol.WriterFeatures...)
	if h.Manifest.Base != nil {
		base := *h.Manifest.Base
		out.Manifest.Base = &base
	}
	out.Manifest.WAL = append([]ObjectRef(nil), h.Manifest.WAL...)
	return out
}

// headSnapshot is a head as read from the backend: the typed view, its CAS
// token, and the raw JSON kept so unknown fields survive a write-back.
type headSnapshot struct {
	head Head
	etag string
	raw  map[string]any
}

// coldHead is the head a brand-new object starts from: no base, no WAL,
// generation 1.
//
// The lease is left released here and filled in by the creating writer, since
// cold create and lease acquisition are one conditional write (contract §5.2)
// — publishing an unheld head first would leave a window in which a second
// process could take a lease on a manifest nobody has restored into yet.
func coldHead(db, engineVersion string, backupFormat int) Head {
	return Head{
		Protocol: Protocol{
			Version:        ProtocolVersion,
			ReaderFeatures: []string{},
			WriterFeatures: []string{},
		},
		Engine: EngineIdentity{
			Name:         EngineName,
			Version:      engineVersion,
			BackupFormat: backupFormat,
			// A fresh object can only be read by this engine or later: its
			// base will be produced by this engine.
			MinReader: engineVersion,
		},
		Lease:    Lease{Generation: 1},
		Manifest: Manifest{DB: db, Base: nil, WAL: []ObjectRef{}, Seq: 0},
	}
}

func stringPtr(s string) *string  { return &s }
func floatPtr(f float64) *float64 { return &f }
