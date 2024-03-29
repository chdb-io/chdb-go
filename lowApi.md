<!-- Code generated by gomarkdoc. DO NOT EDIT -->

# chdbstable

```go
import "github.com/chdb-io/chdb-go/chdbstable"
```

## Index

- [type LocalResult](<#LocalResult>)
  - [func QueryStable\(argc int, argv \[\]string\) \*LocalResult](<#QueryStable>)
  - [func \(r \*LocalResult\) Buf\(\) \[\]byte](<#LocalResult.Buf>)
  - [func \(r \*LocalResult\) BytesRead\(\) uint64](<#LocalResult.BytesRead>)
  - [func \(r \*LocalResult\) Elapsed\(\) float64](<#LocalResult.Elapsed>)
  - [func \(r \*LocalResult\) Len\(\) int](<#LocalResult.Len>)
  - [func \(r \*LocalResult\) RowsRead\(\) uint64](<#LocalResult.RowsRead>)
  - [func \(r LocalResult\) String\(\) string](<#LocalResult.String>)


<a name="LocalResult"></a>
## type [LocalResult](<https://github.com/chdb-io/chdb-go/blob/main/chdbstable/chdb.go#L15-L17>)

LocalResult mirrors the C struct local\_result in Go.

```go
type LocalResult struct {
    // contains filtered or unexported fields
}
```

<a name="QueryStable"></a>
### func [QueryStable](<https://github.com/chdb-io/chdb-go/blob/main/chdbstable/chdb.go#L32>)

```go
func QueryStable(argc int, argv []string) *LocalResult
```

QueryStable calls the C function query\_stable.

<a name="LocalResult.Buf"></a>
### func \(\*LocalResult\) [Buf](<https://github.com/chdb-io/chdb-go/blob/main/chdbstable/chdb.go#L44>)

```go
func (r *LocalResult) Buf() []byte
```

Accessor methods to access fields of the local\_result struct.

<a name="LocalResult.BytesRead"></a>
### func \(\*LocalResult\) [BytesRead](<https://github.com/chdb-io/chdb-go/blob/main/chdbstable/chdb.go#L84>)

```go
func (r *LocalResult) BytesRead() uint64
```



<a name="LocalResult.Elapsed"></a>
### func \(\*LocalResult\) [Elapsed](<https://github.com/chdb-io/chdb-go/blob/main/chdbstable/chdb.go#L70>)

```go
func (r *LocalResult) Elapsed() float64
```



<a name="LocalResult.Len"></a>
### func \(\*LocalResult\) [Len](<https://github.com/chdb-io/chdb-go/blob/main/chdbstable/chdb.go#L63>)

```go
func (r *LocalResult) Len() int
```



<a name="LocalResult.RowsRead"></a>
### func \(\*LocalResult\) [RowsRead](<https://github.com/chdb-io/chdb-go/blob/main/chdbstable/chdb.go#L77>)

```go
func (r *LocalResult) RowsRead() uint64
```



<a name="LocalResult.String"></a>
### func \(LocalResult\) [String](<https://github.com/chdb-io/chdb-go/blob/main/chdbstable/chdb.go#L55>)

```go
func (r LocalResult) String() string
```

Stringer interface for LocalResult

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
