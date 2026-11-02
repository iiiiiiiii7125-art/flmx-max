package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// idleAfterUninstall 让已被面板卸载的容器形态 agent 空转等待人工
// 删除容器：配置已随卸载清除，此时既不能以「配置缺失」崩溃重启
// （重启策略会把它反复拉起），也不能重连面板（服务器记录已删）。
func idleAfterUninstall() {
	fmt.Println("ℹ️ 面板已删除这台服务器并卸载了 agent，不再连接面板。请在宿主机上执行 docker rm -f 删除这个容器")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	os.Exit(0)
}
