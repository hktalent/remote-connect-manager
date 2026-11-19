package main

import (
	"embed"
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"remote-connect-manager/internal/config"
	"remote-connect-manager/internal/keys"
	"remote-connect-manager/internal/netcat"
	"remote-connect-manager/internal/rdp"
	"remote-connect-manager/internal/rdpdrive"
	"remote-connect-manager/internal/screenshot"
	sftpx "remote-connect-manager/internal/sftp"
	"remote-connect-manager/internal/ssh"
	"remote-connect-manager/internal/tunnel"
	"remote-connect-manager/internal/vnc"
)

//go:embed web/*
var webFS embed.FS

//go:embed README.md
var readmeMD []byte

// version 由发布工作流通过 -ldflags "-X main.version=..." 注入
var version = "dev"

func main() {
	log.Printf("remote-connect-manager %s", version)
	if err := config.InitDB("./data/connections.db"); err != nil {
		log.Fatalf("init db: %v", err)
	}
	if err := screenshot.Init("./data/screenshots"); err != nil {
		log.Fatalf("init screenshot dir: %v", err)
	}
	if err := rdpdrive.Ensure(); err != nil {
		log.Printf("init rdp drive dir: %v", err)
	}
	keys.Init("./data/keys")
	// Web Netcat：监听端口配置持久化在 data/netcat.json，payload 在 data/netcat-payloads
	if err := netcat.Init("./data/netcat.json"); err != nil {
		log.Fatalf("init netcat: %v", err)
	}
	// 启动时自动把 ~/.ssh 下的私钥导入密钥库，供界面选择
	if imported, skipped, dir := keys.ImportFromSSH(); len(imported) > 0 || len(skipped) > 0 {
		log.Printf("SSH 密钥扫描 %s: 导入 %d 个 %v, 跳过 %d 个 %v",
			dir, len(imported), imported, len(skipped), skipped)
	}

	r := gin.Default()

	// API
	api := r.Group("/api")
	{
		api.GET("/connections", config.ListConnections)
		api.GET("/connections/:id", config.GetConnection)
		api.POST("/connections", config.CreateConnection)
		api.PUT("/connections/:id", config.UpdateConnection)
		api.DELETE("/connections/:id", config.DeleteConnection)

		api.GET("/connections/:id/screenshot", config.GetScreenshotURL)
		api.POST("/connections/:id/screenshot", config.UploadScreenshot)

		api.GET("/keys", keys.ListKeys)
		api.POST("/keys/generate", keys.GenerateKey)
		api.DELETE("/keys/:name", keys.DeleteKey)
		api.PUT("/keys/:name", keys.UpdateKey)
		api.GET("/keys/:name", keys.GetKey)
		api.GET("/keys/:name/public", keys.GetPublicKey)
		api.POST("/keys/import", keys.ImportKey)
		api.POST("/keys/scan", keys.ScanKeys)

		// 项目 README，供「关于」弹窗渲染
		api.GET("/readme", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"markdown": string(readmeMD)})
		})

		// SFTP 文件管理
		api.GET("/sftp/:id/list", sftpx.List)
		api.GET("/sftp/:id/download", sftpx.Download)
		api.POST("/sftp/:id/upload", sftpx.Upload)
		api.POST("/sftp/:id/mkdir", sftpx.Mkdir)
		api.POST("/sftp/:id/remove", sftpx.Remove)
		// SSH 会话当前目录（供 SFTP 面板打开时对齐）
		api.GET("/ssh/:id/cwd", ssh.Cwd)

		// SSH 本地端口转发
		api.GET("/tunnels", tunnel.List)
		api.POST("/tunnels", tunnel.Create)
		api.DELETE("/tunnels/:id", tunnel.Delete)

		// RDP 共享盘（guacd drive-path 落地目录）：列出与下载远端放入的文件
		api.GET("/rdp-drive/list", rdpdrive.List)
		api.GET("/rdp-drive/download", rdpdrive.Download)
	}

	// Web Netcat（监听端口 / 反弹 shell / 特征应答）
	netcat.Register(api, r)

	// WebSocket endpoints
	r.GET("/ws/ssh/:id", ssh.HandleSSH)
	r.GET("/ws/vnc/:id", vnc.HandleVNC)
	r.GET("/ws/rdp/:id", rdp.HandleRDP)
	r.GET("/ws/telnet/:id", rdp.HandleGuacamoleTunnel)
	r.GET("/ws/guacamole/:id", rdp.HandleGuacamoleTunnel)

	// Screenshot files
	r.Static("/screenshots", "./data/screenshots")

	// Frontend
	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		data, err := webFS.ReadFile("web" + path)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		ct := "text/html; charset=utf-8"
		switch {
		case len(path) > 4 && path[len(path)-3:] == ".js":
			ct = "application/javascript"
		case len(path) > 5 && path[len(path)-4:] == ".css":
			ct = "text/css"
		case len(path) > 5 && path[len(path)-4:] == ".png":
			ct = "image/png"
		case len(path) > 5 && path[len(path)-4:] == ".svg":
			ct = "image/svg+xml"
		}
		// 前端资源随版本频繁变化，禁用缓存，避免浏览器一直用旧的 app.js
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
		c.Data(http.StatusOK, ct, data)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Server starting on :%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}
