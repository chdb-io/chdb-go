//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/chdb-io/chdb-go/v2/chdb"
)

// udfSession opens a session on the engine selected the way TestQueryBatchWithCore
// selects it, and skips unless that engine can register scalar functions.
func udfSession(t *testing.T) *chdb.Session {
	t.Helper()
	path := os.Getenv("CHDB_ARROW_TEST_LIB")
	static := os.Getenv("CHDB_ARROW_TEST_STATIC") == "1"
	if !static && path == "" {
		t.Skip("set CHDB_ARROW_TEST_LIB to a libchdb with the scalar function ABI")
	}
	if !static {
		t.Setenv("CHDB_LIB_PATH", path)
	}
	sess, err := chdb.NewSession(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Close)
	capabilities, err := GetCapabilities(sess)
	if err != nil {
		t.Fatal(err)
	}
	if !capabilities.ScalarFunctions {
		t.Skipf("engine %q lacks the scalar function ABI", capabilities.EngineVersion)
	}
	return sess
}

// testFunc is a ScalarFunc built from a config and an executor.
type testFunc struct {
	config   ScalarFuncConfig
	executor ScalarFuncExecutor
}

func (f *testFunc) Config() ScalarFuncConfig     { return f.config }
func (f *testFunc) Executor() ScalarFuncExecutor { return f.executor }

func rowFunc(inputs []string, result string, fn RowExecutorFn) *testFunc {
	return &testFunc{ScalarFuncConfig{InputTypes: inputs, ResultType: result}, ScalarFuncExecutor{RowExecutor: fn}}
}

// register registers f and drops it when the test ends, so that -count=N reruns can
// register the same name again.
func register(t *testing.T, sess *chdb.Session, name string, f ScalarFunc) {
	t.Helper()
	if err := RegisterScalarUDF(sess, name, f); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	t.Cleanup(func() {
		if err := DropScalarUDF(sess, name); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
	})
}

func query(t *testing.T, sess *chdb.Session, sql string) (string, error) {
	t.Helper()
	res, err := sess.Query(sql, "CSV")
	if err != nil {
		return "", err
	}
	defer res.Free()
	if err := res.Error(); err != nil {
		return "", err
	}
	return res.String(), nil
}

func expectQuery(t *testing.T, sess *chdb.Session, sql, want string) {
	t.Helper()
	got, err := query(t, sess, sql)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	if got != want {
		t.Fatalf("%s:\n got %q\nwant %q", sql, got, want)
	}
}

func expectQueryError(t *testing.T, sess *chdb.Session, sql, fragment string) {
	t.Helper()
	got, err := query(t, sess, sql)
	if err == nil {
		t.Fatalf("%s: expected an error containing %q, got %q", sql, fragment, got)
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("%s: error %q does not contain %q", sql, err, fragment)
	}
}

func TestScalarUDFRowExecutor(t *testing.T) {
	sess := udfSession(t)
	var calls, nulls atomic.Int64
	register(t, sess, "go_plus_one", rowFunc([]string{"Int64"}, "Int64", func(values []driver.Value) (any, error) {
		calls.Add(1)
		if values[0] == nil {
			nulls.Add(1)
			return nil, nil
		}
		return values[0].(int64) + 1, nil
	}))
	register(t, sess, "go_upper", rowFunc([]string{"String"}, "String", func(values []driver.Value) (any, error) {
		return strings.ToUpper(values[0].(string)), nil
	}))

	expectQuery(t, sess, "SELECT go_plus_one(toInt64(number)) FROM numbers(3)", "1\n2\n3\n")
	expectQuery(t, sess, "SELECT go_plus_one(toInt32(-5)), toTypeName(go_plus_one(1))", "-4,\"Int64\"\n")
	expectQuery(t, sess, "SELECT go_upper('héllo'), go_upper(toString(number)) FROM numbers(1)", "\"HÉLLO\",\"0\"\n")
	// Many blocks, on several threads at once.
	expectQuery(t, sess, "SELECT sum(go_plus_one(toInt64(number))) FROM numbers_mt(1000000) SETTINGS max_threads = 8, max_block_size = 8192", "500000500000\n")

	// Rows with a NULL argument yield NULL without a call.
	before := calls.Load()
	expectQuery(t, sess, "SELECT go_plus_one(if(number % 2 = 0, NULL, toInt64(number))) FROM numbers(4)", "\\N\n2\n\\N\n4\n")
	if calls.Load()-before != 2 || nulls.Load() != 0 {
		t.Fatalf("calls = %d, nulls seen = %d; want 2 calls and no NULL", calls.Load()-before, nulls.Load())
	}

	// A process-wide function is visible to database/sql connections too.
	db, err := sql.Open("chdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got int64
	if err := db.QueryRow("SELECT go_plus_one(41)").Scan(&got); err != nil || got != 42 {
		t.Fatalf("database/sql: %d, %v", got, err)
	}
}

func TestScalarUDFValueTypes(t *testing.T) {
	sess := udfSession(t)
	register(t, sess, "go_describe", rowFunc(
		[]string{"Array(Int64)", "Tuple(a UInt8, b String)", "Map(String, Float64)", "DateTime64(3, 'UTC')", "Decimal(18, 2)", "UUID", "Enum8('x' = 1)", "Date"},
		"String",
		func(values []driver.Value) (any, error) {
			return fmt.Sprintf("%v|%v|%v|%s|%v|%v|%v|%s",
				values[0], values[1], values[2], values[3].(time.Time).Format(time.RFC3339Nano),
				values[4], values[5], values[6], values[7].(time.Time).Format(time.DateOnly)), nil
		}))
	expectQuery(t, sess, `SELECT go_describe([1, 2], (7, 'z'), map('k', 1.5),
		toDateTime64('2024-01-02 03:04:05.678', 3, 'UTC'), toDecimal64('-12.34', 2),
		toUUID('123e4567-e89b-12d3-a456-426614174000'), CAST('x', 'Enum8(\'x\' = 1)'), toDate('2024-02-29'))`,
		"\"[1 2]|map[a:7 b:z]|map[k:1.5]|2024-01-02T03:04:05.678Z|-12.34|123e4567-e89b-12d3-a456-426614174000|x|2024-02-29\"\n")

	// Results convert to the declared type: a string parses as a Decimal, a time as a DateTime64.
	register(t, sess, "go_decimal", rowFunc([]string{"String"}, "Decimal(10, 2)", func(values []driver.Value) (any, error) {
		return values[0], nil
	}))
	expectQuery(t, sess, "SELECT go_decimal('12.34'), toTypeName(go_decimal('1'))", "12.34,\"Decimal(10, 2)\"\n")
	register(t, sess, "go_add_hour", rowFunc([]string{"DateTime64(3, 'UTC')"}, "DateTime64(3, 'UTC')", func(values []driver.Value) (any, error) {
		return values[0].(time.Time).Add(time.Hour), nil
	}))
	expectQuery(t, sess, "SELECT go_add_hour(toDateTime64('2024-01-02 03:04:05.678', 3, 'UTC'))", "\"2024-01-02 04:04:05.678\"\n")

	// A value that does not fit the declared type is an error.
	register(t, sess, "go_narrow", rowFunc([]string{"Int64"}, "UInt8", func(values []driver.Value) (any, error) {
		return values[0].(int64) * 100, nil
	}))
	expectQuery(t, sess, "SELECT go_narrow(2)", "200\n")
	expectQueryError(t, sess, "SELECT go_narrow(3)", "while converting the result of Scalar function 'go_narrow'")
}

func TestScalarUDFNullHandling(t *testing.T) {
	sess := udfSession(t)
	register(t, sess, "go_is_null", &testFunc{
		ScalarFuncConfig{InputTypes: []string{"String"}, ResultType: "Bool", SpecialNullHandling: true},
		ScalarFuncExecutor{RowExecutor: func(values []driver.Value) (any, error) { return values[0] == nil, nil }},
	})
	expectQuery(t, sess, "SELECT go_is_null(if(number = 1, NULL, 'a')), toTypeName(go_is_null(toNullable('a'))) FROM numbers(3)",
		"false,\"Bool\"\ntrue,\"Bool\"\nfalse,\"Bool\"\n")

	register(t, sess, "go_bytes_len", &testFunc{
		ScalarFuncConfig{InputTypes: []string{"String"}, ResultType: "UInt64", BinaryStrings: true},
		ScalarFuncExecutor{RowExecutor: func(values []driver.Value) (any, error) { return len(values[0].([]byte)), nil }},
	})
	expectQuery(t, sess, "SELECT go_bytes_len(unhex('FF00FF'))", "3\n")
}

func TestScalarUDFBatchExecutor(t *testing.T) {
	sess := udfSession(t)
	var batches atomic.Int64
	register(t, sess, "go_add", &testFunc{
		ScalarFuncConfig{InputTypes: []string{"Int64", "Int64"}, ResultType: "Int64"},
		ScalarFuncExecutor{BatchExecutor: func(args arrow.RecordBatch) (arrow.Array, error) {
			batches.Add(1)
			left := args.Column(0).(*array.Int64)
			right := args.Column(1).(*array.Int64)
			b := array.NewInt64Builder(memory.DefaultAllocator)
			defer b.Release()
			for i := 0; i < left.Len(); i++ {
				b.Append(left.Value(i) + right.Value(i))
			}
			return b.NewArray(), nil
		}},
	})
	expectQuery(t, sess, "SELECT sum(go_add(toInt64(number), 1)) FROM numbers(200000)", "20000100000\n")
	if batches.Load() < 2 {
		t.Fatalf("expected one call per block, got %d calls", batches.Load())
	}

	register(t, sess, "go_short", &testFunc{
		ScalarFuncConfig{InputTypes: []string{"Int64"}, ResultType: "Int64"},
		ScalarFuncExecutor{BatchExecutor: func(args arrow.RecordBatch) (arrow.Array, error) {
			return array.NewNull(int(args.NumRows()) + 1), nil
		}},
	})
	expectQueryError(t, sess, "SELECT go_short(1)", "BatchExecutor returned 2 values for 1 rows")
}

func TestScalarUDFErrors(t *testing.T) {
	sess := udfSession(t)
	register(t, sess, "go_fail", rowFunc([]string{"Int64"}, "Int64", func(values []driver.Value) (any, error) {
		if values[0].(int64) == 0 {
			return nil, errors.New("zero is not allowed")
		}
		return values[0], nil
	}))
	expectQueryError(t, sess, "SELECT go_fail(toInt64(number)) FROM numbers(2)", "zero is not allowed")
	// Short-circuit evaluation keeps the rows a branch does not take away from the function.
	expectQuery(t, sess, "SELECT if(number > 0, go_fail(toInt64(number)), -1) FROM numbers(2)", "-1\n1\n")

	register(t, sess, "go_panic", rowFunc([]string{"Int64"}, "Int64", func(values []driver.Value) (any, error) {
		panic("kaboom")
	}))
	expectQueryError(t, sess, "SELECT go_panic(1)", "panic: kaboom")
	// The process survives and the session keeps working.
	expectQuery(t, sess, "SELECT 1", "1\n")

	register(t, sess, "go_wrong_type", rowFunc([]string{"Int64"}, "Int64", func(values []driver.Value) (any, error) {
		return struct{}{}, nil
	}))
	expectQueryError(t, sess, "SELECT go_wrong_type(1)", "use BatchExecutor")

	expectQueryError(t, sess, "SELECT go_fail('x')", "does not convert to the declared Int64")
}

func TestScalarUDFVolatile(t *testing.T) {
	sess := udfSession(t)
	var counter atomic.Int64
	next := func(values []driver.Value) (any, error) { return counter.Add(1), nil }
	register(t, sess, "go_next", &testFunc{ScalarFuncConfig{ResultType: "Int64"}, ScalarFuncExecutor{RowExecutor: next}})
	register(t, sess, "go_next_volatile", &testFunc{ScalarFuncConfig{ResultType: "Int64", Volatile: true}, ScalarFuncExecutor{RowExecutor: next}})

	got, err := query(t, sess, "SELECT count(DISTINCT go_next()), count(DISTINCT go_next_volatile()) FROM numbers(5)")
	if err != nil {
		t.Fatal(err)
	}
	// A deterministic call without arguments is folded into one value; a volatile one runs per row.
	if got != "1,5\n" {
		t.Fatalf("distinct values = %q, want \"1,5\\n\"", got)
	}
}

func TestScalarUDFRegistration(t *testing.T) {
	sess := udfSession(t)
	identity := rowFunc([]string{"Int64"}, "Int64", func(values []driver.Value) (any, error) { return values[0], nil })

	live := liveScalarFunctions.Load()
	if err := RegisterScalarUDF(sess, "go_identity", identity); err != nil {
		t.Fatal(err)
	}
	if err := RegisterScalarUDF(sess, "go_identity", identity); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate registration: %v", err)
	}
	if liveScalarFunctions.Load() != live+1 {
		t.Fatalf("a failed registration must not keep its handle: %d live, want %d", liveScalarFunctions.Load(), live+1)
	}
	expectQuery(t, sess, "SELECT go_identity(5)", "5\n")
	if err := DropScalarUDF(sess, "go_identity"); err != nil {
		t.Fatal(err)
	}
	if liveScalarFunctions.Load() != live {
		t.Fatalf("drop must release the handle: %d live, want %d", liveScalarFunctions.Load(), live)
	}
	expectQueryError(t, sess, "SELECT go_identity(5)", "UNKNOWN_FUNCTION")
	if err := DropScalarUDF(sess, "go_identity"); err == nil {
		t.Fatal("dropping an unregistered function must fail")
	}

	for name, f := range map[string]ScalarFunc{
		"plus":        identity,
		"go_bad_type": rowFunc([]string{"NotAType"}, "Int64", identity.executor.RowExecutor),
		"go_no_exec":  &testFunc{ScalarFuncConfig{ResultType: "Int64"}, ScalarFuncExecutor{}},
		"go_no_type":  rowFunc(nil, "", identity.executor.RowExecutor),
	} {
		if err := RegisterScalarUDF(sess, name, f); err == nil {
			t.Fatalf("registering %s must fail", name)
		}
	}
	if liveScalarFunctions.Load() != live {
		t.Fatalf("failed registrations leaked handles: %d live, want %d", liveScalarFunctions.Load(), live)
	}
}
