# 配置与日志

本篇说明 PC-Cast 的配置文件路径、字段含义与日志格式，适合需要手工调参或按日志排障的读者。基本使用见 [README](../README.md)，按症状定位见 [故障排查](troubleshooting.md)。

---

## 配置文件路径

| 平台 | 路径 |
| --- | --- |
| Windows | `%AppData%\pc-cast\config.json`，日志同目录 `pc-cast.log` |
| macOS | `~/Library/Application Support/pc-cast/` |
| Linux | `$XDG_CONFIG_HOME/pc-cast/`（默认 `~/.config/pc-cast/`） |

---

## 配置字段

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `friendly_name` | `PC-Cast (<主机名>)` | 手机上显示的设备名 |
| `port` | `0` | `0` = 优先 8768，占用则 +1 探测（最多 12 次），全失败才随机；显式端口被占用会直接报错 |
| `bind_ip` | `""` | 空 = 全部网卡；可指定单张网卡 IPv4 |
| `player_mode` | `auto` | `auto` / 后端 ID（`mpv`、`vlc`、`wmplayer`…）/ `custom` |
| `player_path` | `""` | 仅 `custom` 模式生效的可执行文件绝对路径 |
| `fullscreen` | `false` | 命令行全屏开关（部分后端不支持） |
| `auto_play` | `true` | 收到 URI 后是否自动播放 |
| `cast_enabled` | `true` | 投屏总开关 |
| `window_size` | `1080p` | `720p` / `1080p` / `1440p` / `4k` / `auto`；只影响窗口，不决定片源清晰度 |
| `open_at_login` | `false` | 开机自启 |
| `log_level` | `info` | `debug` / `info` / `warn` / `error`；**改动需重启应用** |
| `device_uuid` | 随机生成 | 持久化，保证设备身份跨重启稳定 |

配置由设置页原子写入，也支持手工编辑（保存后需重启或触发一次配置变更）。旧版 `mpv_path` 字段在加载时自动迁移。

---

## 日志

单行 JSON，顶级字段固定为 `time`（`2006-01-02 15:04:05.000`）、`level`、`service`（`pc-cast`）、`tag`、`msg`，附加字段统一放在 `field`：

```json
{"time":"2026-11-15 20:08:58.157","level":"INFO","service":"pc-cast","tag":"dlna","msg":"收到投屏请求","field":{"live":true,"remote":"192.168.1.50:54321"}}
```

日志**追加**写入（不清空），超过 2 MiB 轮转为 `pc-cast.log.1`。排查投屏问题时按 `响应 M-SEARCH → 设备描述被访问 → SCPD → SOAP → 已拉起播放器` 逐层定位，能快速区分「请求没到」「能力协商放弃」「播放器解不了流」。

---

相关文档：[技术架构](architecture.md) · [构建与打包](build.md) · [故障排查](troubleshooting.md)
