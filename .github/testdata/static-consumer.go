package main

import (
	"errors"
	"fmt"
	"log"

	"github.com/chdb-io/chdb-go/v2/chdb"
	chdbpurego "github.com/chdb-io/chdb-go/v2/chdb-purego"
)

func main() {
	if _, err := chdbpurego.LoadedLibraryPath(); !errors.Is(err, chdbpurego.ErrStaticallyLinked) {
		log.Fatalf("expected a static engine, got %v", err)
	}
	version, err := chdbpurego.Version()
	if err != nil {
		log.Fatal(err)
	}
	res, err := chdb.Query("SELECT 42", "CSV")
	if err != nil {
		log.Fatal(err)
	}
	defer res.Free()
	fmt.Printf("%s:%s", version, res.String())
}
