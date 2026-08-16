// Package password 提供密码哈希端口的基础设施实现。
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"

	authapplication "rag-reasoning-platform/backend/internal/application/auth"
)

const (
	defaultMemoryKiB   = 19 * 1024
	defaultIterations  = 2
	defaultParallelism = 1
	defaultSaltLength  = 16
	defaultKeyLength   = 32

	maximumMemoryKiB   = 64 * 1024
	maximumIterations  = 10
	maximumParallelism = 4
	maximumSaltLength  = 64
	maximumKeyLength   = 64
)

var (
	// ErrInvalidParameters 表示 Argon2id 成本或输出参数不安全。
	ErrInvalidParameters = errors.New("invalid Argon2id parameters")

	// ErrInvalidEncodedHash 表示数据库中的密码哈希不是受支持的编码格式。
	ErrInvalidEncodedHash = errors.New("invalid encoded Argon2id hash")
)

// Parameters 保存 Argon2id 的成本参数。
// Memory 的单位是 KiB，不是字节。
type Parameters struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultParameters 返回适合当前低内存单机开发环境的安全基线。
func DefaultParameters() Parameters {
	return Parameters{
		MemoryKiB:   defaultMemoryKiB,
		Iterations:  defaultIterations,
		Parallelism: defaultParallelism,
		SaltLength:  defaultSaltLength,
		KeyLength:   defaultKeyLength,
	}
}

// Argon2idHasher 负责生成随机 salt、派生密码摘要并编码参数。
type Argon2idHasher struct {
	parameters Parameters
	random     io.Reader
}

var _ authapplication.PasswordHasher = (*Argon2idHasher)(nil)

// NewArgon2idHasher 创建密码哈希器。
func NewArgon2idHasher(parameters Parameters) (*Argon2idHasher, error) {
	if err := validateParameters(parameters); err != nil {
		return nil, err
	}

	return &Argon2idHasher{
		parameters: parameters,
		random:     rand.Reader,
	}, nil
}

// Hash 使用独立随机 salt 生成 PHC 风格的 Argon2id 编码字符串。
func (h *Argon2idHasher) Hash(password string) (string, error) {
	salt := make([]byte, h.parameters.SaltLength)
	if _, err := io.ReadFull(h.random, salt); err != nil {
		return "", fmt.Errorf("read Argon2id salt: %w", err)
	}

	derivedKey := argon2.IDKey(
		[]byte(password),
		salt,
		h.parameters.Iterations,
		h.parameters.MemoryKiB,
		h.parameters.Parallelism,
		h.parameters.KeyLength,
	)

	return encodeHash(h.parameters, salt, derivedKey), nil
}

// Verify 读取编码中的参数和 salt，重新计算摘要并使用常量时间比较。
func (h *Argon2idHasher) Verify(
	password string,
	encodedHash string,
) (bool, error) {
	parameters, salt, expectedKey, err := decodeHash(encodedHash)
	if err != nil {
		return false, err
	}

	actualKey := argon2.IDKey(
		[]byte(password),
		salt,
		parameters.Iterations,
		parameters.MemoryKiB,
		parameters.Parallelism,
		parameters.KeyLength,
	)

	return subtle.ConstantTimeCompare(actualKey, expectedKey) == 1, nil
}

func encodeHash(
	parameters Parameters,
	salt []byte,
	derivedKey []byte,
) string {
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		parameters.MemoryKiB,
		parameters.Iterations,
		parameters.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(derivedKey),
	)
}

func decodeHash(encodedHash string) (Parameters, []byte, []byte, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Parameters{}, nil, nil, ErrInvalidEncodedHash
	}

	versionText, found := strings.CutPrefix(parts[2], "v=")
	if !found {
		return Parameters{}, nil, nil, ErrInvalidEncodedHash
	}
	version, err := strconv.Atoi(versionText)
	if err != nil || version != argon2.Version {
		return Parameters{}, nil, nil, ErrInvalidEncodedHash
	}

	parameterParts := strings.Split(parts[3], ",")
	if len(parameterParts) != 3 {
		return Parameters{}, nil, nil, ErrInvalidEncodedHash
	}

	memory, err := parseUintParameter(parameterParts[0], "m=")
	if err != nil {
		return Parameters{}, nil, nil, err
	}
	iterations, err := parseUintParameter(parameterParts[1], "t=")
	if err != nil {
		return Parameters{}, nil, nil, err
	}
	parallelism, err := parseUintParameter(parameterParts[2], "p=")
	if err != nil || parallelism > 255 {
		return Parameters{}, nil, nil, ErrInvalidEncodedHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Parameters{}, nil, nil, ErrInvalidEncodedHash
	}
	expectedKey, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return Parameters{}, nil, nil, ErrInvalidEncodedHash
	}

	parameters := Parameters{
		MemoryKiB:   uint32(memory),
		Iterations:  uint32(iterations),
		Parallelism: uint8(parallelism),
		SaltLength:  uint32(len(salt)),
		KeyLength:   uint32(len(expectedKey)),
	}
	if err := validateParameters(parameters); err != nil {
		return Parameters{}, nil, nil, ErrInvalidEncodedHash
	}

	return parameters, salt, expectedKey, nil
}

func parseUintParameter(value string, prefix string) (uint64, error) {
	numberText, found := strings.CutPrefix(value, prefix)
	if !found {
		return 0, ErrInvalidEncodedHash
	}

	number, err := strconv.ParseUint(numberText, 10, 32)
	if err != nil {
		return 0, ErrInvalidEncodedHash
	}

	return number, nil
}

func validateParameters(parameters Parameters) error {
	if parameters.MemoryKiB < 8*1024 || parameters.MemoryKiB > maximumMemoryKiB ||
		parameters.Iterations == 0 || parameters.Iterations > maximumIterations ||
		parameters.Parallelism == 0 || parameters.Parallelism > maximumParallelism ||
		parameters.SaltLength < 16 || parameters.SaltLength > maximumSaltLength ||
		parameters.KeyLength < 16 || parameters.KeyLength > maximumKeyLength {
		return ErrInvalidParameters
	}

	return nil
}
