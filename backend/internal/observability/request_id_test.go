package observability

import (
	"context"
	"testing"
)

// TestRequestIDContextRoundTrip 验证请求 ID 写入父 context 后，后续业务拿到的
// 子 context 仍然可以读取同一个值。
func TestRequestIDContextRoundTrip(t *testing.T) {
	parentContext := WithRequestID(context.Background(), "request-123")
	childContext, cancel := context.WithCancel(parentContext)
	defer cancel()

	requestID, ok := RequestIDFromContext(childContext)
	if !ok {
		t.Fatal("RequestIDFromContext() ok = false, want true")
	}
	if requestID != "request-123" {
		t.Fatalf("RequestIDFromContext() = %q, want %q", requestID, "request-123")
	}
}

// TestRequestIDFromContextRejectsMissingValue 验证普通 context 不会伪造请求 ID。
func TestRequestIDFromContextRejectsMissingValue(t *testing.T) {
	requestID, ok := RequestIDFromContext(context.Background())
	if ok {
		t.Fatalf("RequestIDFromContext() = (%q, true), want missing value", requestID)
	}
}
