# 技术架构

本篇说明 PC-Cast 的播放器后端分级、DLNA/UPnP 协议实现细节与代码目录结构，面向需要改动或扩展代码的读者。软件功能概览与快速上手见 [README](../README.md)。

---

## 播放器能力分级

Windows 版随程序内置 mpv（v0.42.0）：压缩数据以 `go:embed` 编入 exe，首次运行解压到 `%AppData%\pc-cast\bundled-mpv\<版本>\`。`auto` 模式把内置播放器置于候选链首位；用户显式指定（`builtin` / `mpv` / `vlc` / `custom`）时按 ID 精确命中。启动失败的后端被排除后重试，最多 6 轮；内置失败则降级到本机其他完整控制后端。**默认链路不含「系统默认程序」兜底**——`CapSystem` 枚举与相关防御分支仅为兼容保留，三个平台的候选清单都不会产生该能力。

| 档位 | 后端 | 播放 | 暂停 / 继续 | 跳转 | 音量 | 进度 / 状态 | HLS 直播 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `CapFull` 完整控制 | **内置 mpv**（Windows）、本机 **mpv**（JSON IPC）、**VLC 3.x**（HTTP Lua） | ✅ | ✅ 真实 | ✅ 真实 | ✅ 真实 | ✅ 真实 | ✅ 持续刷新 |
| `CapLaunch` 基础播放 | PotPlayer、MPC-BE/HC、ffplay、Windows Media Player、IINA、Celluloid、Totem | ✅ | ⚠️ 杀进程 / 重拉 | ⚠️ 多数仅记账 | ⚠️ 仅记账 | ⚠️ 墙钟估算 | ⚠️ 取决于播放器 |
| `CapSystem` 系统默认 | 已从候选清单移除（`rundll32` / `open` / `xdg-open`） | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| `CapNone` | 无可用后端 | ❌ | — | — | — | 设备仍可被发现 | ❌ |

> **Windows 版开箱即用**：内置 mpv 原生支持 HLS，切台走 JSON IPC `loadfile`，不依赖进程重启。若运行时日志显示 `backend=wmplayer`，说明跑的是未内置播放器的旧版 exe：WMP 会把 `.m3u8` 当静态播放列表读，播完开头几个分片即 EOS，切台也可能被单实例机制吞掉。macOS / Linux 未内置二进制，建议安装 mpv 或 VLC 3.x 升级到 `CapFull`；安装后重启程序或点设置页「重新探测」即可。VLC 的 HTTP 接口参数（端口、随机密码）由程序自动注入，无需手工配置。

---

## 托盘图标状态

`trayui/icon.go` 的 `currentStatus()` 是托盘图标、桌面通知与关于窗口状态徽标的唯一状态来源，优先级如下：

| 状态 | 图标角标 | 文案 | 触发条件 |
| --- | --- | --- | --- |
| 播放中 | 绿色 | `投屏中` | 播放器状态为 `StatePlaying`，优先于其余状态 |
| 未播放 | 琥珀色 | `空闲 · 等待投屏` | 默认状态，含完整后端（内置 mpv）待机；弱后端追加`（能力受限 · X）` |
| 投屏已关闭 | 灰阶 | `投屏已关闭` | `cast_enabled=false` |

琥珀角统一表达「未在播放」，原先独立的「待机图」状态已退役。启动弹窗里的播放器行由 `playerNotifyLine()` 生成：`CapFull` 后端只显示名称（如「播放器：内置 mpv」），弱后端才附能力标签作降级提示；设置页下拉与软件信息窗口仍显示能力标签。

---

## 协议实现

| 层 | 实现要点 |
| --- | --- |
| SSDP | UDP 1900 / `239.255.255.250`；响应 `ssdp:all`、`upnp:rootdevice`、版本化 ST（严格回显请求值）；`ssdp:alive` 每 **5 秒**，`max-age=120`；支持 `SO_REUSEADDR` 与系统 SSDP 服务共享端口 |
| HTTP | 默认 8768，固定端口优先；所有 XML 响应统一由 `writeXMLBody` 设置 `Content-Length`，避免 chunked 被部分 Android SDK 拒收；路径前导斜杠归一化 |
| 设备描述 | `description.xml` 含 AVTransport / RenderingControl / ConnectionManager 三服务；`URLBase` 与三类服务 URL 一律使用**实际监听端口**的绝对地址，不信任 `Host` 头 |
| SCPD | `dlna/scpd/*.xml` 按公开 UPnP 服务规范内嵌三份标准服务描述模板，通过 `go:embed` 提供；动态生成仅作保底 |
| SOAP | `SetAVTransportURI` / `Play` / `Pause` / `Stop` / `Seek` / `Next` / `Previous` / 音量与静音 / 各类查询；直播流的位置与时长返回 `00:00:00`、`Seek` 静默转发，避免控制点判定设备异常后主动断开 |
| GENA | `SUBSCRIBE` / `UNSUBSCRIBE` / 续订，`TIMEOUT: Second-1800`（独立于 SSDP max-age），`LastChange` 事件推送 |
| DIDL-Lite | 多 `<res>` 按码率 + 分辨率打分择优；直播三级判定（强证据 → 点播反证 → URI 特征），覆盖 m3u8 / FLV / TS / DASH 与带长签名的点播链接 |
| Sink 声明 | 高清档 + HLS / MPEG-TS / FLV 等 27 条 MIME + `http-get:*:*:*` 兜底；**刻意不声明标清 PN**，否则控制点会主动推 360P 低码率流 |

---

## 项目结构

```
pc-cast/
├── main.go                    # 入口，调用 app.Run()
├── app/                       # 组件装配、生命周期、配置变更编排、启动通知
├── config/                    # 配置持久化、校验、旧字段迁移、并发安全 Store
├── dlna/                      # 自研 DLNA/UPnP 协议栈
│   ├── server.go / ssdp.go    # HTTP 服务、端口策略、SSDP 发现与 alive
│   ├── description.go         # 设备描述与 SCPD 生成
│   ├── actions.go / soap.go   # SOAP 动作分发与响应
│   ├── events.go              # GENA 订阅与 LastChange
│   ├── didl.go                # DIDL-Lite 解析与生成
│   └── scpd/                  # 内嵌标准服务描述模板（go:embed）
├── renderer/                  # Player 接口与播放后端
│   ├── detect.go / factory.go # 候选清单、内置置顶、能力探测、选择与降级
│   ├── mpv.go / vlc.go        # CapFull：JSON IPC / HTTP Lua；内置 mpv 复用 mpv.go
│   ├── external.go            # CapLaunch 与进度估算（CapSystem 仅兼容保留）
│   └── ipc_*.go / exec_*.go   # 平台相关：命名管道 / domain socket、无窗口进程
├── internal/bundled/          # 内置 mpv：go:embed 压缩数据、解压与缓存路径
│   ├── bundled.go / data_*.go # 按平台提供数据；仅 Windows 有真实二进制
│   └── data/mpv-*.exe.gz      # gzip 压缩的 mpv 可执行文件（随源码提交）
├── trayui/                    # 托盘菜单、设置窗口、软件信息、图标取图策略
├── assets/                    # 图标与状态图（go:embed）、SVG 母版
├── logging/                   # slog JSON Handler
├── icon_windows_*.syso        # Windows exe 图标资源（生成物，已提交）
├── .workbuddy/tools/          # 图标 / .syso / PE 校验生成器（//go:build ignore）
└── Makefile
```

`dlna` 层只依赖 `renderer.Player` 接口，新增播放后端不需要改动协议层；`trayui` 通过 `Deps` 注入 getter，避免与 `app` 形成包循环。

---

相关文档：[构建与打包](build.md) · [配置与日志](configuration.md) · [故障排查](troubleshooting.md)
