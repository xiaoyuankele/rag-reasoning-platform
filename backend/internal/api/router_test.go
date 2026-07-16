package api

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealthEndpoint 测试GET/health 接口
// Go 会把以Test开头，并接收 *testing.T 参数的函数识别为测试函数
func TestHealthEndpoint(t *testing.T) {
	//切换为测试模式
	gin.SetMode(gin.TestMode)

	//创建项目真实使用的Gin路由
	//同属于api包直接调用
	router := NewRouter()

	//httptest.NewRequest 在内存创建一个HTTP请求
	//三个参数：请求方法、请求地址、请求体
	//健康检查不需要请求体，第三个参数使用nil
	request := httptest.NewRequest(http.MethodGet, "/health", nil)

	//NewRecorder 创建一个响应记录器 记录接口返回的状态码，响应头和响应体
	response := httptest.NewRecorder()

	//让Gin处理虚拟请求，并把结果写入reponse
	router.ServeHTTP(response, request)

	//检查响应状态码是否为200
	if response.Code != http.StatusOK {
		//Fatalf 报告错误，并立即终止当前测试 %d整数占位符
		t.Fatalf("expected status code %d,got %d", http.StatusOK, response.Code)
	}

	//反引号表示原生字符串，内容不需要转义双引号
	expectedBody := `{"status":"ok"}`
	//检查接口实际返回的JSON是否符合预期
	if response.Body.String() != expectedBody {
		t.Fatalf("expected body %s, got %s", expectedBody, response.Body.String())
	}
}
