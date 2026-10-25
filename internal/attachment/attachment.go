package attachment

import (
	"encoding/json"
	"errors"
	"net/http"
	"onefrog/internal/id"
	"onefrog/internal/model"
	"onefrog/internal/secret"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

const MaxSize = 5 * 1024 * 1024
const MaxTotal = 12 * 1024 * 1024
const MaxFiles = 4

type File struct {
	model.Attachment
	OwnerID string `json:"owner_id"`
	Data    []byte `json:"data"`
}
type Manager struct {
	mu    sync.Mutex
	path  string
	box   *secret.Box
	files map[string]File
}

func New(path string, box *secret.Box) *Manager {
	return &Manager{path: path, box: box, files: map[string]File{}}
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9-]{8,80}$`)

func (m *Manager) Put(owner, name string, data []byte) (model.Attachment, error) {
	if len(data) == 0 || len(data) > MaxSize {
		return model.Attachment{}, errors.New("文件为空或超过 5 MB")
	}
	mime := http.DetectContentType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/webp", "application/pdf":
	default:
		if len(data) > 256*1024 || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
			return model.Attachment{}, errors.New("支持 PNG、JPEG、WebP 图片、PDF，以及小于 256 KB 的 UTF-8 文本、配置、日志或代码文件")
		}
		mime = "text/plain"
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if len([]rune(name)) > 160 {
		return model.Attachment{}, errors.New("文件名过长")
	}
	f := File{Attachment: model.Attachment{ID: id.New(), Name: name, Mime: mime, Size: len(data)}, OwnerID: owner, Data: append([]byte(nil), data...)}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.path != "" {
		if e := os.MkdirAll(m.path, 0700); e != nil {
			return model.Attachment{}, e
		}
		b, e := json.Marshal(f)
		if e != nil {
			return model.Attachment{}, e
		}
		enc, e := m.box.Encrypt(string(b))
		if e != nil {
			return model.Attachment{}, e
		}
		if e = os.WriteFile(filepath.Join(m.path, f.ID+".enc"), []byte(enc), 0600); e != nil {
			return model.Attachment{}, e
		}
	} else {
		m.files[f.ID] = f
	}
	return f.Attachment, nil
}
func (m *Manager) Get(owner, fileID string) (File, error) {
	if !validID.MatchString(fileID) {
		return File{}, errors.New("附件不存在")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[fileID]
	if m.path != "" {
		raw, e := os.ReadFile(filepath.Join(m.path, fileID+".enc"))
		if e != nil {
			return File{}, errors.New("附件不存在")
		}
		plain, e := m.box.Decrypt(string(raw))
		if e != nil {
			return File{}, errors.New("附件无法解密")
		}
		if e = json.Unmarshal([]byte(plain), &f); e != nil {
			return File{}, e
		}
		ok = true
	}
	if !ok || f.OwnerID != owner {
		return File{}, errors.New("附件不存在")
	}
	f.Data = append([]byte(nil), f.Data...)
	return f, nil
}
