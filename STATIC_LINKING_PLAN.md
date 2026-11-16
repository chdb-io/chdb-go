# chdb-go 可选 cgo 静态引擎实施计划

状态：实施中。macOS/arm64 已用 chdb-core `v26.11.0-rc.3` 静态发布包完成最小查询、根模块全量测试、Arrow 测试、选定 `-race` 测试，以及从当前本地提交生成的 Go module zip 经本地模块代理在仓库外构建运行；同版本动态库的默认无 cgo 全量测试也通过。新的 CI 目标是四个架构（Linux/amd64、Linux/arm64、macOS/arm64、macOS/amd64）均运行静态构建和完整用例；其余三个架构及远端 CI 尚未验证。此处的“静态”指把 **libchdb 引擎**链接进可执行文件，不承诺 libc、macOS 系统库等也全部静态链接。

## 目标与不变量

- 默认 `go build` / `CGO_ENABLED=0 go build` 继续使用 purego、运行时寻找动态 `libchdb`；现有 `lib/embedded` 仍是“把动态库放进二进制，运行时解压加载”的独立方案。默认依赖、构建与发布产物不因新功能引入 C 编译器或静态库。
- 显式选择 `CGO_ENABLED=1 go build -tags chdb_static` 后，调用直接进入链接在可执行文件中的 `libchdb.a`；运行机器无需另装 `libchdb.so`/`.dylib`，也不应解压引擎。缺失静态库、架构不符或关闭 cgo 必须在构建时给出可诊断的失败，不能静默回退 purego。
- 上层 `chdb`、`chdb/driver` 和已有结果/连接的 Go interface 尽量不变。静态与动态构建的公共查询、流式查询、参数、管理接口和关闭语义应一致；非支持平台不宣称支持。
- 给用户提供一份可复制的“单文件运行时交付”指南；说明编译时仍需要匹配的原生归档、头文件、工具链及部分系统运行库，并公布实际验证过的系统范围。

## 阶段 0：取得并审计原生静态产物（前置条件）

1. 与 chdb-core 的版本 pin 对齐，取得各目标架构的 `libchdb.a`、对应 `chdb.h`、构建参数和 SHA-256。现有 `update_libchdb.sh` 和 `scripts/package-engine.sh` 处理的是动态库，不得将其重命名为 `.a` 复用。若上游尚未可靠发布 `.a`，先在上游定义可复现的产出流程、许可证/第三方依赖清单和版本来源，再进入下阶段。
2. 检查归档成员的架构、未定义符号及其对 C++ 标准库、libgcc/compiler-rt、libc、系统 framework/动态库的依赖。尤其核对 macOS 归档是否自带 C++ 标准库对象及其符号可替换行为；不要仅因链接成功就认定可运行。
3. 记录 Linux 最低 glibc 基线和 macOS 最低部署目标，包含构建容器/SDK/编译器版本。这里的基线必须由最终二进制的符号版本和低版本系统实测共同确定，不从当前 CI runner 的版本推断。

交付：四个目标架构可复现的原生归档及校验和、与头文件匹配的 ABI 版本、依赖清单；拿不到相应产物时不宣称该架构支持单文件路径。

## 阶段 1：在原生调用处引入两个 adapter

当前 `chdb-purego/binding.go` 的 `ensureLoaded`/`bindSymbols` 通过 purego 解析动态符号，`loader.go` 负责 `dlopen`；仅往链接器传 `.a` 并不会改变这个调用路径。以原生调用为 seam，保留上层连接、结果与并发控制实现，让默认 adapter 调用现有 purego，`chdb_static && cgo` adapter 使用 cgo 对 C ABI 的直接引用。避免在上层复制一套 `chdb` 或 SQL driver。

实施顺序：

1. 先盘点所有必需/可选符号、C 类型布局、字符串与缓冲区的所有权。把加载、符号能力检查、信号处理和调用相关代码分清哪些是共享逻辑、哪些只属于动态 adapter；给仅动态加载有效的测试加相应构建条件。
2. 加互斥构建条件，例如默认文件为 `!chdb_static`，静态文件要求 `chdb_static && cgo` 并限定四个目标 GOOS/GOARCH；为 `chdb_static && !cgo` 和未支持平台提供清晰的构建错误。不能让 `CGO_ENABLED=1` 本身改变默认路径，也不能让同时携带 `lib/embedded` 的程序悄悄选择另一个引擎。
3. 静态 adapter 的 C 调用应引用真实的引擎符号，并让链接命令指向**确切的归档路径**；单纯 `-lchdb` 在同时存在 `.so` 时可能选中动态库。平台链接选项由经验证的配方产生，不把构建机绝对路径写入最终产物。
4. 检查 cgo 指针规则：当前 `NewConnection` 构造 argv 的方式、`params.go` 中的指针数组、Arrow C Data Interface 的生命周期都要用 `GOEXPERIMENT=cgocheck2` 和实测覆盖。必要时只在静态 adapter 内分配/释放 C 缓冲区，不为此改变 purego 默认行为。
5. 静态构建要定义“引擎来源”诊断：`CHDB_LIB_PATH` 不可覆盖已静态链接的引擎；`LoadedLibraryPath()` 的现有“返回动态库路径”契约不能伪造文件名。可增加 `EngineInfo`/链接模式报告，并对旧函数在静态模式给出明确结果。可选 ABI 在静态模式可选用有版本下限的归档，或通过经过验证的弱符号/能力机制处理，不能盲目直连旧版没有的符号。

交付：默认代码路径不变；显式标签在四个目标架构上可构建并运行现有用例；两种 adapter 的行为测试通过。

## 阶段 2：本地验证与社区维护方式

- 本地先在 macOS/arm64 与可用的 Linux/amd64 环境验证；另外两种架构由 CI 的真实 runner 运行：基础 SQL、持久化 session、流式读取/取消、参数绑定、管理/关闭、并发与 `-race`、Arrow 可选模块的适用组合。不以交叉编译成功替代运行验证。
- 静态路径在没有动态 `libchdb` 的环境运行；以完整运行用例、真实归档和链接依赖检查为验收，不在静态 CI 中安排默认无 cgo 构建或反向构建失败的检查。
- 社区可维护**原生归档的构建、版本 pin、平台链接配方、校验和、兼容矩阵和示例**；核心仓库维护可选 cgo adapter 的 interface 与 ABI 测试。第一版可由用户显式下载带 SHA-256 的归档并指定本地路径。若要做到仅 `go get` 就自动带 `.a`，须另行设计版本化平台模块与归档分片/链接方案，并从 Go 模块代理实际消费验证；Go 构建不会自动运行下载或解压脚本，空白 import 也不能凭空把 purego 调用变成静态调用。不要把大归档加进默认根模块或默认发布产物。

## 阶段 3：用 chdb-go CI 验证运行时兼容性

**可以利用现有 CI 的平台，但需新增专门的静态产物工作流。** `.github/workflows/chdb.yml` 已有 Ubuntu 与 `macos-15` runner，`.github/workflows/arrow.yml` 已有 cgo/Arrow 验证，`.github/scripts/verify-as-user.sh` 有从独立模块消费的检查方法；目前它们下载/使用的主要是动态库，且主工作流对纯 Markdown 改动使用 `paths-ignore`。新增静态工作流应按静态 adapter、归档来源和 CI 脚本的变更触发，不把这份计划文档的提交误称为 CI 已验证。

建议把 CI 划成以下关卡，并保存每次检查的依赖报告：

| 关卡 | 验证内容 | 失败条件 |
| --- | --- | --- |
| 四架构用例 | `CGO_ENABLED=1` 且 `-tags chdb_static`：在四个真实 runner 上分别构建并运行根模块、Arrow 模块和 `-race` 全量用例；不以“只编译成功”代替运行验证 | 任意架构缺少 runner、编译失败或用例失败 |
| 静态构建 | 固定 SHA 的归档与头文件，Go 模块外部消费、`-trimpath`、运行测试 | 缺文件、ABI/架构不匹配、构建机路径泄漏 |
| 依赖审计 | Linux `readelf -d/-V`、`ldd`；macOS `otool -L/-l` 和部署目标；核实没有 `libchdb.so`/`.dylib` 的运行时依赖，列出允许的系统依赖 | 非预期动态依赖、glibc 符号版本高于声明基线、私有或构建机路径引用 |
| 干净运行环境 | **同一份**构建产物（不在测试镜像内重编译）在不安装 `libchdb` 的低版本及较新 Linux glibc 容器中执行 smoke/integration 测试；macOS 在可用的最低版本 runner/真机上运行 | 只能在构建 runner 工作，缺运行库，查询/关闭崩溃 |
| 诊断与压力 | `cgocheck2`、`-race`、流式/并发/信号处理及可选 Arrow；静态模式的引擎版本/依赖报告 | C/Go 内存规则、符号冲突、平台特有退出问题 |

Linux 的策略是**用选定的旧 glibc 基线构建，并把二进制拿到较新 glibc 环境运行**。CI 至少覆盖声明的最低版本和一个较新版本；若要支持更旧发行版，先降低构建基线再增加对应测试。可评估把 libstdc++/libgcc 静态链接以减少其运行时版本要求，但必须审计许可证、体积、C++ 异常/ABI 和其他动态依赖；不能靠某个编译开关推断所有 C++/系统依赖都消失。Alpine/musl 是不同 libc 目标，应使用单独按 musl 构建、测试的归档和产物；不要承诺 glibc 二进制原生支持 musl。

macOS 通常仍依赖系统 `libSystem` 和 Apple 提供的 C++ 运行库/框架；通过明确部署目标、SDK/符号审计和较旧受支持 macOS 的实机/runner 验证来覆盖版本。单独检查归档内 C++ 标准库符号冲突、签名和分发要求。只有 `macos-15` runner 时，CI **不能证明**更旧 macOS 可用；需要增加可用的旧版本 runner 或社区真机验证并在文档中标明缺口。

CI 能证明“在这些具体的 libc/C++ 运行库/系统库组合上可用”，不能证明“任意用户环境通用”。发布时维护按 OS、架构、libc 家族与最低版本、引擎归档版本划分的支持矩阵，并提供 `readelf`/`otool` 的诊断步骤；未测的组合标注未验证，不让 `ldd` 显示少量依赖就替代实际运行。

## 阶段 4：用户指南与发布验收

新增 `docs/static-linking.md`（实施完成后），给出经过 CI 运行的完整例子：下载并校验对应平台归档/头文件、指定精确归档和必要链接选项、执行 `CGO_ENABLED=1 go build -tags chdb_static`、用 `readelf`/`otool` 检查产物、在没有 `libchdb` 的干净环境运行示例。另给出默认 purego/embedded 两种选择，解释静态引擎单文件交付与“整个二进制完全静态”的区别，以及 Linux glibc/musl 和 macOS 的支持矩阵。

发布门槛：默认构建零回归；四架构的真实归档、编译、完整用例、独立消费和依赖审计均通过；Linux 额外验证同一产物在干净 glibc 环境中运行；CI 可复现并留存报告；文档中的命令使用与 CI **同一套配方**。所有版本、平台和依赖结论以测试结果填写，不在实现前写成已支持。
