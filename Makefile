.PHONY: update_libchdb all test clean

update_libchdb:
	./update_libchdb.sh

install:
	curl -sL https://lib.chdb.io | bash

test:
	go test -v -coverprofile=coverage.out ./...

run:
	go run main.go

build:
	go build -o chdb-go main.go
