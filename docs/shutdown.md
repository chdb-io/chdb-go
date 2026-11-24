# Stopping the engine

Closing your last `chdb.Session` does not stop the thread pools the engine
started. Measured on chdb-core v26.11.0-rc.2, linux/amd64: a process sitting at
6 threads reaches 19 once a session runs a query, and is at 22 after that
session is closed. Exiting is fine — the kernel reaps them. Running something
*after* that is not, and a process with a sanitizer, C++ global destructors, or
a host runtime's finalizers does exactly that.

`chdb.Shutdown()` joins them — back to 6 in that same measurement:

```go
defer func() {
        if err := chdb.Shutdown(); err != nil {
                log.Println("chdb:", err)
        }
}()
```

Close every session first; it refuses otherwise, with an error wrapping
`chdb.ErrSessionsOpen` that names how many are still open. It is terminal —
after it, this process cannot open another session — so it belongs at the end
of `main`, not between two units of work. A program that never opened a session
can call it anyway: it loads nothing and returns nil.

Not calling it is as safe as it has always been for a process that simply
exits. The exception is a binary built with `-race`: on a clean exit it leaves
through libc's `exit()`, which runs the engine's exit teardown while engine
threads are still running, so exiting it with a session open — `MergeTree`
merges in flight especially — can crash. Close every session and call
`chdb.Shutdown()` first.

It needs an engine from chdb-core v26.7.2-rc.2 or later; on an older one it
returns `chdbpurego.ErrShutdownUnavailable`. Up to v26.11.0-rc.1 its reach was
narrower than that: a process that had created a `MergeTree` table got an error
back and no teardown at all. From v26.11.0-rc.2 it stops those threads too, and
the process is back at the thread count it started with.
