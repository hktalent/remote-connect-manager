package config

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"remote-connect-manager/internal/screenshot"
)

type Connection struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Type        string    `json:"type"` // ssh, vnc, rdp
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Username    string    `json:"username"`
	Password    string    `json:"password,omitempty"`
	AuthType    string    `json:"auth_type"` // password, key
	KeyName     string    `json:"key_name,omitempty"`
	PrivateKey  string    `json:"private_key,omitempty"`
	PublicKey   string    `json:"public_key,omitempty"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	Screenshot  string    `json:"screenshot,omitempty"`
	ColorDepth  int       `json:"color_depth,omitempty"`
	Security    string    `json:"security,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	LastConnect time.Time `json:"last_connect,omitempty"`
}

var (
	dataFile string
	mu       sync.RWMutex
)

func InitDB(path string) error {
	dataFile = path
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return os.WriteFile(path, []byte("[]"), 0644)
	}
	return nil
}

func loadAll() ([]Connection, error) {
	mu.RLock()
	defer mu.RUnlock()
	data, err := os.ReadFile(dataFile)
	if err != nil {
		return nil, err
	}
	var conns []Connection
	if err := json.Unmarshal(data, &conns); err != nil {
		return nil, err
	}
	return conns, nil
}

func saveAll(conns []Connection) error {
	mu.Lock()
	defer mu.Unlock()
	data, err := json.MarshalIndent(conns, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dataFile, data, 0644)
}

func ListConnections(c *gin.Context) {
	conns, err := loadAll()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Strip secrets for listing
	out := make([]Connection, len(conns))
	for i, c2 := range conns {
		c2.Password = ""
		c2.PrivateKey = ""
		out[i] = c2
	}
	c.JSON(http.StatusOK, out)
}

func GetConnection(c *gin.Context) {
	id := c.Param("id")
	conn, err := GetConnectionByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, conn)
}

func GetConnectionByID(id string) (*Connection, error) {
	conns, err := loadAll()
	if err != nil {
		return nil, err
	}
	for i := range conns {
		if conns[i].ID == id {
			return &conns[i], nil
		}
	}
	return nil, os.ErrNotExist
}

func CreateConnection(c *gin.Context) {
	var conn Connection
	if err := c.ShouldBindJSON(&conn); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	conn.ID = uuid.New().String()
	conn.CreatedAt = time.Now()
	conn.UpdatedAt = time.Now()
	if conn.Width == 0 {
		conn.Width = 1280
	}
	if conn.Height == 0 {
		conn.Height = 720
	}
	if conn.Port == 0 {
		switch conn.Type {
		case "ssh":
			conn.Port = 22
		case "vnc":
			conn.Port = 5900
		case "rdp":
			conn.Port = 3389
		}
	}
	conns, err := loadAll()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	conns = append(conns, conn)
	if err := saveAll(conns); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, conn)
}

func UpdateConnection(c *gin.Context) {
	id := c.Param("id")
	var conn Connection
	if err := c.ShouldBindJSON(&conn); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	conn.ID = id
	conn.UpdatedAt = time.Now()
	conns, err := loadAll()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	found := false
	for i := range conns {
		if conns[i].ID == id {
			conn.CreatedAt = conns[i].CreatedAt
			conn.Screenshot = conns[i].Screenshot
			conns[i] = conn
			found = true
			break
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if err := saveAll(conns); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, conn)
}

func DeleteConnection(c *gin.Context) {
	id := c.Param("id")
	conns, err := loadAll()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	filtered := conns[:0]
	for _, c2 := range conns {
		if c2.ID != id {
			filtered = append(filtered, c2)
		}
	}
	if err := saveAll(filtered); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// RenameKeyReference 密钥改名后同步更新连接配置中的引用，避免配置指向不存在的密钥
func RenameKeyReference(oldName, newName string) {
	conns, err := loadAll()
	if err != nil {
		return
	}
	changed := false
	for i := range conns {
		if conns[i].KeyName == oldName {
			conns[i].KeyName = newName
			changed = true
		}
	}
	if changed {
		saveAll(conns)
	}
}

func UpdateScreenshot(id, path string) error {	conns, err := loadAll()
	if err != nil {
		return err
	}
	for i := range conns {
		if conns[i].ID == id {
			conns[i].Screenshot = path
			conns[i].LastConnect = time.Now()
			break
		}
	}
	return saveAll(conns)
}

func GetScreenshotURL(c *gin.Context) {
	id := c.Param("id")
	conn, err := GetConnectionByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"screenshot": conn.Screenshot})
}

func UploadScreenshot(c *gin.Context) {
	id := c.Param("id")
	var body struct {
		Data string `json:"data"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	path, err := saveScreenshot(id, body.Data)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := UpdateScreenshot(id, path); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"screenshot": path})
}

func saveScreenshot(id, data string) (string, error) {
	prefix := "data:image/png;base64,"
	if len(data) > len(prefix) && data[:len(prefix)] == prefix {
		data = data[len(prefix):]
	}
	return screenshot.SaveScreenshotData(id, data)
}
