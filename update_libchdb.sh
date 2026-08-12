
#!/bin/bash

# CHDB_ENGINE_VERSION pins the engine to one chdb-core release. Set it to test
# against a specific engine; leave it unset to take whatever is newest.
LATEST_RELEASE="${CHDB_ENGINE_VERSION:-$(curl --silent "https://api.github.com/repos/chdb-io/chdb-core/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')}"

if [ -z "$LATEST_RELEASE" ]; then
    echo "Could not determine a chdb-core release to download. Set CHDB_ENGINE_VERSION to pick one." >&2
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
