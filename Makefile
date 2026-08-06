.PHONY: update_libchdb all test clean verify-as-user

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

# update_libchdb.sh drops the engine in the repository root, but test binaries
# run from a temporary directory and so never look there. Point the loader at
# that copy when it exists, so `make update_libchdb && make test` works without
# a system-wide install.
test:
	@if [ -f "$(CURDIR)/libchdb.so" ] && [ -z "$$CHDB_LIB_PATH" ]; then \
		echo "using $(CURDIR)/libchdb.so"; \
		CHDB_LIB_PATH="$(CURDIR)/libchdb.so" go test -v -coverprofile=coverage.out ./...; \
	else \
		go test -v -coverprofile=coverage.out ./...; \
	fi

# Consume this commit the way a user does: package it as the module proxy
# would, then go get and run it from outside the repository.
verify-as-user:
	./.github/scripts/verify-as-user.sh

run:
	go run main.go

build:
	go build -o chdb-go main.go
