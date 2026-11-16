package rdp

import (
	"fmt"
	"io"
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

	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
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
	sb.WriteString(guacEncode([]string{"image", "image/png", "image/jpeg"}))
	sb.WriteString(guacEncode([]string{"video"}))
	sb.WriteString(guacEncode([]string{"audio"}))
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
	done := make(chan struct{}, 2)

	go func() {
		buf := make([]byte, 65536)
		for {
			n, err := guacConn.Read(buf)
			if n > 0 {
				ws.WriteMessage(websocket.TextMessage, buf[:n])
			}
			if err != nil {
				done <- struct{}{}
				return
			}
		}
	}()

	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				done <- struct{}{}
				return
			}
			guacConn.Write(data)
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
		if conn.Type == "vnc" {
			return "5900"
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
	case "ignore-cert":
		return "true"
	case "disable-auth":
		return "false"
	case "enable-drive", "enable-sftp", "disable-copy", "disable-paste", "console", "read-only":
		return "false"
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
