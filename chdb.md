<!-- Code generated by gomarkdoc. DO NOT EDIT -->

# chdb

```go
import "github.com/chdb-io/chdb-go/chdb"
```

## Index

- [func Query\(queryStr string, outputFormats ...string\) \*chdbstable.LocalResult](<#Query>)
- [type Session](<#Session>)
  - [func NewSession\(paths ...string\) \(\*Session, error\)](<#NewSession>)
  - [func \(s \*Session\) Cleanup\(\)](<#Session.Cleanup>)
  - [func \(s \*Session\) Close\(\)](<#Session.Close>)
  - [func \(s \*Session\) IsTemp\(\) bool](<#Session.IsTemp>)
  - [func \(s \*Session\) Path\(\) string](<#Session.Path>)
  - [func \(s \*Session\) Query\(queryStr string, outputFormats ...string\) \*chdbstable.LocalResult](<#Session.Query>)


<a name="Query"></a>
## func [Query](<https://github.com/chdb-io/chdb-go/blob/main/chdb/wrapper.go#L8>)

```go
func Query(queryStr string, outputFormats ...string) *chdbstable.LocalResult
```

Query calls queryToBuffer with a default output format of "CSV" if not provided.

<a name="Session"></a>
## type [Session](<https://github.com/chdb-io/chdb-go/blob/main/chdb/session.go#L11-L14>)



```go
type Session struct {
    // contains filtered or unexported fields
}
```

<a name="NewSession"></a>
### func [NewSession](<https://github.com/chdb-io/chdb-go/blob/main/chdb/session.go#L19>)

```go
func NewSession(paths ...string) (*Session, error)
```

NewSession creates a new session with the given path. If path is empty, a temporary directory is created. Note: The temporary directory is removed when Close is called.

<a name="Session.Cleanup"></a>
### func \(\*Session\) [Cleanup](<https://github.com/chdb-io/chdb-go/blob/main/chdb/session.go#L57>)

```go
func (s *Session) Cleanup()
```

Cleanup closes the session and removes the directory.

<a name="Session.Close"></a>
### func \(\*Session\) [Close](<https://github.com/chdb-io/chdb-go/blob/main/chdb/session.go#L49>)

```go
func (s *Session) Close()
```

Close closes the session and removes the temporary directory

```
temporary directory is created when NewSession was called with an empty path.
```

<a name="Session.IsTemp"></a>
### func \(\*Session\) [IsTemp](<https://github.com/chdb-io/chdb-go/blob/main/chdb/session.go#L68>)

```go
func (s *Session) IsTemp() bool
```

IsTemp returns whether the session is temporary.

<a name="Session.Path"></a>
### func \(\*Session\) [Path](<https://github.com/chdb-io/chdb-go/blob/main/chdb/session.go#L63>)

```go
func (s *Session) Path() string
```

Path returns the path of the session.

<a name="Session.Query"></a>
### func \(\*Session\) [Query](<https://github.com/chdb-io/chdb-go/blob/main/chdb/session.go#L39>)

```go
func (s *Session) Query(queryStr string, outputFormats ...string) *chdbstable.LocalResult
```

Query calls queryToBuffer with a default output format of "CSV" if not provided.

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
