package main

// 桌面模式（--desktop）：把服务只绑到 127.0.0.1 的随机端口，
// 然后用系统自带的浏览器内核以「应用窗口」形式打开界面（Chrome/Edge 的 --app 模式），
// 没有可用浏览器时退回默认浏览器。全程零外部依赖、无需 CGO，三个平台用同一份代码。

import (
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
)

// desktopDataDir 桌面模式下把数据放到用户配置目录，避免 .app / 安装目录不可写
func desktopDataDir() string {
	if v := os.Getenv("RCM_DATA_DIR"); v != "" {
		return v
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "remote-connect-manager")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".remote-connect-manager")
}

// listenLoopback 在 127.0.0.1 上挑一个空闲端口，桌面模式不对外暴露
func listenLoopback(port string) (net.Listener, error) {
	if port == "" || port == "0" {
		return net.Listen("tcp", "127.0.0.1:0")
	}
	return net.Listen("tcp", "127.0.0.1:"+port)
}

func run(cmd string, args ...string) bool {
	c := exec.Command(cmd, args...)
	c.Stdout = nil
	c.Stderr = nil
	if err := c.Start(); err != nil {
		return false
	}
	return true
}

func which(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// openAppWindow 打开一个尽量像本地应用的窗口（Chrome/Edge --app），失败退回默认浏览器
func openAppWindow(url string) {
	switch goruntime.GOOS {
	case "darwin":
		for _, app := range []string{"Google Chrome", "Microsoft Edge", "Chromium", "Brave Browser"} {
			if run("open", "-na", app, "--args", "--app="+url) {
				log.Printf("桌面窗口: %s --app", app)
				return
			}
		}
		if run("open", url) {
			log.Printf("桌面窗口: 默认浏览器")
			return
		}
	case "windows":
		for _, exe := range []string{"msedge.exe", "chrome.exe"} {
			if which(exe) && run(exe, "--app="+url) {
				log.Printf("桌面窗口: %s --app", exe)
				return
			}
		}
		run("rundll32", "url.dll,FileProtocolHandler", url)
		log.Printf("桌面窗口: 默认浏览器")
		return
	default: // linux
		for _, exe := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"} {
			if which(exe) && run(exe, "--app="+url) {
				log.Printf("桌面窗口: %s --app", exe)
				return
			}
		}
		if which("xdg-open") && run("xdg-open", url) {
			log.Printf("桌面窗口: 默认浏览器")
			return
		}
	}
	log.Printf("无法自动打开界面，请手动访问 %s", url)
}

// setupDesktopLog 桌面版（Windows -H windowsgui）没有控制台，把日志落到数据目录
func setupDesktopLog(dir string) {
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "desktop.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(f)
}
