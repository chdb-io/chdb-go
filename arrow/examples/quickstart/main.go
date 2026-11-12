// A runnable example of in-memory Arrow input, typed output, and streaming
// output. Set CHDB_LIB_PATH to a chdb-core v26.11.0-rc.3+ libchdb before running.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	chdbarrow "github.com/chdb-io/chdb-go/v2/arrow"
	"github.com/chdb-io/chdb-go/v2/chdb"
)

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
	fmt.Printf("engine: %s, Arrow input: %t, output: %t\n",
		caps.EngineVersion, caps.Input, caps.TableOutput)

	schema := arrow.NewSchema([]arrow.Field{{Name: "id", Type: arrow.PrimitiveTypes.Int64}}, nil)
	builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer builder.Release()
	builder.Field(0).(*array.Int64Builder).AppendValues([]int64{7, 5}, nil)
	batch := builder.NewRecordBatch()
	defer batch.Release()

	// QueryBatch copies the input into native-owned buffers and closes its
	// temporary SQL source before returning. The caller still owns batch.
	out, err := chdbarrow.QueryBatch(sess, batch, func(source string) string {
		return "SELECT sum(id) FROM " + source
	}, "TSV")
	if err != nil {
		return err
	}
	fmt.Printf("sum: %s", out)
	created, err := sess.Query("CREATE TEMPORARY TABLE arrow_demo (id Int64)", "TSV")
	if err != nil {
		return err
	}
	created.Free()
	input, err := array.NewRecordReader(schema, []arrow.RecordBatch{batch, batch})
	if err != nil {
		return err
	}
	err = chdbarrow.InsertReader(sess, "arrow_demo", input)
	input.Release() // InsertReader retains only while it is running
	if err != nil {
		return err
	}
	inserted, err := sess.Query("SELECT count(), sum(id) FROM arrow_demo", "TSV")
	if err != nil {
		return err
	}
	fmt.Printf("inserted: %s", inserted.String())
	inserted.Free()

	result, err := chdbarrow.QueryTable(sess, "SELECT toInt64(7) AS id")
	if err != nil {
		return err
	}
	fmt.Printf("typed table: %d row(s), schema %s\n", result.NumRows(), result.Schema())
	result.Release()

	reader, err := chdbarrow.QueryReader(context.Background(), sess,
		"SELECT number FROM numbers(5) SETTINGS max_block_size=2")
	if err != nil {
		return err
	}
	defer reader.Close() // releases a partially consumed result too
	for reader.Next() {
		fmt.Printf("received %d row(s) in a batch\n", reader.RecordBatch().NumRows())
	}
	return reader.Err()
}
