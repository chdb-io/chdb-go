package chdb

import (
	"database/sql"
	"testing"
)

func TestDb(t *testing.T) {
	db, err := sql.Open("chdb", "")
	if err != nil {
		t.Errorf("open db fail")
	}
	if db.Ping() != nil {
		t.Errorf("ping db fail")
	}
	{
		rows, err := db.Query(`SELECT 1,'abc'`)
		if err != nil {
			t.Errorf("run Query fail, err:%s", err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Errorf("get result columns fail, err: %s", err)
		}
		if len(cols) != 2 {
			t.Errorf("select version(), result columns length should be 1")
		}
		var (
			bar int
			foo string
		)
		defer rows.Close()
		for rows.Next() {
			err := rows.Scan(&bar, &foo)
			if err != nil {
				t.Errorf("scan fail, err: %s", err)
			}
			if bar != 1 {
				t.Errorf("expected error")
			}
			if foo != "abc" {
				t.Errorf("expected error")
			}
		}
	}
}
