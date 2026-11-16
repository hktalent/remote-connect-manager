package keys

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"remote-connect-manager/internal/config"
)

var keysDir string

func Init(dir string) {
	keysDir = dir
	os.MkdirAll(dir, 0700)
}

// SSHDir 返回要扫描的 ~/.ssh 目录（容器内默认 /host-ssh，本地默认 $HOME/.ssh）
func SSHDir() string {
	if d := os.Getenv("SSH_SCAN_DIR"); d != "" {
		return d
	}
	if fi, err := os.Stat("/host-ssh"); err == nil && fi.IsDir() {
		return "/host-ssh"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh")
}

type KeyInfo struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key,omitempty"`
	CreatedAt string `json:"created_at"`
	Source    string `json:"source,omitempty"`
	Encrypted bool   `json:"encrypted,omitempty"`
}

// 记录每个密钥的来源（生成 / 从 ~/.ssh 导入）
const manifestName = ".manifest.json"

func readManifest() map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(filepath.Join(keysDir, manifestName))
	if err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

func writeManifest(m map[string]string) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(filepath.Join(keysDir, manifestName), data, 0600)
}

func ListKeys(c *gin.Context) {
	entries, err := os.ReadDir(keysDir)
	if err != nil {
		c.JSON(http.StatusOK, []KeyInfo{})
		return
	}
	manifest := readManifest()
	keys := []KeyInfo{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".pub") || strings.HasPrefix(name, ".") {
			continue
		}
		info, _ := e.Info()
		ki := KeyInfo{Name: name, CreatedAt: info.ModTime().Format("2006-01-02 15:04:05")}
		if pub, err := os.ReadFile(filepath.Join(keysDir, name+".pub")); err == nil {
			ki.PublicKey = strings.TrimSpace(string(pub))
		}
		if src, ok := manifest[name]; ok {
			ki.Source = src
		}
		if raw, err := os.ReadFile(filepath.Join(keysDir, name)); err == nil {
			if _, perr := ssh.ParsePrivateKey(raw); perr != nil && looksLikePrivateKey(raw) {
				ki.Encrypted = true
			}
		}
		keys = append(keys, ki)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })
	c.JSON(http.StatusOK, keys)
}

// ScanKeys 手动触发扫描 ~/.ssh 并导入其中的私钥
func ScanKeys(c *gin.Context) {
	imported, skipped, dir := ImportFromSSH()
	c.JSON(http.StatusOK, gin.H{"dir": dir, "imported": imported, "skipped": skipped})
}

// ImportFromSSH 扫描 ~/.ssh，把可解析的私钥复制进密钥库（同名同内容跳过）
func ImportFromSSH() (imported []string, skipped []string, dir string) {
	dir = SSHDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{}, []string{fmt.Sprintf("无法读取 %s: %v", dir, err)}, dir
	}
	manifest := readManifest()
	manifestChanged := false

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if strings.HasSuffix(name, ".pub") ||
			name == "config" || name == "known_hosts" ||
			strings.HasPrefix(name, "known_hosts.") ||
			name == "authorized_keys" || name == "authorized_keys2" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(data)
		encrypted := false
		if err != nil {
			if !looksLikePrivateKey(data) {
				// 既不是私钥也不是可解析文件，跳过
				skipped = append(skipped, name)
				continue
			}
			// 带口令的加密私钥：仍然导入，连接时再提供口令
			encrypted = true
		}

		target := sanitizeName(name)
		targetPath := filepath.Join(keysDir, target)
		if existing, err := os.ReadFile(targetPath); err == nil {
			if string(existing) == string(data) {
				if _, ok := manifest[target]; !ok {
					manifest[target] = dir + "/" + name
					manifestChanged = true
				}
				continue // 已存在且内容一致
			}
			sum := sha256.Sum256(data)
			target = target + "_" + hex.EncodeToString(sum[:])[:6]
			targetPath = filepath.Join(keysDir, target)
		}

		if err := os.WriteFile(targetPath, data, 0600); err != nil {
			skipped = append(skipped, name)
			continue
		}
		var pub []byte
		if signer != nil {
			pub = ssh.MarshalAuthorizedKey(signer.PublicKey())
		} else if p, err := os.ReadFile(filepath.Join(dir, name+".pub")); err == nil {
			// 加密私钥无法解析，直接沿用同目录下的公钥文件
			pub = p
		}
		if len(pub) > 0 {
			os.WriteFile(targetPath+".pub", pub, 0644)
		}
		manifest[target] = dir + "/" + name
		manifestChanged = true
		if encrypted {
			imported = append(imported, target+"(加密)")
		} else {
			imported = append(imported, target)
		}
	}
	if manifestChanged {
		writeManifest(manifest)
	}
	if imported == nil {
		imported = []string{}
	}
	if skipped == nil {
		skipped = []string{}
	}
	return imported, skipped, dir
}

func sanitizeName(name string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_")
	return replacer.Replace(name)
}

// looksLikePrivateKey 判断文件头是否是私钥（含加密私钥）
func looksLikePrivateKey(data []byte) bool {
	s := string(data)
	if !strings.Contains(s, "PRIVATE KEY-----") {
		return false
	}
	return strings.Contains(s, "BEGIN OPENSSH PRIVATE KEY") ||
		strings.Contains(s, "BEGIN RSA PRIVATE KEY") ||
		strings.Contains(s, "BEGIN EC PRIVATE KEY") ||
		strings.Contains(s, "BEGIN DSA PRIVATE KEY") ||
		strings.Contains(s, "BEGIN PRIVATE KEY") ||
		strings.Contains(s, "BEGIN ENCRYPTED PRIVATE KEY")
}

// LoadSigner 加载私钥，若是加密私钥则用 passphrase 解密
func LoadSigner(keyName, passphrase string) (ssh.Signer, error) {
	pemBytes, err := LoadPrivateKey(keyName)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey([]byte(pemBytes))
	if err == nil {
		return signer, nil
	}
	if passphrase == "" {
		return nil, fmt.Errorf("密钥 %s 已加密，需要在密码栏填写密钥口令", keyName)
	}
	return ssh.ParsePrivateKeyWithPassphrase([]byte(pemBytes), []byte(passphrase))
}

func GenerateKey(c *gin.Context) {
	var body struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name required"})
		return
	}
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	privBytes := x509.MarshalPKCS1PrivateKey(priv)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privBytes})

	pubSSH, err := ssh.NewPublicKey(&priv.PublicKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	pubBytes := ssh.MarshalAuthorizedKey(pubSSH)

	privPath := filepath.Join(keysDir, body.Name)
	pubPath := filepath.Join(keysDir, body.Name+".pub")
	if err := os.WriteFile(privPath, privPEM, 0600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	os.WriteFile(pubPath, pubBytes, 0644)

	c.JSON(http.StatusOK, gin.H{
		"name":       body.Name,
		"public_key": strings.TrimSpace(string(pubBytes)),
	})
}

func ImportKey(c *gin.Context) {
	var body struct {
		Name       string `json:"name"`
		PrivateKey string `json:"private_key"`
		PublicKey  string `json:"public_key"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Name == "" || body.PrivateKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and private_key required"})
		return
	}
	privPath := filepath.Join(keysDir, body.Name)
	if err := os.WriteFile(privPath, []byte(body.PrivateKey), 0600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if body.PublicKey == "" {
		// derive public key from private
		block, _ := pem.Decode([]byte(body.PrivateKey))
		if block != nil {
			priv, err := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err == nil {
				pubSSH, _ := ssh.NewPublicKey(&priv.PublicKey)
				if pubSSH != nil {
					body.PublicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubSSH)))
				}
			}
		}
	}
	if body.PublicKey != "" {
		os.WriteFile(filepath.Join(keysDir, body.Name+".pub"), []byte(body.PublicKey+"\n"), 0644)
	}
	c.JSON(http.StatusOK, gin.H{"name": body.Name, "public_key": body.PublicKey})
}

func DeleteKey(c *gin.Context) {
	name := c.Param("name")
	os.Remove(filepath.Join(keysDir, name))
	os.Remove(filepath.Join(keysDir, name+".pub"))
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// UpdateKey 编辑已有密钥：可改名、替换私钥内容、更新公钥
func UpdateKey(c *gin.Context) {
	oldName := c.Param("name")
	if sanitizeName(oldName) != oldName {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法密钥名"})
		return
	}
	var body struct {
		NewName    string `json:"new_name"`
		PrivateKey string `json:"private_key"`
		PublicKey  string `json:"public_key"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	oldPriv := filepath.Join(keysDir, oldName)
	oldPub := oldPriv + ".pub"
	if _, err := os.Stat(oldPriv); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "密钥不存在"})
		return
	}

	newName := oldName
	if body.NewName != "" && body.NewName != oldName {
		newName = sanitizeName(body.NewName)
		if newName == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "密钥名不能为空"})
			return
		}
		if _, err := os.Stat(filepath.Join(keysDir, newName)); err == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "同名密钥已存在"})
			return
		}
	}

	privData := body.PrivateKey
	if privData == "" {
		// 未修改私钥内容，沿用原文件
		if raw, err := os.ReadFile(oldPriv); err == nil {
			privData = string(raw)
		}
	} else if !looksLikePrivateKey([]byte(privData)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "私钥内容格式不正确"})
		return
	}

	newPriv := filepath.Join(keysDir, newName)
	if newName != oldName {
		if err := os.Rename(oldPriv, newPriv); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if _, err := os.Stat(oldPub); err == nil {
			os.Rename(oldPub, newPriv+".pub")
		}
	}
	if err := os.WriteFile(newPriv, []byte(privData), 0600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 公钥：优先用传入值，否则尝试由私钥推导
	pub := strings.TrimSpace(body.PublicKey)
	if pub == "" {
		if signer, err := ssh.ParsePrivateKey([]byte(privData)); err == nil {
			pub = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
		} else if raw, err := os.ReadFile(newPriv + ".pub"); err == nil {
			pub = strings.TrimSpace(string(raw))
		}
	}
	if pub != "" {
		os.WriteFile(newPriv+".pub", []byte(pub+"\n"), 0644)
	}

	// 同步 manifest 中的来源记录
	if manifest := readManifest(); len(manifest) > 0 {
		if src, ok := manifest[oldName]; ok {
			delete(manifest, oldName)
			manifest[newName] = src
			writeManifest(manifest)
		}
	}

	// 改名后同步更新连接配置里的密钥引用
	if newName != oldName {
		config.RenameKeyReference(oldName, newName)
	}

	c.JSON(http.StatusOK, gin.H{"name": newName, "public_key": pub})
}

func GetKey(c *gin.Context) {
	name := c.Param("name")
	data, err := os.ReadFile(filepath.Join(keysDir, name))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"name": name, "private_key": string(data)})
}

func GetPublicKey(c *gin.Context) {
	name := c.Param("name")
	data, err := os.ReadFile(filepath.Join(keysDir, name+".pub"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"name": name, "public_key": strings.TrimSpace(string(data))})
}

// LoadPrivateKey loads a named private key from disk
func LoadPrivateKey(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(keysDir, name))
	if err != nil {
		return "", fmt.Errorf("key %s not found: %w", name, err)
	}
	return string(data), nil
}
