package migration

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

const Pointer = ".migration-current"

var generation = regexp.MustCompile(`^\.restore-[a-zA-Z0-9]+$`)

// Resolve switches generations inside the existing data volume. The previous
// generation is never overwritten and can be selected again for rollback.
func Resolve(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, Pointer))
	if os.IsNotExist(err) {
		return root, nil
	}
	if err != nil {
		return "", err
	}
	name := string(b)
	if !generation.MatchString(name) {
		return "", errors.New("迁移数据指针无效")
	}
	p := filepath.Join(root, name)
	info, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("迁移数据目录无效")
	}
	return p, nil
}

func Stage(root string, files map[string][]byte, manifest Manifest) (string, error) {
	dir, err := os.MkdirTemp(root, ".restore-")
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dir)
		}
	}()
	for name, b := range files {
		if !allowed(name) {
			return "", errors.New("恢复文件路径无效")
		}
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			return "", err
		}
		if err = writeSync(p, b); err != nil {
			return "", err
		}
	}
	b, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	if err = writeSync(filepath.Join(dir, ManifestName), b); err != nil {
		return "", err
	}
	if err = writeSync(filepath.Join(dir, ".migration-paused"), []byte("restored\n")); err != nil {
		return "", err
	}
	ok = true
	return dir, nil
}

func Activate(root, dir string) error {
	if filepath.Dir(dir) != filepath.Clean(root) || !generation.MatchString(filepath.Base(dir)) {
		return errors.New("无效恢复目录")
	}
	old, err := os.ReadFile(filepath.Join(root, Pointer))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err = writeSync(filepath.Join(root, ".migration-previous"), old); err != nil {
		return err
	}
	tmp := filepath.Join(root, Pointer+".tmp")
	if err = writeSync(tmp, []byte(filepath.Base(dir))); err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(root, Pointer)); err != nil {
		return err
	}
	f, err := os.Open(root)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func writeSync(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	e := f.Close()
	if err == nil {
		err = e
	}
	return err
}
