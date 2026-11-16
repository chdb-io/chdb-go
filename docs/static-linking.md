# 可选静态引擎构建（实验性）

默认 `go build` 不需要 cgo，也不会链接 `libchdb.a`；它通过 purego 在运行时加载 `libchdb`。`lib/embedded` 则把动态库随程序携带，首次运行仍要解压。只有显式的 `chdb_static` 标签会走下面的静态 cgo adapter。这里的“单文件”指运行时不另带 `libchdb`，**不是**完全静态的系统可执行文件。

目前仅在 macOS/arm64 使用 chdb-core `v26.11.0-rc.3` 正式静态归档完成本地查询与测试；Linux/amd64、Linux/arm64 和 macOS/amd64 尚待 CI 验证。静态归档须由社区或 chdb-core 提供，和随包 `chdb.h` 的 ABI 匹配，并公布来源及 SHA-256。仓库的 `update_libchdb.sh` 下载的是动态库，不能用于以下构建。

## macOS/arm64

从 [chdb-core v26.11.0-rc.3](https://github.com/chdb-io/chdb-core/releases/tag/v26.11.0-rc.3) 取得发布包，并在应用的 Go module 目录中运行（下面使用 GitHub CLI；目标路径不要含空格）：

```sh
CHDB_STATIC_DIR=$(mktemp -d)
gh release download v26.11.0-rc.3 -R chdb-io/chdb-core \
  -p macos-arm64-libchdb-static.tar.gz -D "$CHDB_STATIC_DIR"
echo "20e215f2d7e9ea1dc98bae47d61dc13bcf76fc19ca86ed05b410410e91f3c047  $CHDB_STATIC_DIR/macos-arm64-libchdb-static.tar.gz" | shasum -a 256 -c -
tar -xzf "$CHDB_STATIC_DIR/macos-arm64-libchdb-static.tar.gz" -C "$CHDB_STATIC_DIR"
CGO_ENABLED=1 \
  CGO_CFLAGS="-DCHDB_STATIC_HEADER=\"$CHDB_STATIC_DIR/chdb.h\"" \
  CGO_LDFLAGS="-Wl,-force_load,$CHDB_STATIC_DIR/libchdb.a" \
  go build -tags chdb_static -trimpath -o chdb-go-static .
otool -L ./chdb-go-static
env -u CHDB_LIB_PATH ./chdb-go-static 'SELECT 42' CSV
```

`force_load` 保留只有静态初始化器引用的 SQL 函数。期望 `otool -L` 没有 `libchdb.so`/`.dylib`，但仍可能列出 `libSystem`、`libiconv` 和系统 framework。仅 macOS/arm64 已做过本地最小查询，不代表所有 macOS 版本均支持；部署目标和签名仍须逐个产物验证。

## Linux/amd64（待 CI 实证）

对应平台须使用 Linux 静态包，不得复用 macOS `.a`。以下是待 CI 实证的构建配方，不能据此认定任意 glibc 系统可用：

```sh
CHDB_STATIC_DIR=$(mktemp -d)
gh release download v26.11.0-rc.3 -R chdb-io/chdb-core \
  -p linux-x86_64-libchdb-static.tar.gz -D "$CHDB_STATIC_DIR"
echo "c3edc9a0d0ed5df62136b9fb642f40c7f56802eaa5bc1c9b499ecd916f1fd524  $CHDB_STATIC_DIR/linux-x86_64-libchdb-static.tar.gz" | sha256sum -c -
tar -xzf "$CHDB_STATIC_DIR/linux-x86_64-libchdb-static.tar.gz" -C "$CHDB_STATIC_DIR"
CGO_ENABLED=1 \
  CGO_CFLAGS="-DCHDB_STATIC_HEADER=\"$CHDB_STATIC_DIR/chdb.h\"" \
  CGO_LDFLAGS_ALLOW='-Wl,(--whole-archive|--no-whole-archive)|-l:libchdb[.]a' \
  CGO_LDFLAGS="-L$CHDB_STATIC_DIR" \
  go build -tags chdb_static -trimpath -o chdb-go-static .
readelf -d ./chdb-go-static
readelf -V ./chdb-go-static
env -u CHDB_LIB_PATH ./chdb-go-static 'SELECT 42' CSV
```

Linux 的 `-l:libchdb.a` 会按文件名精确选择 `.a`，`-L` 指向已校验的发布包目录；归档只由静态 adapter 的一处 `#cgo` 链接声明强制装载。不要再把 `--whole-archive` 和归档路径放入 `CGO_LDFLAGS`：Go 汇总多个 cgo 包时可能重复装载。必须把**同一份**已编译产物放进无 `libchdb` 的最低 glibc 和较新 glibc 环境运行；仅在构建机成功、或只看 `ldd`，均不足以声称可移植。Alpine/musl 需要独立的 musl 归档、工具链和验证，不属于此 Linux/amd64 glibc 路径。

## 另外两个 CI 架构（待 CI 实证）

macOS/amd64 使用 `macos-x86_64-libchdb-static.tar.gz`，SHA-256 为 `f70e8ce130e0625cd75906fa8593a9c3b03014864a7d7e61ce7e2a07ec901a53`；构建参数与上面的 macOS/arm64 示例相同，必须在 amd64 机器上构建。Linux/arm64 使用 `linux-aarch64-libchdb-static.tar.gz`，SHA-256 为 `5d874ec81a96715830db2439ac824f0219fc9fbf348b88eefdbec58b8d5b2a96`，构建参数与上面的 Linux/amd64 示例相同，并应在 arm64 上运行而不是仅交叉编译。

## 构建契约与诊断

- `chdb_static` 的候选目标为 `darwin/arm64`、`darwin/amd64`、`linux/amd64`、`linux/arm64` 且 `CGO_ENABLED=1`；目前只有 macOS/arm64 有本地验证结果，四平台完整 CI 仍需最终通过。不支持的组合应在编译时报错。必须通过 macOS 的精确归档路径或 Linux 的 `-L` 加 `-l:libchdb.a` 选中已校验的归档，而不是运行时寻找动态库；普通 `-lchdb` 可能优先选到 `.so`，不应作为静态验证依据。
- `CHDB_LIB_PATH` 在静态构建中无效；若设置会在首次使用引擎时报错。不要同时空白导入 `lib/embedded`。`chdbpurego.LoadedLibraryPath()` 对静态构建返回 `ErrStaticallyLinked`，因为根本没有运行时加载的文件；使用 `chdbpurego.Version()` 查询实际引擎版本。
- 静态 adapter 当前要求至少具备 chdb-core `v26.11.0-rc.3` 的完整 C ABI；旧归档缺少的管理/Arrow 等符号会令链接失败。检查归档是否与头文件版本一致，不可把动态库加在链接参数后面来“修好”静态构建。默认动态 adapter 仍可针对较旧引擎按能力降级。
- 最终产物中的 libc、C++ 运行库和系统框架依赖须用 `readelf`/`otool` 审计，并在目标系统上实测。macOS 系统库不提供“全静态”承诺；Linux glibc 的最低版本取决于构建基线及最终符号版本。没有经验证的最低支持版本时，按未验证处理。

社区维护者可提供按平台和引擎版本固定的归档、校验和、构建参数与兼容性测试结果；这些原生字节不进入默认 `chdb-go/v2` 根模块。详见 [实施计划](../STATIC_LINKING_PLAN.md)。
