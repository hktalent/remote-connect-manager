package netcat

import (
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// Register 挂载 Web Netcat 相关路由
func Register(api *gin.RouterGroup, r *gin.Engine) {
	g := api.Group("/netcat")
	{
		g.GET("/listeners", ListListeners)
		g.POST("/listeners", CreateListenerAPI)
		g.PUT("/listeners/:id", UpdateListenerAPI)
		g.DELETE("/listeners/:id", DeleteListenerAPI)
		g.POST("/listeners/:id/start", StartListenerAPI)
		g.POST("/listeners/:id/stop", StopListenerAPI)
		g.GET("/listeners/:id/sessions", ListSessionsAPI)
		g.GET("/listeners/:id/events", EventsAPI)

		g.GET("/sessions", ListSessionsAPI)
		g.DELETE("/sessions/:id", DeleteSessionAPI)
		g.POST("/sessions/:id/exec", ExecSessionAPI)

		g.GET("/payloads", ListPayloadsAPI)
		g.POST("/payloads", UploadPayloadAPI)
		g.DELETE("/payloads/:name", DeletePayloadAPI)
	}
	// 浏览器终端：/ws/netcat/<session_id>
	r.GET("/ws/netcat/:id", HandleWS)
}

// ListListeners 返回全部监听（含运行状态）
func ListListeners(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"listeners": ListViews(), "payload_dir": payloadDir})
}

// CreateListenerAPI 新建监听
func CreateListenerAPI(c *gin.Context) {
	var l Listener
	if err := c.ShouldBindJSON(&l); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	out, err := CreateListener(l)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"listener": out})
}

// UpdateListenerAPI 修改监听（运行中会重启）
func UpdateListenerAPI(c *gin.Context) {
	var l Listener
	if err := c.ShouldBindJSON(&l); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := UpdateListener(c.Param("id"), l); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// DeleteListenerAPI 删除监听
func DeleteListenerAPI(c *gin.Context) {
	if err := DeleteListener(c.Param("id")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// StartListenerAPI 启动监听
func StartListenerAPI(c *gin.Context) {
	if err := StartListener(c.Param("id")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// StopListenerAPI 停止监听
func StopListenerAPI(c *gin.Context) {
	if err := StopListener(c.Param("id")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ListSessionsAPI 列出会话；支持 ?listener=<id>&limit=n
func ListSessionsAPI(c *gin.Context) {
	listenerID := c.Param("id")
	if listenerID == "" {
		listenerID = c.Query("listener")
	}
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	out := ListSessions(listenerID)
	if len(out) > limit {
		out = out[:limit]
	}
	c.JSON(http.StatusOK, gin.H{"sessions": out})
}

// DeleteSessionAPI 断开会话
func DeleteSessionAPI(c *gin.Context) {
	if err := KillSession(c.Param("id")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ExecSessionAPI 在 shell 会话里执行命令
func ExecSessionAPI(c *gin.Context) {
	var req struct {
		Cmd string `json:"cmd"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Cmd == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cmd 不能为空"})
		return
	}
	s := GetSession(c.Param("id"))
	if s == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}
	if err := s.Exec(req.Cmd); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.listener.addEvent(eventInfo, s.remote, "手动下发命令："+req.Cmd)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// EventsAPI 监听事件流水
func EventsAPI(c *gin.Context) {
	limit := 100
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	c.JSON(http.StatusOK, gin.H{"events": Events(c.Param("id"), limit)})
}

// ListPayloadsAPI 列出 payload 文件
func ListPayloadsAPI(c *gin.Context) {
	files, err := PayloadFiles()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"payloads": files, "dir": payloadDir})
}

// UploadPayloadAPI 上传 payload 文件（exp.so、Exploit.class、gadget.jar 等）
func UploadPayloadAPI(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请用 multipart/form-data 上传 file 字段"})
		return
	}
	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer src.Close()
	data, err := io.ReadAll(io.LimitReader(src, 64*1024*1024))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name, err := SavePayload(file.Filename, data)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"name": name, "size": len(data)})
}

// DeletePayloadAPI 删除 payload 文件
func DeletePayloadAPI(c *gin.Context) {
	if err := DeletePayload(c.Param("name")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// HandleWS 把浏览器 xterm 挂到某个 nc 会话上
func HandleWS(c *gin.Context) {
	s := GetSession(c.Param("id"))
	if s == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	ch, detach := s.Attach()
	defer detach()
	defer ws.Close()

	go func() {
		for b := range ch {
			if err := ws.WriteMessage(websocket.BinaryMessage, b); err != nil {
				ws.Close()
				return
			}
		}
		// 会话结束：给前端一个收尾提示再关闭
		ws.WriteMessage(websocket.BinaryMessage,
			[]byte("\r\n\x1b[31m[会话已结束]\x1b[0m\r\n"))
		ws.Close()
	}()

	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		s.SendInput(data)
	}
}
