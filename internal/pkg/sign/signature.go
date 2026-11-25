package sign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var _ Signature = (*signature)(nil)

const (
	delimiter = "|"
)

// 合法的 Methods
var methods = map[string]bool{
	http.MethodGet:     true,
	http.MethodPost:    true,
	http.MethodHead:    true,
	http.MethodPut:     true,
	http.MethodPatch:   true,
	http.MethodDelete:  true,
	http.MethodConnect: true,
	http.MethodOptions: true,
	http.MethodTrace:   true,
}

type Signature interface {

	// Generate 生成签名
	Generate(path string, method string, params url.Values) (authorization, date string, err error)

	// Verify 验证签名
	Verify(authorization, date string, path string, method string, params url.Values) (ok bool, err error)
}

type signature struct {
	key    string
	secret string
	ttl    time.Duration
}

func New(key, secret string, ttl time.Duration) Signature {
	return &signature{
		key:    key,
		secret: secret,
		ttl:    ttl,
	}
}

var signatureZone = time.FixedZone("CST", 8*60*60)

func (s *signature) digest(path, method string, params url.Values, date string) (string, error) {
	method = strings.ToUpper(method)
	if path == "" || !methods[method] || s.key == "" || s.secret == "" || s.ttl <= 0 {
		return "", errors.New("invalid signature parameters")
	}
	query, err := url.QueryUnescape(params.Encode())
	if err != nil {
		return "", fmt.Errorf("decode signature query: %w", err)
	}
	mac := hmac.New(sha256.New, []byte(s.secret))
	mac.Write([]byte(strings.Join([]string{path, method, query, date}, delimiter)))
	return s.key + " " + base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *signature) Generate(path, method string, params url.Values) (string, string, error) {
	date := time.Now().In(signatureZone).Format(time.DateTime)
	auth, err := s.digest(path, method, params, date)
	if err != nil {
		return "", "", err
	}
	return auth, date, nil
}

func (s *signature) Verify(authorization, date, path, method string, params url.Values) (bool, error) {
	ts, err := time.ParseInLocation(time.DateTime, date, signatureZone)
	if err != nil {
		return false, fmt.Errorf("parse signature date: %w", err)
	}
	if age := time.Since(ts); age < 0 || age > s.ttl {
		return false, errors.New("signature date outside validity window")
	}
	expected, err := s.digest(path, method, params, date)
	if err != nil {
		return false, err
	}
	return hmac.Equal([]byte(authorization), []byte(expected)), nil
}
