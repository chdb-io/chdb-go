package chdbdriver

import (
	"database/sql"
	"strings"
	"testing"
)

// A ClickHouse Array, Map or Tuple arrives as a parquet group, and Kind() panics
// on those: Rows.ColumnTypes() — which ORMs and generic row scanners call for
// every query — used to die with "cannot call Kind on parquet group". Worse, it
// panicked inside database/sql while it held the connection's lock.
//
// The columns still have no mapping, so reading them must fail with an error that
// says so. An Array used to be worse than unsupported: the row came back with that
// column NULL, the columns after it NULL too, and rows.Err() nil — data, not a
// complaint.
func TestGroupColumnsAreReportedNotFatal(t *testing.T) {
	for _, driverType := range []string{"PARQUET", "PARQUET_STREAMING"} {
		for _, tc := range []struct {
			name  string
			query string
		}{
			{"array", "SELECT 7 AS x, [1,2,3] AS arr"},
			{"map", "SELECT map('k', 1) AS m"},
			{"tuple", "SELECT tuple(1, 'a') AS t"},
		} {
			t.Run(driverType+"/"+tc.name, func(t *testing.T) {
				db, err := sql.Open("chdb", "driverType="+driverType)
				if err != nil {
					t.Fatalf("open db fail, err:%s", err)
				}
				defer db.Close()

				rows, err := db.Query(tc.query)
				if err != nil {
					t.Fatalf("run Query fail, err:%s", err)
				}
				defer rows.Close()

				// The call that used to panic.
				cts, err := rows.ColumnTypes()
				if err != nil {
					t.Fatalf("ColumnTypes fail, err:%s", err)
				}
				for i, ct := range cts {
					// Callers build a scan destination from this with reflect.New,
					// which panics on a nil type.
					if ct.ScanType() == nil {
						t.Errorf("column %d (%s) has no scan type", i, ct.Name())
					}
				}

				if rows.Next() {
					t.Error("Next() returned a row for a column type the driver cannot read")
				}
				err = rows.Err()
				if err == nil {
					t.Fatal("reading an unsupported column type reported no error")
				}
				if !strings.Contains(err.Error(), "column") {
					t.Errorf("error %q does not say which column could not be read", err)
				}
			})
		}
	}
}

// The mapped types keep working, and their scan types stay what they were.
func TestColumnTypesOfMappedColumns(t *testing.T) {
	db, err := sql.Open("chdb", "driverType=PARQUET")
	if err != nil {
		t.Fatalf("open db fail, err:%s", err)
	}
	defer db.Close()

	rows, err := db.Query("SELECT toInt32(-2) AS i, toUInt64(3) AS u, toFloat64(4.5) AS f, 'text' AS s, true AS b")
	if err != nil {
		t.Fatalf("run Query fail, err:%s", err)
	}
	defer rows.Close()

	cts, err := rows.ColumnTypes()
	if err != nil {
		t.Fatalf("ColumnTypes fail, err:%s", err)
	}
	want := []string{"int32", "int64", "float64", "string", "bool"}
	if len(cts) != len(want) {
		t.Fatalf("got %d columns, want %d", len(cts), len(want))
	}
	for i, ct := range cts {
		if ct.ScanType() == nil {
			t.Fatalf("column %s has no scan type", ct.Name())
		}
		if got := ct.ScanType().String(); got != want[i] {
			t.Errorf("column %s scan type = %s, want %s", ct.Name(), got, want[i])
		}
	}
	if !rows.Next() {
		t.Fatalf("no row returned, err:%v", rows.Err())
	}
}
