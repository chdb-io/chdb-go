package chdbpurego

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewConnection(t *testing.T) {
	tests := []struct {
		name    string
		argc    int
		argv    []string
		wantErr bool
	}{
		{
			name:    "empty args",
			argc:    0,
			argv:    []string{},
			wantErr: false,
		},
		{
			name:    "memory database",
			argc:    1,
			argv:    []string{":memory:"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, err := NewConnection(tt.argc, tt.argv)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewConnection() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if conn == nil && !tt.wantErr {
				t.Error("NewConnection() returned nil connection without error")
				return
			}
			if conn != nil {
				defer conn.Close()
				if !conn.Ready() {
					t.Error("NewConnection() returned connection that is not ready")
				}
			}
		})
	}
}

func TestNewConnectionFromConnString(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "chdb_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name      string
		connStr   string
		wantErr   bool
		checkPath bool
	}{
		{
			name:    "empty string",
			connStr: "",
			wantErr: false,
		},
		{
			name:    "memory database",
			connStr: ":memory:",
			wantErr: false,
		},
		{
			name:    "memory database with params",
			connStr: ":memory:?verbose&log-level=test",
			wantErr: false,
		},
		{
			name:      "relative path",
			connStr:   "test.db",
			wantErr:   false,
			checkPath: true,
		},
		{
			name:      "file prefix",
			connStr:   "file:test.db",
			wantErr:   false,
			checkPath: true,
		},
		{
			name:      "absolute path",
			connStr:   filepath.Join(tmpDir, "test.db"),
			wantErr:   false,
			checkPath: true,
		},
		{
			name:      "file prefix with absolute path",
			connStr:   "file:" + filepath.Join(tmpDir, "test.db"),
			wantErr:   false,
			checkPath: true,
		},
		// {
		// 	name:      "readonly mode with existing dir",
		// 	connStr:   filepath.Join(tmpDir, "readonly.db") + "?mode=ro",
		// 	wantErr:   false,
		// 	checkPath: true,
		// },
		// {
		// 	name:      "readonly mode with non-existing dir",
		// 	connStr:   filepath.Join(tmpDir, "new_readonly.db") + "?mode=ro",
		// 	wantErr:   true,
		// 	checkPath: true,
		// },
		{
			name:      "write mode with existing dir",
			connStr:   filepath.Join(tmpDir, "write.db"),
			wantErr:   false,
			checkPath: true,
		},
		{
			name:      "write mode with non-existing dir",
			connStr:   filepath.Join(tmpDir, "new_write.db"),
			wantErr:   false,
			checkPath: true,
		},
	}

	// Create a directory with read-only permissions for permission testing
	readOnlyDir := filepath.Join(tmpDir, "readonly_dir")
	if err := os.MkdirAll(readOnlyDir, 0555); err != nil {
		t.Fatalf("Failed to create read-only directory: %v", err)
	}
	tests = append(tests, struct {
		name      string
		connStr   string
		wantErr   bool
		checkPath bool
	}{
		name:      "write mode with read-only dir",
		connStr:   filepath.Join(readOnlyDir, "test.db"),
		wantErr:   true,
		checkPath: true,
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, err := NewConnectionFromConnString(tt.connStr)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewConnectionFromConnString() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if conn == nil && !tt.wantErr {
				t.Error("NewConnectionFromConnString() returned nil connection without error")
				return
			}
			if conn != nil {
				defer conn.Close()
				if !conn.Ready() {
					t.Error("NewConnectionFromConnString() returned connection that is not ready")
				}

				// Test a simple query to verify the connection works
				result, err := conn.Query("SELECT 1", "CSV")
				if err != nil {
					t.Errorf("Query failed: %v", err)
					return
				}
				if result == nil {
					t.Error("Query returned nil result")
					return
				}
				if result.Error() != nil {
					t.Errorf("Query result has error: %v", result.Error())
					return
				}
				if result.String() != "1\n" {
					t.Errorf("Query result = %v, want %v", result.String(), "1\n")
				}
			}
		})
	}
}
