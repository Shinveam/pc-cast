# 构建与打包

本篇汇总 PC-Cast 的构建命令、Windows 打包含图标流程与开发期命令。环境要求与首次运行见 [README](../README.md)，内部机制见 [技术架构](architecture.md)。

---

## 构建命令

```bash
# 标准方式（产出 bin/pc-cast.exe）
make build

# 无 make 环境的等价命令（Windows GUI 版）
go build -trimpath -ldflags "-s -w -H=windowsgui -X pc-cast/app.Version=0.5.0 -X pc-cast/app.Commit=Shinveam -X pc-cast/app.BuildTime=$(date +%Y-%m-%dT%H:%M:%S)" -o bin/pc-cast.exe .

# Linux / macOS（不要带 -H=windowsgui）
go build -trimpath -o bin/pc-cast .
```

`make build` 会自动带齐三项注入；`Commit` 是「软件信息 → 构建」显示的专属标识，默认值由 `app/info.go` 提供，需要改成 git 短哈希时用 `make build COMMIT=xxx` 覆盖。

`make cross` 会交叉编译出 Windows amd64、macOS arm64、Linux amd64 三个产物到 `dist/`。

---

## Windows 打包（含图标）

Windows 可执行文件编译为 GUI 子系统（`make build` 已带 `-H=windowsgui`），双击不弹控制台。exe 的图标由项目根目录的 `icon_windows_amd64.syso` / `icon_windows_arm64.syso` 提供，Go 链接器在 `go build` 时按目标架构自动选取并写入 PE 资源，**日常构建无需任何额外参数**：

| 目的 | 命令 | 说明 |
| --- | --- | --- |
| 常规构建 | `make build` | 产出 `bin/pc-cast.exe`，自动带六帧 exe 图标 |
| 单独交叉编译 Windows | `GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H=windowsgui" -o dist/pc-cast-windows-amd64.exe .` | 即 `make cross` 的 Windows 分支；arm64 把 `GOARCH` 换成 `arm64` |
| 更新图标 | `make icons` | 改动 `assets/*.svg` 后重新栅格化 PNG/ICO 并重建 `.syso` |
| 校验图标 | `make verify-icon` | 检查 `bin/pc-cast.exe` 中的 6 帧图标资源是否完整 |

`.syso` 是已提交的生成物，必须放在 main 包所在目录（即项目根目录），Go 才会自动链接；只要不改 SVG 母版，无需重新生成。窗口、托盘与通知图标则通过 `go:embed` 内嵌自 `assets/`，与 exe 资源图标是两套独立来源。

---

## 开发期命令

| 命令 | 作用 |
| --- | --- |
| `make build` | 构建带版本注入的 Windows GUI 可执行文件 |
| `make run` / `go run .` | 直接运行（会带控制台） |
| `make test` / `go test ./... -count=1` | 全量单元测试 |
| `make vet` / `go vet ./...` | 静态检查 |
| `make fmt` / `make tidy` | 格式化 / 依赖整理 |
| `make cross` | 三平台交叉编译 |
| `make smoke` | 托盘退出链路冒烟（真实 mygo 事件循环，`PC_CAST_QUIT_SMOKE=1`） |
| `make icons` | 重新栅格化 PNG/ICO 并重建 `.syso`（改动 SVG 后执行） |
| `make verify-icon` | 校验构建产物中 exe 图标的 6 帧资源 |
| `make clean` | 清理 `bin/` 与 `dist/` |

无 `make` 的环境可直接执行等价命令，例如：

```bash
go test ./... -count=1
go vet ./...
PC_CAST_QUIT_SMOKE=1 go test ./trayui -run '^$' -count=1 -v
go run .workbuddy/tools/gen_icons.go
go run .workbuddy/tools/gen_syso.go
```

`go test -race` 需要 `CGO_ENABLED=1` 且本机装有 gcc；纯 Go 构建与运行本身不需要 cgo。

---

相关文档：[技术架构](architecture.md) · [配置与日志](configuration.md) · [故障排查](troubleshooting.md)
