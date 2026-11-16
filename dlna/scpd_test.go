package dlna

import (
	"encoding/xml"
	"strings"
	"testing"
)

type scpdAction struct {
	Name string `xml:"name"`
	Args []struct {
		Name      string `xml:"name"`
		Direction string `xml:"direction"`
		Related   string `xml:"relatedStateVariable"`
	} `xml:"argumentList>argument"`
}

type scpdDoc struct {
	Actions []scpdAction `xml:"actionList>action"`
	Vars    []struct {
		Name string `xml:"name"`
	} `xml:"serviceStateTable>stateVariable"`
}

// UPnP 规范要求每个 <argument> 的 <relatedStateVariable> 都指向
// <serviceStateTable> 里真实声明的变量。悬空引用会让 Cling / CyberGarage
// 等严格 DLNA SDK 判定服务描述无效，进而在拉完 SCPD 后直接放弃投屏。
func TestSCPDHasNoDanglingStateVariables(t *testing.T) {
	for _, svc := range serviceList() {
		t.Run(svc.URN, func(t *testing.T) {
			body := buildSCPD(svc.URN)
			if body == "" {
				t.Fatal("SCPD 为空")
			}
			var doc scpdDoc
			if err := xml.Unmarshal([]byte(body), &doc); err != nil {
				t.Fatalf("SCPD 不是合法 XML: %v", err)
			}
			declared := make(map[string]bool)
			for _, v := range doc.Vars {
				declared[v.Name] = true
			}
			if len(declared) == 0 {
				t.Fatal("serviceStateTable 为空")
			}
			for _, a := range doc.Actions {
				for _, arg := range a.Args {
					if arg.Direction != "in" && arg.Direction != "out" {
						t.Errorf("%s/%s: direction=%q 非法", a.Name, arg.Name, arg.Direction)
					}
					if arg.Related == "" {
						t.Errorf("%s/%s: 缺少 relatedStateVariable", a.Name, arg.Name)
						continue
					}
					if !declared[arg.Related] {
						t.Errorf("%s/%s: relatedStateVariable=%q 未在 serviceStateTable 声明",
							a.Name, arg.Name, arg.Related)
					}
				}
			}
		})
	}
}

// TestDescriptionContainsCompatibilityFields 防止补齐的兼容字段被回退。
func TestDescriptionContainsCompatibilityFields(t *testing.T) {
	const base = "http://127.0.0.1:8768/"
	body := buildDescription(descriptionData{
		URLBase: base, FriendlyName: "测试", UDN: "uuid:test",
		Services: serviceList(),
	})
	for _, want := range []string{"<manufacturerURL>", "<modelURL>", "<dlna:X_DLNADOC>", "<serviceList>", "<URLBase>"} {
		if !strings.Contains(body, want) {
			t.Errorf("设备描述缺少 %q", want)
		}
	}
	// 这两个可选 URL 只指向接收端自身，不下发任何第三方项目地址。
	for _, tag := range []string{"manufacturerURL", "modelURL"} {
		if !strings.Contains(body, "<"+tag+">"+base+"</"+tag+">") {
			t.Errorf("%s 应指向接收端自身地址: %s", tag, body)
		}
	}
	if strings.Contains(body, "github.com") {
		t.Error("设备描述不应包含外部项目地址")
	}
}

// TestSCPDUsesOfficialTemplates 确保内嵌的 UPnP 官方模板真正生效，而不是
// 悄悄回退到动态生成；同时确认两个新增的 DB 音量动作已声明。
func TestSCPDUsesOfficialTemplates(t *testing.T) {
	for _, svc := range serviceList() {
		official := officialSCPD(svc.URN)
		if official == "" {
			t.Errorf("%s 未加载官方模板", svc.URN)
			continue
		}
		if got := buildSCPD(svc.URN); got != official {
			t.Errorf("%s 未使用官方模板", svc.URN)
		}
	}
	if !strings.Contains(buildSCPD(ServiceRenderingControl), "GetVolumeDBRange") {
		t.Error("RenderingControl 官方模板缺少 GetVolumeDBRange")
	}
}

func TestSCPDContainsRequiredActions(t *testing.T) {
	want := map[string][]string{
		ServiceAVTransport:       {"SetAVTransportURI", "Play", "Pause", "Stop", "Seek", "GetPositionInfo", "GetTransportInfo", "GetMediaInfo"},
		ServiceRenderingControl:  {"GetVolume", "SetVolume", "GetMute", "SetMute"},
		ServiceConnectionManager: {"GetProtocolInfo", "GetCurrentConnectionIDs"},
	}
	for urn, names := range want {
		body := buildSCPD(urn)
		var doc scpdDoc
		if err := xml.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatalf("%s SCPD 解析失败: %v", urn, err)
		}
		got := make(map[string]bool)
		for _, a := range doc.Actions {
			got[a.Name] = true
		}
		for _, n := range names {
			if !got[n] {
				t.Errorf("%s 缺少必需 action %q", urn, n)
			}
		}
	}
}
