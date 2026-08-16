package verification

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	verificationapplication "rag-reasoning-platform/backend/internal/application/verification"
	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
)

const minimumHMACSecretBytes = 32

var (
	// ErrHMACSecretTooShort 表示验证码摘要密钥没有达到最低安全长度。
	ErrHMACSecretTooShort = errors.New("verification HMAC secret must be at least 32 bytes")
)

// HMACCodeHasher 使用服务端密钥生成验证码摘要。
// 即使数据库泄漏，攻击者没有服务端密钥也不能离线枚举全部六位验证码。
type HMACCodeHasher struct {
	secret []byte
}

var _ verificationapplication.CodeHasher = (*HMACCodeHasher)(nil)

// NewHMACCodeHasher 创建验证码摘要器，并复制密钥避免调用方后续修改底层切片。
func NewHMACCodeHasher(secret []byte) (*HMACCodeHasher, error) {
	if len(secret) < minimumHMACSecretBytes {
		return nil, ErrHMACSecretTooShort
	}

	secretCopy := append([]byte(nil), secret...)
	return &HMACCodeHasher{secret: secretCopy}, nil
}

// Hash 返回 HMAC-SHA-256 的小写十六进制结果。
func (h *HMACCodeHasher) Hash(
	channel authdomain.VerificationChannel,
	destination string,
	purpose authdomain.VerificationPurpose,
	code string,
) string {
	return hex.EncodeToString(
		h.digest(channel, destination, purpose, code),
	)
}

// Matches 使用常量时间比较核对验证码，减少基于比较耗时推测摘要内容的风险。
func (h *HMACCodeHasher) Matches(
	expectedHash string,
	channel authdomain.VerificationChannel,
	destination string,
	purpose authdomain.VerificationPurpose,
	code string,
) bool {
	expectedDigest, err := hex.DecodeString(expectedHash)
	if err != nil || len(expectedDigest) != sha256.Size {
		return false
	}

	actualDigest := h.digest(channel, destination, purpose, code)
	return hmac.Equal(expectedDigest, actualDigest)
}

func (h *HMACCodeHasher) digest(
	channel authdomain.VerificationChannel,
	destination string,
	purpose authdomain.VerificationPurpose,
	code string,
) []byte {
	digest := hmac.New(sha256.New, h.secret)

	// NUL 分隔符防止不同字段拼接后产生相同字节序列。
	for _, field := range []string{
		string(channel),
		destination,
		string(purpose),
		code,
	} {
		_, _ = digest.Write([]byte(field))
		_, _ = digest.Write([]byte{0})
	}

	return digest.Sum(nil)
}
