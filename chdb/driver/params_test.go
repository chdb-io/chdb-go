package chdbdriver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"
)

func TestNamedParametersUseNativeBinding(t *testing.T) {
	for _, driverType := range []string{"PARQUET", "PARQUET_STREAMING"} {
		t.Run(driverType, func(t *testing.T) {
			db, err := sql.Open("chdb", "driverType="+driverType)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var n int64
			if err := db.QueryRowContext(context.Background(), "SELECT {n:Int64} + 1", sql.Named("n", int64(41))).Scan(&n); err != nil || n != 42 {
				t.Fatalf("bound integer=%d err=%v", n, err)
			}
			value := "a' OR 1=1 --"
			var text string
			if err := db.QueryRowContext(context.Background(), "SELECT {v:String}", sql.Named("v", value)).Scan(&text); err != nil || text != value {
				t.Fatalf("bound string=%q err=%v", text, err)
			}
			if err := db.QueryRowContext(context.Background(), "SELECT {v:String}").Scan(&text); err == nil {
				t.Fatal("per-call parameter leaked into a later query")
			}
			stamp := time.Date(2026, 11, 13, 9, 12, 13, 123000000, time.UTC)
			var formatted string
			if err := db.QueryRowContext(context.Background(), "SELECT toString({ts:DateTime64(3)})", sql.Named("ts", stamp)).Scan(&formatted); err != nil || formatted != "2026-11-13 09:12:13.123" {
				t.Fatalf("bound timestamp=%q err=%v", formatted, err)
			}
		})
	}
}

func TestNamedParametersRejectMixedAndDuplicate(t *testing.T) {
	for _, args := range [][]driver.NamedValue{
		{{Name: "x", Value: 1}, {Ordinal: 2, Value: 2}},
		{{Name: "x", Value: 1}, {Name: "x", Value: 2}},
		{{Name: "x-y", Value: 1}},
	} {
		if _, _, err := namedParameters(args); err == nil {
			t.Fatalf("accepted invalid arguments: %#v", args)
		}
	}
}
