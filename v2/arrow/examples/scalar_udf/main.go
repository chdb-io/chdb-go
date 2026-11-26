// A runnable example of scalar user-defined functions written in Go: one
// computed per row with Go values, one per batch with Arrow arrays. Set
// CHDB_LIB_PATH to a libchdb with the scalar function ABI before running.
package main

import (
	"database/sql/driver"
	"fmt"
	"log"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	chdbarrow "github.com/chdb-io/chdb-go/v2/arrow"
	"github.com/chdb-io/chdb-go/v2/chdb"
)

// titleCase is computed one row at a time.
type titleCase struct{}

func (titleCase) Config() chdbarrow.ScalarFuncConfig {
	return chdbarrow.ScalarFuncConfig{InputTypes: []string{"String"}, ResultType: "String"}
}

func (titleCase) Executor() chdbarrow.ScalarFuncExecutor {
	return chdbarrow.ScalarFuncExecutor{RowExecutor: func(values []driver.Value) (any, error) {
		words := strings.Fields(values[0].(string))
		for i, w := range words {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
		return strings.Join(words, " "), nil
	}}
}

// weightedSum is computed one batch at a time with Arrow arrays.
type weightedSum struct{}

func (weightedSum) Config() chdbarrow.ScalarFuncConfig {
	return chdbarrow.ScalarFuncConfig{InputTypes: []string{"Float64", "Float64"}, ResultType: "Float64"}
}

func (weightedSum) Executor() chdbarrow.ScalarFuncExecutor {
	return chdbarrow.ScalarFuncExecutor{BatchExecutor: func(args arrow.RecordBatch) (arrow.Array, error) {
		values := args.Column(0).(*array.Float64)
		weights := args.Column(1).(*array.Float64)
		b := array.NewFloat64Builder(memory.DefaultAllocator)
		defer b.Release()
		for i := 0; i < values.Len(); i++ {
			b.Append(values.Value(i) * weights.Value(i))
		}
		return b.NewArray(), nil
	}}
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	sess, err := chdb.NewSession(":memory:")
	if err != nil {
		return err
	}
	defer sess.Close()

	caps, err := chdbarrow.GetCapabilities(sess)
	if err != nil {
		return err
	}
	if !caps.ScalarFunctions {
		return fmt.Errorf("libchdb %s cannot register scalar functions", caps.EngineVersion)
	}

	for name, f := range map[string]chdbarrow.ScalarFunc{"title_case": titleCase{}, "weighted": weightedSum{}} {
		if err := chdbarrow.RegisterScalarUDF(sess, name, f); err != nil {
			return err
		}
		defer chdbarrow.DropScalarUDF(sess, name)
	}

	res, err := sess.Query(`
		SELECT title_case(name), weighted(price, quantity)
		FROM values('name String, price Float64, quantity Float64',
		            ('green tea', 3.5, 2), ('black coffee', 4, 3))`, "PrettyCompact")
	if err != nil {
		return err
	}
	defer res.Free()
	fmt.Print(res.String())
	return nil
}
