.PHONY: update_libchdb all test clean

update_libchdb:
	./update_libchdb.sh

# lib.chdb.io installs system-wide, which is what the CLI needs; update_libchdb.sh
# only drops the engine in the repository root. Both read the same pin, so the two
# cannot end up on different builds — left to itself the installer takes whatever
# chdb-core released most recently, which made an unrelated commit's CI go red on
# the day of a release.
install:
	@export INSTALL_VERSION="$${CHDB_ENGINE_VERSION:-$$(grep -E '^CHDB_ENGINE_PIN=' update_libchdb.sh | cut -d= -f2)}"; \
	echo "installing libchdb $$INSTALL_VERSION"; \
	curl -sL https://lib.chdb.io | bash

test:
	go test -v -coverprofile=coverage.out ./...

run:
	go run main.go

build:
	go build -o chdb-go main.go
