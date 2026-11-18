//go:build !windows

package bundled

// macOS / Linux 暂不内置二进制：macOS 官方不分发静态 mpv，Linux 依赖发行版
// 动态库，静态捆绑在多数机器上反而跑不起来。这两个平台沿用系统探测后端，
// 行为与内置不可用时的降级链一致。未来要支持时，新增对应平台文件并设置
// source 即可，主逻辑无需改动。
func init() {
	source.ok = false
}
