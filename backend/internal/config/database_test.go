package config

import (
	"net/url"
	"strings"
	"testing"
)

// TestLoadDatabaseUsesDefaults 验证只提供密码时使用其他默认值。
func TestLoadDatabaseUsesDefaults(t *testing.T) {
	t.Setenv("DB_HOST", "")
	t.Setenv("DB_PORT", "")
	t.Setenv("DB_NAME", "")
	t.Setenv("DB_USER", "")
	t.Setenv("DB_PASSWORD", "test_password")
	t.Setenv("DB_SSLMODE", "")

	databaseConfig, err := LoadDatabase()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	expected := DatabaseConfig{
		Host:     defaultDBHost,
		Port:     defaultDBPort,
		Name:     defaultDBName,
		User:     defaultDBUser,
		Password: "test_password",
		SSLMode:  defaultDBSSLMode,
	}

	// DatabaseConfig 的所有字段都可以比较，因此两个结构体可以直接使用 !=。
	if databaseConfig != expected {
		t.Fatal("database config did not match the expected default values")
	}
}

// TestLoadDatabaseUsesEnvironment 验证环境变量能够覆盖全部默认值。
func TestLoadDatabaseUsesEnvironment(t *testing.T) {
	t.Setenv("DB_HOST", "database.example")
	t.Setenv("DB_PORT", "5544")
	t.Setenv("DB_NAME", "custom_database")
	t.Setenv("DB_USER", "custom_user")
	t.Setenv("DB_PASSWORD", "custom_password")
	t.Setenv("DB_SSLMODE", "require")

	databaseConfig, err := LoadDatabase()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	expected := DatabaseConfig{
		Host:     "database.example",
		Port:     5544,
		Name:     "custom_database",
		User:     "custom_user",
		Password: "custom_password",
		SSLMode:  "require",
	}

	if databaseConfig != expected {
		t.Fatal("database config did not match the expected environment values")
	}
}

// TestLoadDatabaseRequiresPassword 验证数据库密码不能为空。
func TestLoadDatabaseRequiresPassword(t *testing.T) {
	t.Setenv("DB_PORT", "")
	t.Setenv("DB_PASSWORD", "")

	_, err := LoadDatabase()
	if err == nil {
		t.Fatal("expected an error when DB_PASSWORD is empty")
	}
}

// TestLoadDatabaseRejectsInvalidPort 使用表驱动测试验证多种错误端口。
func TestLoadDatabaseRejectsInvalidPort(t *testing.T) {
	// 切片中的每一项代表一个独立测试场景。
	testCases := []struct {
		name string
		port string
	}{
		{
			name: "non-numeric port",
			port: "abc",
		},
		{
			name: "out-of-range port",
			port: "70000",
		},
	}

	// range 会依次取出切片中的每个测试场景。
	for _, testCase := range testCases {
		// t.Run 创建带名称的子测试，失败时能准确显示具体场景。
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("DB_PORT", testCase.port)
			t.Setenv("DB_PASSWORD", "test_password")

			_, err := LoadDatabase()
			if err == nil {
				t.Fatalf(
					"expected an error for DB_PORT %q",
					testCase.port,
				)
			}
		})
	}
}

// TestDatabaseConfigConnectionString 验证标准数据库配置生成正确地址。
func TestDatabaseConfigConnectionString(t *testing.T) {
	databaseConfig := DatabaseConfig{
		Host:     "localhost",
		Port:     5433,
		Name:     "rag_platform",
		User:     "rag_user",
		Password: "test_password",
		SSLMode:  "disable",
	}

	expected := "postgres://rag_user:test_password@localhost:5433/rag_platform?sslmode=disable"
	actual := databaseConfig.ConnectionString()

	if actual != expected {
		t.Fatalf("expected %q, got %q", expected, actual)
	}
}

// TestDatabaseConfigConnectionStringEscapesPassword 验证特殊字符密码会被安全编码，
// 并且可以正确还原。
func TestDatabaseConfigConnectionStringEscapesPassword(t *testing.T) {
	databaseConfig := DatabaseConfig{
		Host:     "localhost",
		Port:     5433,
		Name:     "rag_platform",
		User:     "rag_user",
		Password: "p@ss:word/with?symbols",
		SSLMode:  "disable",
	}

	connectionString := databaseConfig.ConnectionString()

	// 生成的 URL 不应该直接包含未编码的原始密码。
	if strings.Contains(connectionString, databaseConfig.Password) {
		t.Fatal("connection string contains the unescaped password")
	}

	// Parse 把字符串重新解析成 URL 结构，验证编码结果仍可还原。
	parsedURL, err := url.Parse(connectionString)
	if err != nil {
		t.Fatalf("expected a valid URL, got %v", err)
	}

	// Password 返回解码后的密码和一个表示密码是否存在的布尔值。
	decodedPassword, passwordExists := parsedURL.User.Password()
	if !passwordExists {
		t.Fatal("expected the connection string to contain a password")
	}

	if decodedPassword != databaseConfig.Password {
		t.Fatalf(
			"expected decoded password %q, got %q",
			databaseConfig.Password,
			decodedPassword,
		)
	}
}
