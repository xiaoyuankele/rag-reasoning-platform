package integration

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	infrastructure "rag-reasoning-platform/backend/internal/infrastructure"
)

// TestRedisCapacityCoordinationAcrossClients 验证两个独立 Go 客户端共享同一组
// Redis 全局/Owner 槽位，并验证异常进程遗留租约会按 TTL 自动回收。
func TestRedisCapacityCoordinationAcrossClients(t *testing.T) {
	if os.Getenv("RUN_REDIS_TESTS") != "1" {
		t.Skip("set RUN_REDIS_TESTS=1 to run Redis coordination integration tests")
	}

	address := strings.TrimSpace(os.Getenv("REDIS_TEST_ADDRESS"))
	if address == "" {
		address = "127.0.0.1:6381"
	}
	first := newTestRedisCapacityStore(t, address)
	second := newTestRedisCapacityStore(t, address)
	ctx := t.Context()
	namespace := fmt.Sprintf("integration-capacity-%d", time.Now().UnixNano())
	globalKey := "{" + namespace + "}:global"
	ownerOneKey := "{" + namespace + "}:owner:1"
	ownerTwoKey := "{" + namespace + "}:owner:2"
	limits := []int{2, 1}

	firstToken, _, acquired, err := first.AcquireCapacity(
		ctx,
		[]string{globalKey, ownerOneKey},
		limits,
		time.Minute,
	)
	if err != nil || !acquired {
		t.Fatalf("first AcquireCapacity() = (%q, %t, %v)", firstToken, acquired, err)
	}

	_, counts, acquired, err := second.AcquireCapacity(
		ctx,
		[]string{globalKey, ownerOneKey},
		limits,
		time.Minute,
	)
	if err != nil || acquired || len(counts) != 2 || counts[0] != 1 || counts[1] != 1 {
		t.Fatalf("same-owner AcquireCapacity() = (%v, %t, %v)", counts, acquired, err)
	}

	secondToken, _, acquired, err := second.AcquireCapacity(
		ctx,
		[]string{globalKey, ownerTwoKey},
		limits,
		time.Minute,
	)
	if err != nil || !acquired {
		t.Fatalf("second owner AcquireCapacity() = (%q, %t, %v)", secondToken, acquired, err)
	}

	_, counts, acquired, err = second.AcquireCapacity(
		ctx,
		[]string{globalKey, "{" + namespace + "}:owner:3"},
		limits,
		time.Minute,
	)
	if err != nil || acquired || len(counts) != 2 || counts[0] != 2 {
		t.Fatalf("global-full AcquireCapacity() = (%v, %t, %v)", counts, acquired, err)
	}

	if err := first.ReleaseCapacity(
		ctx,
		[]string{globalKey, ownerOneKey},
		firstToken,
	); err != nil {
		t.Fatalf("ReleaseCapacity(first) error = %v", err)
	}
	if err := second.ReleaseCapacity(
		ctx,
		[]string{globalKey, ownerTwoKey},
		secondToken,
	); err != nil {
		t.Fatalf("ReleaseCapacity(second) error = %v", err)
	}

	ttlKey := "{" + namespace + "}:ttl"
	_, _, acquired, err = first.AcquireCapacity(
		ctx,
		[]string{ttlKey},
		[]int{1},
		40*time.Millisecond,
	)
	if err != nil || !acquired {
		t.Fatalf("TTL first acquire = (%t, %v)", acquired, err)
	}
	time.Sleep(80 * time.Millisecond)
	ttlToken, _, acquired, err := second.AcquireCapacity(
		ctx,
		[]string{ttlKey},
		[]int{1},
		time.Minute,
	)
	if err != nil || !acquired {
		t.Fatalf("TTL recovery acquire = (%t, %v)", acquired, err)
	}
	if err := second.ReleaseCapacity(ctx, []string{ttlKey}, ttlToken); err != nil {
		t.Fatalf("release recovered TTL lease: %v", err)
	}
}

func newTestRedisCapacityStore(
	t *testing.T,
	address string,
) *infrastructure.RedisCapacityStore {
	t.Helper()
	store, err := infrastructure.NewRedisCapacityStore(
		infrastructure.RedisCapacityOptions{
			Address:          address,
			OperationTimeout: time.Second,
		},
	)
	if err != nil {
		t.Fatalf("NewRedisCapacityStore() error = %v", err)
	}
	if err := store.Ping(t.Context()); err != nil {
		_ = store.Close()
		t.Fatalf("Ping Redis capacity store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close Redis capacity store: %v", err)
		}
	})
	return store
}
