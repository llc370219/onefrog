package secret

import (
	"errors"
	"strings"
	"unicode"
)

const (
	// MaxNameRunes is the longest vault name. Names are human labels, including
	// Chinese, and are embedded in {{secret:name}} so they cannot contain the
	// placeholder delimiters or whitespace.
	MaxNameRunes = 64
	// MaxLabelRunes is the password-card title.
	MaxLabelRunes = 120
	// MaxDescriptionRunes is the optional explanation on the card and in the vault.
	MaxDescriptionRunes = 400
	// MaxValueBytes is the largest plaintext a secret may hold.
	MaxValueBytes = 32 * 1024
	// MaxCount is how many secrets the vault will store.
	MaxCount = 100
)

// ValidName accepts a vault name. It trims surrounding space and rejects
// empty names, names longer than MaxNameRunes, and any brace, whitespace, or
// control character.
func ValidName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("密钥名称不能为空")
	}
	if len([]rune(name)) > MaxNameRunes {
		return "", errors.New("密钥名称最多 64 个字")
	}
	for _, r := range name {
		if r == '{' || r == '}' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", errors.New("密钥名称不能包含空格、换行或大括号")
		}
	}
	return name, nil
}

// ValidValue rejects an empty secret or one larger than MaxValueBytes.
// The plaintext is never included in the error.
func ValidValue(value string) error {
	if value == "" {
		return errors.New("密钥内容不能为空")
	}
	if len(value) > MaxValueBytes {
		return errors.New("密钥内容不能超过 32 KiB")
	}
	return nil
}

// CleanLabel makes a single-line card title and clips it to MaxLabelRunes.
func CleanLabel(raw string) (string, error) {
	label := oneLine(raw)
	if label == "" {
		return "", errors.New("密码卡片需要标题")
	}
	return clipRunes(label, MaxLabelRunes), nil
}

// CleanDescription strips control characters, keeps newlines, and clips.
func CleanDescription(raw string) string {
	s := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw)
	return clipRunes(strings.TrimSpace(s), MaxDescriptionRunes)
}

func oneLine(raw string) string {
	s := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw)
	return strings.Join(strings.Fields(s), " ")
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
