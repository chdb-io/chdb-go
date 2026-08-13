#!/bin/bash
# The race detector's findings decide this step, not the exit status.
#
# libchdb starts process-global ClickHouse thread pools and offers no way to stop
# them: the v26.7.0 C ABI has 50 functions and none of them shuts the engine down,
# so ~33 engine threads are still parked on condvars when the test binary exits.
# Under -race, Go's exit path runs racefini -> __tsan_fini, which tears the
# sanitizer runtime down while those threads can still wake and run instrumented
# code. One faults; because it is a C thread, Go's badsignal re-raises and the
# process dies with a segfault after every test has already reported PASS.
#
# Established from a core dump: the crashing thread's stack is
# runtime.raise <- raisebadsignal <- badsignal <- sigtrampgo, __tsan_fini and
# __run_exit_handlers are on the stack, and 33 threads sit in
# __pthread_cond_wait_common inside libchdb. Waiting before exit does not help —
# measured 5/48, 3/48, 7/48, 3/48 crashes for waits of none, 500ms, 2s and 5s,
# because BackgroundSchedulePool re-arms its tasks on a timer and never quiesces.
#
# So: run the race detector and fail on what it is for — a reported data race, a
# failing test, a panic — and do not fail on that one segfault. Nothing about the
# detector's coverage changes; only the way this step reads its own result.
#
# Remove this wrapper once the engine can be shut down (chdb-core needs either a
# chdb_shutdown() for callers or an atexit that stops the pools). Then the plain
# exit status is trustworthy again.
set -uo pipefail

LOG=$(mktemp)
go test -race -timeout=180s ./... 2>&1 | tee "$LOG"
status=${PIPESTATUS[0]}

if grep -q "DATA RACE" "$LOG"; then
    echo "::error::the race detector reported a data race"
    exit 1
fi
if grep -qE "^--- FAIL|^\s+--- FAIL" "$LOG"; then
    echo "::error::a test failed under the race detector"
    exit 1
fi
if grep -qE "^panic:|^fatal error:" "$LOG"; then
    echo "::error::the test binary panicked under the race detector"
    exit 1
fi

# A fault the Go runtime handled itself, i.e. one that hit a thread it owns while
# a test was running. It prints its own header and a goroutine dump; the exit-time
# crash never does, because there the signal lands on a libchdb thread and Go's
# badsignal path re-raises without a dump. The distinction is what keeps the
# exemption below from covering a crash during a test — a real one landed here
# once already, chdb-io/chdb-go#46.
if grep -qE "^(SIGSEGV|SIGBUS|SIGFPE|SIGILL|SIGABRT|SIGTRAP):" "$LOG"; then
    echo "::error::the runtime reported a fatal signal during a test, not at exit"
    exit 1
fi

if [ "$status" -ne 0 ]; then
    # Positive evidence, not just the presence of the word: the package has to have
    # reported PASS and then died on the very next line. "Contains a segfault
    # somewhere" would exempt far more than the one crash this is about.
    if grep -A1 '^PASS$' "$LOG" | grep -q '^signal: segmentation fault'; then
        echo "::warning::tests passed; the binary segfaulted at exit, which is the" \
             "known libchdb thread-shutdown issue and not a test result"
        exit 0
    fi
    echo "::error::race step failed for a reason this wrapper does not recognise" \
         "(exit $status; no data race, failing test, panic, or in-test signal, and" \
         "no PASS immediately followed by a segfault)"
    exit "$status"
fi
