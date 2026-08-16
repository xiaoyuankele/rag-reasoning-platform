// Package session 提供不透明 Session Token 的生成和摘要能力。
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	authapplication "rag-reasoning-platform/backend/internal/application/auth"
)

const sessionTokenBytes = 32

var (
	// ErrInvalidSessionToken 表示 Cookie 中的 Token 不是系统生成的规范格式。
	ErrInvalidSessionToken = errors.New("invalid session token")
)

// TokenGenerator 使用加密安全随机源生成 256-bit Session Token。
type TokenGenerator struct {
	random io.Reader
}

var _ authapplication.SessionTokenGenerator = (*TokenGenerator)(nil)
var _ authapplication.SessionTokenHasher = (*TokenGenerator)(nil)

// NewTokenGenerator 创建生产 Session Token 生成器。
func NewTokenGenerator() *TokenGenerator {
	return &TokenGenerator{random: rand.Reader}
}

// Generate 返回浏览器持有的 URL 安全 Token 和数据库保存的 SHA-256 摘要。
func (g *TokenGenerator) Generate() (authapplication.SessionTokenPair, error) {
	randomBytes := make([]byte, sessionTokenBytes)
	if _, err := io.ReadFull(g.random, randomBytes); err != nil {
		return authapplication.SessionTokenPair{}, fmt.Errorf(
			"read session token randomness: %w",
			err,
		)
	}

	rawToken := base64.RawURLEncoding.EncodeToString(randomBytes)
	tokenHash, err := g.Hash(rawToken)
	if err != nil {
		return authapplication.SessionTokenPair{}, fmt.Errorf(
			"hash generated session token: %w",
			err,
		)
	}

	return authapplication.SessionTokenPair{
		Raw:  rawToken,
		Hash: tokenHash,
	}, nil
}

// Hash 校验 URL 安全 Base64 Token，并返回 SHA-256 小写十六进制摘要。
func (g *TokenGenerator) Hash(rawToken string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(rawToken)
	if err != nil || len(decoded) != sessionTokenBytes ||
		base64.RawURLEncoding.EncodeToString(decoded) != rawToken {
		return "", ErrInvalidSessionToken
	}
	digest := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(digest[:]), nil
}
