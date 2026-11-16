package vnc

import (
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"remote-connect-manager/internal/config"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
	Subprotocols: []string{"binary"},
}

func HandleVNC(c *gin.Context) {
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

	vncAddr := fmt.Sprintf("%s:%d", conn.Host, conn.Port)
	vncConn, err := net.Dial("tcp", vncAddr)
	if err != nil {
		ws.WriteJSON(gin.H{"error": err.Error()})
		return
	}
	defer vncConn.Close()

	done := make(chan struct{}, 2)

	// VNC -> WS
	go func() {
		buf := make([]byte, 65536)
		for {
			n, err := vncConn.Read(buf)
			if n > 0 {
				ws.WriteMessage(websocket.BinaryMessage, buf[:n])
			}
			if err != nil {
				done <- struct{}{}
				return
			}
		}
	}()

	// WS -> VNC
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				done <- struct{}{}
				return
			}
			vncConn.Write(data)
		}
	}()

	<-done
	_ = io.EOF
}
