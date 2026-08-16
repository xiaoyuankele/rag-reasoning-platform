// Package session 提供不透明 Session Token 的生成和摘要能力。
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"

	authapplication "rag-reasoning-platform/backend/internal/application/auth"
)

const sessionTokenBytes = 32

// TokenGenerator 使用加密安全随机源生成 256-bit Session Token。
type TokenGenerator struct {
	random io.Reader
}

var _ authapplication.SessionTokenGenerator = (*TokenGenerator)(nil)

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
	digest := sha256.Sum256([]byte(rawToken))

	return authapplication.SessionTokenPair{
		Raw:  rawToken,
		Hash: hex.EncodeToString(digest[:]),
	}, nil
}
