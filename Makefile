.PHONY: update_libchdb all test clean

update_libchdb:
	./update_libchdb.sh

install:
	curl -sL https://lib.chdb.io | bash

test:
	CGO_ENABLED=1 go test -v -coverprofile=coverage.out ./...

run:
	CGO_ENABLED=1 go run main.go

build:
	CGO_ENABLED=1 go build -ldflags '-extldflags "-Wl,-rpath,/usr/local/lib"' -o chdb-go main.go
