package durable

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	chdbpurego "github.com/chdb-io/chdb-go/v2/chdb-purego"
)

// A stand-in engine and a fault-injecting backend, so the state machine can be
// driven through every branch the contract's fault matrix names without a
// native library.
//
// The fake engine is allowed to classify SQL with string matching, which the
// real one is not: it stands in for core's parser rather than reimplementing
// it, and the gates it feeds are exercised against the real parser in
// engine_test.go. What it models faithfully is the part the state machine
// depends on — that a statement changes state, that a backup captures exactly
// the state at the moment it runs, and that a restore reproduces it.

type fakeEngine struct {
	version      string
	backupFormat int

	mu         sync.Mutex
	started    bool
	dataPath   string
	backupPath string
	database   string
	// applied is the database: the statements that have run, in order. It is
	// what a backup captures and a replay has to reproduce.
	applied []string

	// analyze overrides classification for one test.
	analyze func(sql, target string) (chdbpurego.QueryAnalysis, error)
	// runErr fails one statement, for the "a failed statement is never
	// logged" case.
	runErr func(sql string) error
	// backupErr and restoreErr fail the management calls.
	backupErr  error
	restoreErr error
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{version: "26.7.2-rc.2", backupFormat: BackupFormatBaseline}
}

func (f *fakeEngine) factory() EngineFactory {
	return func(context.Context) (Engine, error) { return f, nil }
}

func (f *fakeEngine) Version(context.Context) (string, error) { return f.version, nil }

func (f *fakeEngine) BackupFormat(context.Context) (int, error) { return f.backupFormat, nil }

func (f *fakeEngine) Start(_ context.Context, options EngineStartOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
	f.dataPath = options.DataPath
	f.backupPath = options.BackupsAllowedPath
	return nil
}

func (f *fakeEngine) CreateDatabase(_ context.Context, database string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.database = database
	return nil
}

func (f *fakeEngine) UseDatabase(_ context.Context, database string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.database = database
	return nil
}

func (f *fakeEngine) Analyze(_ context.Context, sql, target string) (chdbpurego.QueryAnalysis, error) {
	if f.analyze != nil {
		return f.analyze(sql, target)
	}
	return fakeClassify(sql, target), nil
}

func (f *fakeEngine) Query(_ context.Context, sql, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// The one query the tests ask: what is in the database.
	return strings.Join(f.applied, "\n"), nil
}

func (f *fakeEngine) Run(_ context.Context, sql string) error {
	if f.runErr != nil {
		if err := f.runErr(sql); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, sql)
	return nil
}

func (f *fakeEngine) BackupDatabase(_ context.Context, database, filePath string) error {
	if f.backupErr != nil {
		return f.backupErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := os.Stat(filePath); err == nil {
		return newError(CategoryEngine, "durable: %s already exists", filePath)
	}
	data, err := json.Marshal(struct {
		Database   string   `json:"database"`
		Statements []string `json:"statements"`
	}{database, f.applied})
	if err != nil {
		return err
	}
	return os.WriteFile(filePath, data, 0o600)
}

func (f *fakeEngine) RestoreDatabase(_ context.Context, database, filePath string) error {
	if f.restoreErr != nil {
		return f.restoreErr
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	var archive struct {
		Database   string   `json:"database"`
		Statements []string `json:"statements"`
	}
	if err := json.Unmarshal(data, &archive); err != nil {
		return err
	}
	if archive.Database != database {
		// An archive names the database it came from and restores under that
		// name and no other — the real engine reports BACKUP_ENTRY_NOT_FOUND.
		return newError(CategoryEngine, "durable: database %s not found in backup", database)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, archive.Statements...)
	f.database = database
	return nil
}

func (f *fakeEngine) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = false
	return nil
}

func (f *fakeEngine) isStarted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

func (f *fakeEngine) statements() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.applied...)
}

// fakeClassify stands in for core's parser. Crude on purpose: what it has to
// get right is the shape of the answer, not the parsing.
func fakeClassify(sql, target string) chdbpurego.QueryAnalysis {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ";"))
	upper := strings.ToUpper(trimmed)

	count := uint32(1)
	if trimmed == "" {
		count = 0
	} else if strings.Contains(trimmed, ";") || strings.Contains(upper, "PARALLEL WITH") {
		count = 2
	}

	analysis := chdbpurego.QueryAnalysis{StatementCount: count}
	analysis.HasSecrets = strings.Contains(upper, "SECRET_ACCESS_KEY")

	switch {
	case strings.HasPrefix(upper, "SELECT"), strings.HasPrefix(upper, "SHOW"),
		strings.HasPrefix(upper, "DESCRIBE"), strings.HasPrefix(upper, "EXPLAIN"):
		analysis.Class = chdbpurego.QueryReadOnly
		analysis.WritesOnlyTargetDatabase = true
	case strings.HasPrefix(upper, "SET"), strings.HasPrefix(upper, "USE"),
		strings.HasPrefix(upper, "SYSTEM"), strings.HasPrefix(upper, "BACKUP"),
		strings.HasPrefix(upper, "RESTORE"), strings.Contains(upper, "INTO OUTFILE"):
		analysis.Class = chdbpurego.QueryControl
	case strings.HasPrefix(upper, "CREATE FUNCTION"), strings.HasPrefix(upper, "CREATE USER"),
		strings.Contains(upper, "SYSTEM."):
		analysis.Class = chdbpurego.QueryMutatingGlobal
	case strings.HasPrefix(upper, "NOT SQL"):
		analysis.Class = chdbpurego.QueryUnknown
		analysis.StatementCount = 0
		analysis.HasSecrets = false
	default:
		analysis.Class = chdbpurego.QueryMutating
		if strings.Contains(upper, "DATABASE") &&
			(strings.HasPrefix(upper, "CREATE DATABASE") || strings.HasPrefix(upper, "DROP DATABASE") ||
				strings.HasPrefix(upper, "RENAME DATABASE")) {
			analysis.ChangesDatabaseLifecycle = true
		}
		// A qualified reference to anything but the target database, or a
		// write through a table function, clears the proof.
		other := strings.Contains(upper, "INTO FUNCTION") ||
			(strings.Contains(trimmed, ".") && !strings.Contains(trimmed, target+"."))
		analysis.WritesOnlyTargetDatabase = !other
	}
	return analysis
}

// faultBackend wraps a backend so a test can make one operation behave the way
// a provider does on a bad day.
type faultBackend struct {
	Backend

	mu sync.Mutex

	// onPutBytes intercepts a conditional create. Returning handled=true
	// substitutes the outcome without touching the store, which is how a
	// write that never left the machine is modelled.
	onPutBytes func(key string, attempt int) (outcome PutOutcome, err error, handled bool)

	// afterPutBytes runs after the real create, so a test can model bytes
	// that landed and a response that did not.
	afterPutBytes func(key string, attempt int, outcome PutOutcome) (PutOutcome, error)

	// onReplace intercepts a compare-and-swap. It runs *before* the real call;
	// pass through by returning handled=false.
	onReplace func(key string, attempt int) (outcome ReplaceOutcome, err error, handled bool)

	// afterReplace runs after a successful compare-and-swap, so a test can
	// model a commit whose response was lost.
	afterReplace func(key string, attempt int, outcome ReplaceOutcome) (ReplaceOutcome, error)

	putAttempts     map[string]int
	replaceAttempts map[string]int
}

func newFaultBackend(inner Backend) *faultBackend {
	return &faultBackend{
		Backend:         inner,
		putAttempts:     map[string]int{},
		replaceAttempts: map[string]int{},
	}
}

func (f *faultBackend) PutBytesIfAbsent(ctx context.Context, key string, data []byte) (PutOutcome, error) {
	f.mu.Lock()
	f.putAttempts[key]++
	attempt := f.putAttempts[key]
	before, after := f.onPutBytes, f.afterPutBytes
	f.mu.Unlock()

	if before != nil {
		if outcome, err, handled := before(key, attempt); handled {
			return outcome, err
		}
	}
	outcome, err := f.Backend.PutBytesIfAbsent(ctx, key, data)
	if err != nil || after == nil {
		return outcome, err
	}
	return after(key, attempt, outcome)
}

func (f *faultBackend) ReplaceIfMatch(ctx context.Context, key string, data []byte, etag string) (ReplaceOutcome, error) {
	f.mu.Lock()
	f.replaceAttempts[key]++
	attempt := f.replaceAttempts[key]
	before, after := f.onReplace, f.afterReplace
	f.mu.Unlock()

	if before != nil {
		if outcome, err, handled := before(key, attempt); handled {
			return outcome, err
		}
	}
	outcome, err := f.Backend.ReplaceIfMatch(ctx, key, data, etag)
	if err != nil || after == nil {
		return outcome, err
	}
	return after(key, attempt, outcome)
}

// Reaching past the backend to damage the store, for the corruption cases the
// contract's fixture list names. A backend has no delete and no overwrite —
// V1 gives nothing the authority to remove an object — so a test that needs
// one goes to the filesystem directly.
func removeFromStore(f *fixture, key string) error {
	return os.Remove(filepath.Join(f.root, "orders", key))
}

func overwriteInStore(f *fixture, key string, data []byte) error {
	return os.WriteFile(filepath.Join(f.root, "orders", key), data, 0o600)
}
