
#!/bin/bash

# The engine this repository is built and tested against. `make install` reads
# it too, so both ways of fetching libchdb land on the same build. Keep it on
# its own line and literal: the release check greps for it when it proposes a
# bump, and the Makefile cuts the value out of it.
CHDB_ENGINE_PIN=v26.7.0

# CHDB_ENGINE_VERSION overrides the pin, which is how the release check runs the
# suite against an engine this repository has not adopted yet.
LATEST_RELEASE="${CHDB_ENGINE_VERSION:-$CHDB_ENGINE_PIN}"

if [ -z "$LATEST_RELEASE" ]; then
    echo "No chdb-core release to download. Set CHDB_ENGINE_VERSION or fix CHDB_ENGINE_PIN." >&2
    exit 1
fi

# Download the correct version based on the platform
case "$(uname -s)" in
    Linux)
        if [[ $(uname -m) == "aarch64" ]]; then
            PLATFORM="linux-aarch64-libchdb.tar.gz"
        else
            PLATFORM="linux-x86_64-libchdb.tar.gz"
        fi
        ;;
    Darwin)
        if [[ $(uname -m) == "arm64" ]]; then
            PLATFORM="macos-arm64-libchdb.tar.gz"
        else
            PLATFORM="macos-x86_64-libchdb.tar.gz"
        fi
        ;;
    *)
        echo "Unsupported platform"
        exit 1
        ;;
esac

DOWNLOAD_URL="https://github.com/chdb-io/chdb-core/releases/download/$LATEST_RELEASE/$PLATFORM"

echo "Downloading $PLATFORM from $DOWNLOAD_URL"

# Download the file
curl -L -o libchdb.tar.gz $DOWNLOAD_URL

# Untar the file
tar -xzf libchdb.tar.gz

# Set execute permission for libchdb.so
chmod +x libchdb.so

# Clean up
rm -f libchdb.tar.gz
