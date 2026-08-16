// Package verification 提供验证码相关端口的基础设施实现。
package verification

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"

	verificationapplication "rag-reasoning-platform/backend/internal/application/verification"
)

const verificationCodeUpperBound = 1_000_000

// RandomCodeGenerator 使用操作系统密码学安全随机源生成六位数字验证码。
type RandomCodeGenerator struct {
	reader io.Reader
}

var _ verificationapplication.CodeGenerator = (*RandomCodeGenerator)(nil)

// NewRandomCodeGenerator 创建生产用验证码生成器。
func NewRandomCodeGenerator() *RandomCodeGenerator {
	return &RandomCodeGenerator{reader: rand.Reader}
}

// Generate 返回包含前导零在内的固定六位验证码。
func (g *RandomCodeGenerator) Generate() (string, error) {
	value, err := rand.Int(
		g.reader,
		big.NewInt(verificationCodeUpperBound),
	)
	if err != nil {
		return "", fmt.Errorf("read secure random verification code: %w", err)
	}

	return fmt.Sprintf("%06d", value.Int64()), nil
}
