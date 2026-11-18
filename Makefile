BINARY := pc-cast
CMD := .
VERSION ?= 0.6.0
# 构建标识：默认恒定显示专属名称；需要 git 短哈希时用 `make build COMMIT=xxx` 覆盖
COMMIT ?= Shinveam
BUILDTIME := $(shell date +%Y-%m-%dT%H:%M:%S)
PKG := pc-cast/app
# -H=windowsgui：双击 exe 不再弹出控制台窗口（日志仍写入 %AppData%/pc-cast/pc-cast.log）
LDFLAGS := -s -w -H=windowsgui -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).BuildTime=$(BUILDTIME)

.PHONY: build run test vet fmt tidy clean cross smoke icons verify-icon

# 改动 assets/*.svg 或 .workbuddy/tools/gen_*.go 后执行：重新栅格化 PNG/ICO，
# 并重建 Windows exe 图标资源 icon_windows_{amd64,arm64}.syso。
# .syso 已提交到仓库，日常 go build / make build 不需要先跑它。
icons:
	go run .workbuddy/tools/gen_icons.go
	go run .workbuddy/tools/gen_syso.go

# 校验已构建的 Windows exe 确实带上了六帧图标资源
verify-icon:
	go run .workbuddy/tools/verify_icon.go bin/$(BINARY).exe

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY).exe $(CMD)

run:
	go run $(CMD)

test:
	go test ./... -count=1

vet:
	go vet ./...

# 退出链路冒烟：真实 mygo 事件循环 + 设置窗口存在时触发托盘退出
smoke:
	PC_CAST_QUIT_SMOKE=1 go test ./trayui -run '^$$' -count=1 -v

fmt:
	go fmt ./...

tidy:
	go mod tidy

cross:
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H=windowsgui" -o dist/$(BINARY)-windows-amd64.exe $(CMD)
	GOOS=darwin  GOARCH=arm64  go build -trimpath -o dist/$(BINARY)-darwin-arm64 $(CMD)
	GOOS=linux   GOARCH=amd64  go build -trimpath -o dist/$(BINARY)-linux-amd64 $(CMD)

clean:
	rm -rf bin dist
