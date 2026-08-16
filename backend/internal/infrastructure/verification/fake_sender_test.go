package verification

import (
	"context"
	"testing"

	verificationapplication "rag-reasoning-platform/backend/internal/application/verification"
)

func TestFakeSenderStoresMessagesWithoutRemoteCalls(t *testing.T) {
	sender := NewFakeSender()
	message := verificationapplication.Message{
		ChallengeID: 42,
		Code:        "123456",
	}

	if err := sender.Send(context.Background(), message); err != nil {
		t.Fatalf("Send() error = %v, want nil", err)
	}

	messages := sender.Messages()
	if len(messages) != 1 || messages[0] != message {
		t.Fatalf("Messages() = %+v, want [%+v]", messages, message)
	}

	// 修改返回切片不能影响 Sender 内部保存的消息。
	messages[0].Code = "000000"
	if actual := sender.Messages()[0].Code; actual != "123456" {
		t.Fatalf("stored code = %q after caller mutation, want original", actual)
	}
}
