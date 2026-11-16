package app

import (
	"strings"
	"testing"
)

// 构建标识是专属署名，防止被改回通用占位值。
func TestBuildSignature(t *testing.T) {
	if got := BuildInfo().Commit; got != "Shinveam" {
		t.Fatalf("Commit = %q, want Shinveam", got)
	}
	var build string
	for _, kv := range BuildInfo().Lines() {
		if kv[0] == "构建" {
			build = kv[1]
		}
	}
	if !strings.HasPrefix(build, "Shinveam @ ") {
		t.Fatalf("构建行 = %q, 应以 Shinveam 开头", build)
	}
	if !strings.Contains(BuildInfo().Summary(), "Shinveam") {
		t.Fatal("Summary 未包含构建标识")
	}
}
