package chdb

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const hostStdinChildEnv = "CHDB_GO_HOST_STDIN_CHILD"

const hostStdinRequest = `{"jsonrpc":"2.0","id":1,"method":"tools/call"}` + "\n"

// Issue #31: a program that speaks a protocol over its stdin, such as an MCP
// server on the STDIO transport, hung on INSERT. Engines before chdb-core
// v26.9.0 read fd 0 as INSERT data: an open, empty pipe blocked the INSERT,
// and a pending request was consumed and parsed as rows. Each case runs the
// queries in a child process whose stdin is a pipe the parent keeps open.
func TestQueriesDoNotReadHostStdin(t *testing.T) {
	if mode := os.Getenv(hostStdinChildEnv); mode != "" {
		runHostStdinChild(t, mode)
		return
	}

	for _, mode := range []string{"empty", "request"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestQueriesDoNotReadHostStdin$", "-test.count=1", "-test.v")
			cmd.Env = append(os.Environ(), hostStdinChildEnv+"="+mode)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if mode == "request" {
				if _, err := stdin.Write([]byte(hostStdinRequest)); err != nil {
					t.Fatal(err)
				}
			}

			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("child failed: %v\n%s", err, out.String())
				}
			case <-time.After(60 * time.Second):
				cmd.Process.Kill()
				<-done
				t.Fatalf("child hung with its stdin pipe open\n%s", out.String())
			}
		})
	}
}

func runHostStdinChild(t *testing.T, mode string) {
	sess, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	for _, q := range []string{
		"CREATE TABLE host_stdin (a UInt32) ENGINE = MergeTree ORDER BY a",
		"INSERT INTO host_stdin VALUES (1), (2)",
	} {
		if _, err := sess.Query(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	res, err := sess.Query("SELECT groupArray(a) FROM host_stdin", "CSV")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(res.String()); got != `"[1,2]"` {
		t.Fatalf("table holds %s, want \"[1,2]\"", got)
	}

	if mode == "request" {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil || line != hostStdinRequest {
			t.Fatalf("stdin after the queries: %q (%v), want the untouched request %q", line, err, hostStdinRequest)
		}
	}
}
