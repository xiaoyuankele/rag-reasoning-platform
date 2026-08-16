package verification

import (
	"context"
	"sync"

	verificationapplication "rag-reasoning-platform/backend/internal/application/verification"
)

// FakeSender 是默认零远程费用测试使用的内存发送器。
// 它不访问邮件或短信服务，也不会把验证码打印到日志。
type FakeSender struct {
	mutex    sync.Mutex
	messages []verificationapplication.Message
}

var _ verificationapplication.Sender = (*FakeSender)(nil)

// NewFakeSender 创建一个空的内存发送器。
func NewFakeSender() *FakeSender {
	return &FakeSender{
		messages: make([]verificationapplication.Message, 0),
	}
}

// Send 把消息保存到进程内存，供自动化测试核对。
func (s *FakeSender) Send(
	_ context.Context,
	message verificationapplication.Message,
) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.messages = append(s.messages, message)
	return nil
}

// Messages 返回消息切片副本，防止测试调用方修改 Sender 内部状态。
func (s *FakeSender) Messages() []verificationapplication.Message {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	return append([]verificationapplication.Message(nil), s.messages...)
}
