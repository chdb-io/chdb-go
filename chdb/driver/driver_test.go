package chdb

import (
  "testing"
  "database/sql"
  "fmt"
)

func TestDb(t *testing.T) {
    db, err := sql.Open("chdb", "")
    if err != nil {
      t.Errorf("open db fail")
    }
    if db.Ping() != nil {
      t.Errorf("ping db fail")
    }
    rows, err := db.Query("SELECT version()");
    if err != nil {
      t.Errorf("run Query fail, err:%s", err)
    }
    col, err := rows.Columns()
    fmt.Printf("col: %v\n", col)
}
