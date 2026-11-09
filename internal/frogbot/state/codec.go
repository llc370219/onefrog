// Package state adapts Frog Bot's persistence to the host's encryption.
package state

import "os"

type Codec interface {
	Encrypt(string) (string, error)
	Decrypt(string) (string, error)
}

func Encode(codec Codec, raw []byte) ([]byte, error) {
	if codec == nil {
		return raw, nil
	}
	s, err := codec.Encrypt(string(raw))
	return []byte(s), err
}

func Decode(codec Codec, raw []byte) ([]byte, error) {
	if codec == nil {
		return raw, nil
	}
	s, err := codec.Decrypt(string(raw))
	return []byte(s), err
}

func ReadFile(codec Codec, path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Decode(codec, raw)
}

func WriteFile(codec Codec, path string, raw []byte) error {
	raw, err := Encode(codec, raw)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
