package config

import "testing"

// TestLoadUsesDefaultPort 验证未配置 APP_PORT 时使用默认端口。
func TestLoadUsesDefaultPort(t *testing.T) {
	// t.Setenv 只在当前测试期间设置环境变量；测试结束后会自动恢复。
	t.Setenv("APP_PORT", "")
	t.Setenv("APP_ROLE", "")

	config, err := Load()
	// 配置读取成功时不应返回错误。
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// 验证读取到的端口等于默认端口。
	if config.Port != defaultPort {
		t.Fatalf(
			"expected default port %d, got %d",
			defaultPort,
			config.Port,
		)
	}
	if config.Role != ApplicationRoleAll {
		t.Fatalf("expected default role %q, got %q", ApplicationRoleAll, config.Role)
	}

	// 验证默认端口能够转换成 Gin 需要的监听地址。
	if config.ServerAddress() != ":8080" {
		t.Fatalf(
			"expected server address :8080, got %s",
			config.ServerAddress(),
		)
	}
}

// TestLoadUsesEnvironmentPort 验证有效的环境变量能够覆盖默认端口。
func TestLoadUsesEnvironmentPort(t *testing.T) {
	t.Setenv("APP_PORT", "9090")
	t.Setenv("APP_ROLE", "api")

	config, err := Load()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if config.Port != 9090 {
		t.Fatalf(
			"expected port 9090, got %d", config.Port,
		)
	}
	if config.Role != ApplicationRoleAPI {
		t.Fatalf("expected role %q, got %q", ApplicationRoleAPI, config.Role)
	}

	if config.ServerAddress() != ":9090" {
		t.Fatalf(
			"expected server address :9090, got %s",
			config.ServerAddress(),
		)
	}
}

// TestLoadRejectsNonNumericPort 验证非数字端口会被拒绝。
func TestLoadRejectsNonNumericPort(t *testing.T) {
	t.Setenv("APP_PORT", "abc")
	t.Setenv("APP_ROLE", "")

	_, err := Load()

	if err == nil {
		t.Fatal("expected an error for a non-numeric port")
	}
}

// TestLoadRejectsOutOfRangePort 验证超出 TCP 范围的端口会被拒绝。
func TestLoadRejectsOutOfRangePort(t *testing.T) {
	t.Setenv("APP_PORT", "70000")
	t.Setenv("APP_ROLE", "")

	_, err := Load()

	if err == nil {
		t.Fatal("expected an error for an out-of-range port")
	}
}
