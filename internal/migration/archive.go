// Package migration implements password-encrypted snapshots for file restores
// and direct server transfers. Archives never contain installation credentials.
package migration

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
	"onefrog/internal/secret"
)

const MaxBytes = 256 << 20
const maxFiles = 12000
const magic = "ONEFROG-BACKUP-1\n"
const ManifestName = "manifest.json"
const DatabaseName = "database.json"

type Manifest struct {
	Format    int               `json:"format"`
	Version   string            `json:"version"`
	Driver    string            `json:"driver"`
	CreatedAt time.Time         `json:"created_at"`
	PublicURL string            `json:"public_url"`
	Files     map[string]string `json:"files"`
}

// Only known persistent files are exported; upgrade requests, maintenance
// markers, rollback generations and ephemeral login sessions are excluded.
func allowed(name string) bool {
	switch name {
	case "master.key", "accounts.json", "servers.enc", "bots.enc", "secrets.enc", "history.enc", "modules.enc", "memories.enc", "settings.enc", "usage.enc", "oauth.enc", "assets.enc", "devmodules.enc", "servicewatch.enc", "automations.enc", "monitor.json", "onboarding.json", DatabaseName:
		return true
	}
	parts := strings.Split(name, "/")
	return len(parts) == 2 && (parts[0] == "chats" || parts[0] == "uploads") && strings.HasSuffix(parts[1], ".enc") && !strings.HasPrefix(parts[1], ".") && !strings.ContainsAny(parts[1], "\\:")
}

func ReadFiles(dir string, key []byte) (map[string][]byte, error) {
	out := map[string][]byte{}
	total := 0
	err := filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if e.IsDir() {
			if rel != "chats" && rel != "uploads" {
				return filepath.SkipDir
			}
			return nil
		}
		if !allowed(rel) || rel == DatabaseName {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("数据目录中不能包含符号链接")
		}
		if info.Size() > MaxBytes || total+int(info.Size()) > MaxBytes || len(out) >= maxFiles {
			return errors.New("备份数据超过 256 MiB 或文件数量上限")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
		f.Close()
		if err != nil {
			return err
		}
		total += len(b)
		if total > MaxBytes {
			return errors.New("备份数据过大")
		}
		out[rel] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	out["master.key"] = append([]byte(nil), key...)
	return out, nil
}

func Seal(files map[string][]byte, m Manifest, password string) ([]byte, error) {
	if len(password) < 12 || len(password) > 256 {
		return nil, errors.New("备份密码需要 12–256 字节")
	}
	m.Format = 1
	m.CreatedAt = time.Now().UTC()
	m.Files = map[string]string{}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	total := 0
	for name, b := range files {
		if !allowed(name) {
			return nil, errors.New("不支持的备份文件")
		}
		total += len(b)
		if total > MaxBytes || len(files) > maxFiles {
			return nil, errors.New("备份数据超过 256 MiB")
		}
		sum := sha256.Sum256(b)
		m.Files[name] = hex.EncodeToString(sum[:])
		f, err := w.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err = f.Write(b); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	f, err := w.Create(ManifestName)
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(raw); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	if buf.Len() > MaxBytes {
		return nil, errors.New("备份压缩包过大")
	}
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return nil, err
	}
	aead, err := derive(password, salt)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	header := append(append([]byte(magic), salt...), nonce...)
	return aead.Seal(header, nonce, buf.Bytes(), header), nil
}

func derive(password string, salt []byte) (cipher.AEAD, error) {
	key, err := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func Open(raw []byte, password string) (map[string][]byte, Manifest, error) {
	var m Manifest
	headerLen := len(magic) + 16 + 12
	if len(raw) < headerLen+16 || len(raw) > MaxBytes+1024 || !bytes.HasPrefix(raw, []byte(magic)) || len(password) < 12 || len(password) > 256 {
		return nil, m, errors.New("无效的 OneFrog 备份包或密码")
	}
	aead, err := derive(password, raw[len(magic):len(magic)+16])
	if err != nil {
		return nil, m, err
	}
	plain, err := aead.Open(nil, raw[headerLen-12:headerLen], raw[headerLen:], raw[:headerLen])
	if err != nil {
		return nil, m, errors.New("备份密码不正确或文件已损坏")
	}
	r, err := zip.NewReader(bytes.NewReader(plain), int64(len(plain)))
	if err != nil {
		return nil, m, errors.New("备份包损坏")
	}
	if len(r.File) > maxFiles+1 {
		return nil, m, errors.New("备份文件数量超限")
	}
	files := map[string][]byte{}
	total := 0
	for _, f := range r.File {
		if (!allowed(f.Name) && f.Name != ManifestName) || !f.Mode().IsRegular() {
			return nil, m, errors.New("备份含不支持的文件路径或类型")
		}
		if _, ok := files[f.Name]; ok {
			return nil, m, errors.New("备份存在重复文件")
		}
		if f.UncompressedSize64 > MaxBytes {
			return nil, m, errors.New("备份文件过大")
		}
		rd, err := f.Open()
		if err != nil {
			return nil, m, err
		}
		b, err := io.ReadAll(io.LimitReader(rd, int64(MaxBytes-total)+1))
		rd.Close()
		if err != nil {
			return nil, m, err
		}
		total += len(b)
		if total > MaxBytes {
			return nil, m, errors.New("备份解压大小超限")
		}
		files[f.Name] = b
	}
	if err = json.Unmarshal(files[ManifestName], &m); err != nil || m.Format != 1 {
		return nil, m, errors.New("不支持的备份格式")
	}
	delete(files, ManifestName)
	if len(m.Files) != len(files) {
		return nil, m, errors.New("备份清单不完整")
	}
	box, err := secret.New(files["master.key"])
	if err != nil {
		return nil, m, errors.New("备份缺少有效主密钥")
	}
	for name, b := range files {
		sum := sha256.Sum256(b)
		if m.Files[name] != hex.EncodeToString(sum[:]) {
			return nil, m, errors.New("备份校验失败")
		}
		if strings.HasSuffix(name, ".enc") {
			v, err := box.Decrypt(string(b))
			if err != nil {
				return nil, m, fmt.Errorf("备份文件无法解密：%s", name)
			}
			if !strings.HasPrefix(name, "uploads/") && !json.Valid([]byte(v)) {
				return nil, m, fmt.Errorf("备份 JSON 无效：%s", name)
			}
		} else if strings.HasSuffix(name, ".json") && !json.Valid(b) {
			return nil, m, errors.New("备份 JSON 无效")
		}
	}
	return files, m, nil
}
