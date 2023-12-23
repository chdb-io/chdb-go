.PHONY: update_libchdb all test clean

update_libchdb:
	./update_libchdb.sh

install: update_libchdb
	@echo "All the tests an main.go will search libchdb.so in the Current Working Directory"
	@echo "We perfer to put libchdb.so in /usr/local/lib, so you can run"
	@echo "'sudo cp -a libchdb.so /usr/local/lib' or 'make install' to do it"
	@echo "You can also put it in other places, but you need to set LD_LIBRARY_PATH on Linux or DYLD_LIBRARY_PATH on macOS"
	chmod +x libchdb.so
	sudo cp -a libchdb.so /usr/local/lib
	# if on Linux run `sudo ldconfig` to update the cache
	# if on macOS run `sudo update_dyld_shared_cache` to update the cache
test:
	CGO_ENABLED=1 go test -v -coverprofile=coverage.out ./...

run:
	CGO_ENABLED=1 go run main.go

build:
	CGO_ENABLED=1 go build -o chdb-go main.go
