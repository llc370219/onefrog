package secret

import (
	"crypto/rand"
	"errors"
	"math/big"
	"strings"
)

const (
	// DefaultGenLength is the length used when a caller does not pick one.
	DefaultGenLength = 32
	// MinGenLength and MaxGenLength bound generated secrets.
	MinGenLength = 8
	MaxGenLength = 256
	// DefaultCharset is used when a caller does not pick one.
	DefaultCharset = "alnum"
)

const (
	lower   = "abcdefghijklmnopqrstuvwxyz"
	upper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	digits  = "0123456789"
	hexChar = "0123456789abcdef"
	// symbols avoids quotes, backslash, dollar, backtick, braces, slash,
	// whitespace and shell operators so a generated value can be dropped into a
	// command, URL or config file through {{secret:名称}} without escaping.
	symbols = "-_.,:+=@%^"
)

type charset struct {
	label   string
	alpha   string
	classes []string // each class must appear at least once
}

var charsets = map[string]charset{
	"alnum":     {"大小写字母和数字", lower + upper + digits, []string{lower, upper, digits}},
	"symbols":   {"字母、数字和安全符号", lower + upper + digits + symbols, []string{lower, upper, digits, symbols}},
	"hex":       {"十六进制", hexChar, nil},
	"base64url": {"Base64URL", lower + upper + digits + "-_", nil},
	"digits":    {"纯数字", digits, nil},
}

// Spec describes a random secret. The zero value means 32 letters and digits.
type Spec struct {
	Length  int    `json:"length"`
	Charset string `json:"charset"`
}

// Charsets lists the accepted charset names.
func Charsets() []string { return []string{"alnum", "symbols", "hex", "base64url", "digits"} }

// CharsetLabel is a short Chinese description of a charset name.
func CharsetLabel(name string) string { return charsets[name].label }

// NormalizeSpec fills defaults and rejects unknown charsets or lengths
// outside MinGenLength..MaxGenLength.
func NormalizeSpec(s Spec) (Spec, error) {
	s.Charset = strings.ToLower(strings.TrimSpace(s.Charset))
	if s.Charset == "" {
		s.Charset = DefaultCharset
	}
	if _, ok := charsets[s.Charset]; !ok {
		return Spec{}, errors.New("字符集只能是 alnum、symbols、hex、base64url 或 digits")
	}
	if s.Length == 0 {
		s.Length = DefaultGenLength
	}
	if s.Length < MinGenLength || s.Length > MaxGenLength {
		return Spec{}, errors.New("随机密钥长度必须在 8 到 256 之间")
	}
	return s, nil
}

// Generate returns a random secret drawn uniformly from the charset with
// crypto/rand. For alnum and symbols every character class appears at least
// once; candidates missing a class are discarded and drawn again, so the
// result stays uniform over the accepted strings.
func Generate(s Spec) (string, error) {
	s, err := NormalizeSpec(s)
	if err != nil {
		return "", err
	}
	cs := charsets[s.Charset]
	n := big.NewInt(int64(len(cs.alpha)))
	buf := make([]byte, s.Length)
	for attempt := 0; attempt < 1000; attempt++ {
		for i := range buf {
			k, err := rand.Int(rand.Reader, n)
			if err != nil {
				return "", errors.New("系统随机数不可用")
			}
			buf[i] = cs.alpha[k.Int64()]
		}
		if hasClasses(buf, cs.classes) {
			return string(buf), nil
		}
	}
	return "", errors.New("生成随机密钥失败")
}

func hasClasses(buf []byte, classes []string) bool {
	for _, class := range classes {
		if !strings.ContainsAny(string(buf), class) {
			return false
		}
	}
	return true
}
