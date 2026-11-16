// Command pc-cast 是跨平台 DLNA 投屏接收端入口。
package main

import (
	"fmt"
	"os"

	"pc-cast/app"
)

func main() {
	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "pc-cast:", err)
		os.Exit(1)
	}
}
