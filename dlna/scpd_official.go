package dlna

import "embed"

// officialSCPDFiles 内嵌 UPnP 官方服务描述模板（AVTransport /
// RenderingControl / ConnectionManager）。
//
// 之前 buildSCPD 用精简的 Go 结构动态拼装，虽然补齐了悬空引用并改成
// Content-Length 传输，仍与严格 DLNA SDK 的校验存在大量细微差异
// （变量表规模、allowedValueRange、XML 格式、action 集合）。直接内嵌
// 官方静态原文可一次性消除内容层的全部差异。
//
//go:embed scpd/*.xml
var officialSCPDFiles embed.FS

var officialSCPDName = map[string]string{
	ServiceAVTransport:       "AVTransport.xml",
	ServiceRenderingControl:  "RenderingControl.xml",
	ServiceConnectionManager: "ConnectionManager.xml",
}

// officialSCPD 返回官方模板原文；找不到时返回空串，由 buildSCPD 回退到
// 动态生成，保证嵌入式/裁剪构建不会因缺文件而返回空 SCPD。
func officialSCPD(urn string) string {
	name := officialSCPDName[urn]
	if name == "" {
		return ""
	}
	b, err := officialSCPDFiles.ReadFile("scpd/" + name)
	if err != nil {
		return ""
	}
	return string(b)
}
