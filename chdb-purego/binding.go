package chdbpurego

import (
	"os"
	"os/exec"

	"github.com/ebitengine/purego"
)

func findLibrary() string {
	// Env var
	if envPath := os.Getenv("CHDB_LIB_PATH"); envPath != "" {
		return envPath
	}

	// ldconfig with Linux
	if path, err := exec.LookPath("libchdb.so"); err == nil {
		return path
	}

	// default path
	commonPaths := []string{
		"/usr/local/lib/libchdb.so",
		"/opt/homebrew/lib/libchdb.dylib",
	}

	for _, p := range commonPaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	//should be an error ?
	return "libchdb.so"
}

var (
	queryStable   func(argc int, argv []string) *local_result
	freeResult    func(result *local_result)
	queryStableV2 func(argc int, argv []string) *local_result_v2
	freeResultV2  func(result *local_result_v2)
	connectChdb   func(argc int, argv []*byte) **chdb_conn
	closeConn     func(conn **chdb_conn)
	queryConn     func(conn *chdb_conn, query string, format string) *local_result_v2
)

func init() {
	path := findLibrary()
	libchdb, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		panic(err)
	}
	purego.RegisterLibFunc(&queryStable, libchdb, "query_stable")
	purego.RegisterLibFunc(&freeResult, libchdb, "free_result")
	purego.RegisterLibFunc(&queryStableV2, libchdb, "query_stable_v2")

	purego.RegisterLibFunc(&freeResultV2, libchdb, "free_result_v2")
	purego.RegisterLibFunc(&connectChdb, libchdb, "connect_chdb")
	purego.RegisterLibFunc(&closeConn, libchdb, "close_conn")
	purego.RegisterLibFunc(&queryConn, libchdb, "query_conn")

}
