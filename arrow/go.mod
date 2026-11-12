module github.com/chdb-io/chdb-go/v2/arrow

go 1.23.0

// The root module version must be advanced to a release containing the
// internal/nativearrow bridge before this submodule can be published.

require (
	github.com/apache/arrow-go/v18 v18.4.1
	github.com/chdb-io/chdb-go/v2 v2.2.0
)
