package rdp

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"remote-connect-manager/internal/config"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// guacamole-common-js 建连时声明子协议 "guacamole"，
// 服务端必须回显该子协议，否则浏览器会以 1006 立即断开
var guacUpgrader = websocket.Upgrader{
	CheckOrigin:  func(r *http.Request) bool { return true },
	Subprotocols: []string{"guacamole"},
}

// guacd 地址，默认本机；容器编排时通过 GUACD_ADDR 指向 guacd 服务
func guacdAddr() string {
	if v := os.Getenv("GUACD_ADDR"); v != "" {
		return v
	}
	return "127.0.0.1:4822"
}

func HandleRDP(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "use /ws/guacamole/:id"})
}

// HandleGuacamoleTunnel 把浏览器的 WebSocket 与 guacd 的 TCP 打通，
// 由服务端完成 Guacamole 握手（select → args → size/image/audio → connect → ready），
// 之后双向透传绘制/事件指令。
func HandleGuacamoleTunnel(c *gin.Context) {
	id := c.Param("id")
	conn, err := config.GetConnectionByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "connection not found"})
		return
	}

	ws, err := guacUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	guacConn, err := net.DialTimeout("tcp", guacdAddr(), 10*time.Second)
	if err != nil {
		ws.WriteMessage(websocket.TextMessage,
			[]byte(fmt.Sprintf("5.error,9.%s;", escapeGuac("guacd 连接失败: "+err.Error()))))
		return
	}
	defer guacConn.Close()

	proto := conn.Type
	if proto == "" {
		proto = "rdp"
	}

	// 1) select：告诉 guacd 使用哪个协议
	if _, err := guacConn.Write([]byte(guacEncode([]string{"select", proto}))); err != nil {
		return
	}

	// 2) 读取 args（形如 4.args,13.VERSION_1_5_0,8.hostname,4.port,...;）
	argsLine, err := readInstruction(guacConn)
	if err != nil {
		return
	}
	parts := decodeInstruction(argsLine)
	if len(parts) < 2 || parts[0] != "args" {
		ws.WriteMessage(websocket.TextMessage, []byte(guacEncode([]string{"error", "guacd 握手异常: " + argsLine})))
		return
	}
	version := parts[1]
	names := parts[2:]

	// 3) 客户端能力与显示尺寸
	var sb strings.Builder
	sb.WriteString(guacEncode([]string{"size", strconv.Itoa(conn.Width), strconv.Itoa(conn.Height), "96"}))
	// 优先声明有损格式：guacd 会按顺序选第一个支持的编码，
	// PNG 放前面会导致大块画面走无损编码，带宽高、易卡顿
	sb.WriteString(guacEncode([]string{"image", "image/webp", "image/jpeg", "image/png"}))
	sb.WriteString(guacEncode([]string{"video"}))
	// 声明支持的音频编码，RDP/VNC 启用了声音后 guacd 会推 audio 流
	sb.WriteString(guacEncode([]string{"audio", "audio/L16", "audio/L8"}))
	sb.WriteString(guacEncode([]string{"timezone", "Asia/Shanghai"}))

	// 4) connect：值顺序必须与 args 中参数顺序一致
	connectArgs := []string{"connect", version}
	for _, name := range names {
		connectArgs = append(connectArgs, argValue(name, conn))
	}
	sb.WriteString(guacEncode(connectArgs))
	if _, err := guacConn.Write([]byte(sb.String())); err != nil {
		return
	}

	// 5) 双向透传（浏览器端 Guacamole.Client 不在连接时发送数据，握手由服务端完成）
	// guacamole-common-js 的 WebSocketTunnel 要求：
	//   a) 先收到内部指令 0.<uuid>; 才会把隧道置为 OPEN，之后客户端才能回 sync/nop
	//   b) 每条 WebSocket 消息必须是完整指令（以 ; 结尾），否则会按 "Incomplete instruction" 断开
	if err := ws.WriteMessage(websocket.TextMessage,
		[]byte(guacEncode([]string{"0", tunnelUUID()}))); err != nil {
		return
	}

	done := make(chan struct{}, 2)

	go func() {
		buf := make([]byte, 65536)
		pending := make([]byte, 0, 65536)
		for {
			n, err := guacConn.Read(buf)
			if n > 0 {
				pending = append(pending, buf[:n]...)
				if idx := bytes.LastIndexByte(pending, ';'); idx >= 0 {
					if werr := ws.WriteMessage(websocket.TextMessage, pending[:idx+1]); werr != nil {
						log.Printf("rdp tunnel: ws write failed: %v", werr)
					}
					pending = append(pending[:0], pending[idx+1:]...)
				}
			}
			if err != nil {
				log.Printf("rdp tunnel: guacd read ended: %v", err)
				done <- struct{}{}
				return
			}
		}
	}()

	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				log.Printf("rdp tunnel: ws read ended: %v", err)
				done <- struct{}{}
				return
			}
			// 过滤隧道内部保活指令（0.ping 等），guacd 不认识该 opcode
			payload := filterInternalInstructions(string(data))
			if payload == "" {
				continue
			}
			if _, werr := guacConn.Write([]byte(payload)); werr != nil {
				log.Printf("rdp tunnel: guacd write failed: %v", werr)
			}
		}
	}()

	<-done
	_ = io.EOF
}

// guacEncode 按 Guacamole 协议编码一条指令：opcode,<len>.<value>,...;
func guacEncode(parts []string) string {
	var sb strings.Builder
	for i, p := range parts {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.Itoa(len(p)))
		sb.WriteByte('.')
		sb.WriteString(p)
	}
	sb.WriteByte(';')
	return sb.String()
}

// tunnelUUID 生成隧道标识，用于 0.<uuid>; 内部指令（仅前端隧道状态机使用）
func tunnelUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// filterInternalInstructions 去掉 opcode 为 "0" 的隧道内部指令（如 0.ping），
// 其余完整指令原样保留。
func filterInternalInstructions(s string) string {
	if s == "" {
		return ""
	}
	var sb strings.Builder
	for _, ins := range strings.Split(s, ";") {
		if ins == "" || opcodeOf(ins) == "0" {
			continue
		}
		sb.WriteString(ins)
		sb.WriteByte(';')
	}
	return sb.String()
}

// opcodeOf 从 "<len>.<opcode>,..." 中解析出 opcode
func opcodeOf(ins string) string {
	dot := strings.Index(ins, ".")
	if dot <= 0 {
		return ""
	}
	n, err := strconv.Atoi(ins[:dot])
	if err != nil || dot+1+n > len(ins) {
		return ""
	}
	return ins[dot+1 : dot+1+n]
}

// decodeInstruction 去掉长度前缀并拆分为元素
func decodeInstruction(line string) []string {
	line = strings.TrimSuffix(line, ";")
	out := []string{}
	for _, el := range strings.Split(line, ",") {
		if idx := strings.Index(el, "."); idx >= 0 {
			out = append(out, el[idx+1:])
		} else {
			out = append(out, el)
		}
	}
	return out
}

// readInstruction 读一条完整指令（以 ; 结尾）
func readInstruction(r io.Reader) (string, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[0])
			if tmp[0] == ';' {
				break
			}
		}
		if err != nil {
			return string(buf), err
		}
	}
	return string(buf), nil
}

// argValue 为 guacd 请求的参数提供取值
func argValue(name string, conn *config.Connection) string {
	switch name {
	case "hostname", "host":
		return conn.Host
	case "port":
		if conn.Port > 0 {
			return strconv.Itoa(conn.Port)
		}
		switch conn.Type {
		case "vnc":
			return "5900"
		case "telnet":
			return "23"
		}
		return "3389"
	case "username":
		return conn.Username
	case "password":
		return conn.Password
	case "domain":
		return ""
	case "width":
		if conn.Width > 0 {
			return strconv.Itoa(conn.Width)
		}
		return "1280"
	case "height":
		if conn.Height > 0 {
			return strconv.Itoa(conn.Height)
		}
		return "720"
	case "dpi":
		return "96"
	case "color-depth":
		if conn.ColorDepth > 0 {
			return strconv.Itoa(conn.ColorDepth)
		}
		return "24"
	case "security":
		if conn.Security != "" {
			return conn.Security
		}
		return "any"
	case "force-lossless":
		return "false"
	case "ignore-cert":
		return "true"
	case "disable-auth":
		return "false"
	case "enable-drive":
		// RDP 共享盘：Windows 映射为 \\tsclient\rcm，xrdp 挂到 ~/thinclient_drives/rcm
		return "true"
	case "drive-name":
		return "rcm"
	case "drive-path":
		// guacd 侧的共享盘落地目录，必须存在且可写，否则远端文件操作会被
		// "File open refused (-2)" 拒绝，浏览器也拿不到文件内容
		if v := os.Getenv("RCM_DRIVE_PATH"); v != "" {
			return v
		}
		return "/tmp/rcm-drive"
	case "create-drive-path":
		return "true"
	case "disable-audio", "disable-audio-input":
		return "false"
	case "enable-sftp", "console", "read-only":
		return "false"
	case "disable-copy", "disable-paste", "disable-upload", "disable-download":
		return "false"
	case "normalize-clipboard":
		// guacd 1.5 取值为 preserve/unix/windows，true 会被忽略
		return "preserve"
	case "clipboard-encoding":
		return "UTF-8"
	case "font-size":
		return "14"
	case "font-name":
		return "monospace"
	case "color-scheme":
		return "gray-black"
	case "terminal-type":
		return "xterm-256color"
	case "backspace":
		return "127"
	case "scrollback":
		return "1000"
	case "resize-method":
		return "display-update"
	case "server-layout":
		return "en-us-qwerty"
	case "cursor":
		return "remote"
	case "swap-red-blue":
		return "false"
	default:
		return ""
	}
}

func escapeGuac(s string) string {
	s = strings.ReplaceAll(s, ",", "&#44;")
	s = strings.ReplaceAll(s, ";", "&#59;")
	return s
}
