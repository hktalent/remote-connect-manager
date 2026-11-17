// Package sftp 提供基于 SSH 的远端文件管理接口
package sftp

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pkg/sftp"
	"remote-connect-manager/internal/config"
	sshx "remote-connect-manager/internal/ssh"
)

type fileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Dir     bool   `json:"dir"`
	Mode    string `json:"mode"`
	ModTime string `json:"mod_time"`
}

func withClient(c *gin.Context) (*sftp.Client, io.Closer, bool) {
	conn, err := config.GetConnectionByID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "connection not found"})
		return nil, nil, false
	}
	sshClient, err := sshx.Dial(conn)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return nil, nil, false
	}
	sc, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return nil, nil, false
	}
	return sc, sshClient, true
}

// List 列出目录内容，目录在前、按名称排序
func List(c *gin.Context) {
	sc, cl, ok := withClient(c)
	if !ok {
		return
	}
	defer cl.Close()
	defer sc.Close()

	dir := c.DefaultQuery("path", ".")
	if dir == "." {
		if wd, err := sc.Getwd(); err == nil {
			dir = wd
		}
	}
	entries, err := sc.ReadDir(dir)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	out := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, fileEntry{
			Name:    e.Name(),
			Path:    path.Join(dir, e.Name()),
			Size:    e.Size(),
			Dir:     e.IsDir(),
			Mode:    e.Mode().String(),
			ModTime: e.ModTime().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return out[i].Name < out[j].Name
	})
	c.JSON(http.StatusOK, gin.H{"path": dir, "entries": out})
}

// Download 把远端文件流式下载到浏览器
func Download(c *gin.Context) {
	sc, cl, ok := withClient(c)
	if !ok {
		return
	}
	defer cl.Close()
	defer sc.Close()

	p := c.Query("path")
	if p == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path required"})
		return
	}
	f, err := sc.Open(p)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil || st.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not a file"})
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(p)))
	http.ServeContent(c.Writer, c.Request, path.Base(p), st.ModTime(), f)
}

// Upload 上传浏览器选中的文件到远端目录
func Upload(c *gin.Context) {
	sc, cl, ok := withClient(c)
	if !ok {
		return
	}
	defer cl.Close()
	defer sc.Close()

	dir := c.DefaultPostForm("path", ".")
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少文件"})
		return
	}
	src, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer src.Close()

	target := path.Join(dir, fh.Filename)
	dst, err := sc.Create(target)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer dst.Close()
	n, err := io.Copy(dst, src)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"path": target, "size": n})
}

type pathReq struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
}

// Mkdir 创建目录（支持多级）
func Mkdir(c *gin.Context) {
	var req pathReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path required"})
		return
	}
	sc, cl, ok := withClient(c)
	if !ok {
		return
	}
	defer cl.Close()
	defer sc.Close()

	if err := sc.MkdirAll(req.Path); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"path": req.Path})
}

// Remove 删除文件或目录
func Remove(c *gin.Context) {
	var req pathReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path required"})
		return
	}
	sc, cl, ok := withClient(c)
	if !ok {
		return
	}
	defer cl.Close()
	defer sc.Close()

	var err error
	if req.Dir {
		err = sc.RemoveDirectory(req.Path)
	} else {
		err = sc.Remove(req.Path)
	}
	if err != nil && !os.IsNotExist(err) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"removed": req.Path})
}
