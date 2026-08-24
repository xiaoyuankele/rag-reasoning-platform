package answer

import (
	"context"
	"sync"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

// answerAdmissionScheduler 是单进程内 Owner 公平调度器。
//
// owners 保存每个用户的执行数和 FIFO 等待队列；ownerOrder 只保存当前存在
// waiter 的 Owner，并在每次分配后轮转到尾部，从而实现 Owner 之间的轮询公平。
type answerAdmissionScheduler struct {
	mu         sync.Mutex
	limits     AnswerAdmissionLimits
	owners     map[int64]*answerOwnerAdmissionState
	ownerOrder []int64
	inFlight   int
	waiting    int
}

type answerOwnerAdmissionState struct {
	inFlight int
	waiters  []*answerAdmissionWaiter
	inOrder  bool
}

// answerAdmissionWaiter 对应一个正在同步等待槽位的 HTTP 请求。
// admitted 和 snapshot 只在 scheduler.mu 保护下写入；关闭 ready 后等待方可安全读取。
type answerAdmissionWaiter struct {
	ready    chan struct{}
	admitted bool
	snapshot answerAdmissionSnapshot
}

type answerAdmissionSnapshot struct {
	inFlight      int
	ownerInFlight int
	waiting       int
	ownerWaiting  int
}

type answerAdmissionDecision struct {
	snapshot answerAdmissionSnapshot
	outcome  AnswerAdmissionOutcome
	err      error
}

func newAnswerAdmissionScheduler(limits AnswerAdmissionLimits) *answerAdmissionScheduler {
	return &answerAdmissionScheduler{
		limits:     limits,
		owners:     make(map[int64]*answerOwnerAdmissionState),
		ownerOrder: make([]int64, 0),
	}
}

func (s *answerAdmissionScheduler) acquire(
	ctx context.Context,
	scope accessdomain.OwnerScope,
) answerAdmissionDecision {
	if err := ctx.Err(); err != nil {
		return answerAdmissionDecision{
			snapshot: s.snapshot(scope.OwnerUserID()),
			outcome:  AnswerAdmissionOutcomeCanceled,
			err:      err,
		}
	}
	if !scope.IsValid() {
		return answerAdmissionDecision{err: accessdomain.ErrInvalidOwnerScope}
	}

	ownerID := scope.OwnerUserID()
	s.mu.Lock()
	state := s.ownerStateLocked(ownerID)

	// 先分配此前已经排队且当前具备资格的 Owner，防止新请求绕过旧 waiter。
	s.dispatchLocked()
	if s.canAdmitLocked(state) && !s.hasEligibleWaiterLocked() {
		s.inFlight++
		state.inFlight++
		snapshot := s.snapshotLocked(state)
		s.mu.Unlock()
		return answerAdmissionDecision{snapshot: snapshot}
	}

	if len(state.waiters) >= s.limits.MaxWaitersPerOwner {
		snapshot := s.snapshotLocked(state)
		s.mu.Unlock()
		return answerAdmissionDecision{
			snapshot: snapshot,
			outcome:  AnswerAdmissionOutcomeOwnerCapacity,
			err:      ErrAnswerOwnerCapacityExhausted,
		}
	}
	if s.waiting >= s.limits.MaxWaitersGlobal {
		snapshot := s.snapshotLocked(state)
		s.mu.Unlock()
		return answerAdmissionDecision{
			snapshot: snapshot,
			outcome:  AnswerAdmissionOutcomeGlobalCapacity,
			err:      ErrAnswerCapacityExhausted,
		}
	}

	waiter := &answerAdmissionWaiter{ready: make(chan struct{})}
	state.waiters = append(state.waiters, waiter)
	s.waiting++
	if !state.inOrder {
		state.inOrder = true
		s.ownerOrder = append(s.ownerOrder, ownerID)
	}
	s.dispatchLocked()
	if waiter.admitted {
		snapshot := waiter.snapshot
		s.mu.Unlock()
		return answerAdmissionDecision{snapshot: snapshot}
	}
	s.mu.Unlock()

	timer := time.NewTimer(s.limits.WaitTimeout)
	defer timer.Stop()

	select {
	case <-waiter.ready:
		return answerAdmissionDecision{snapshot: waiter.snapshot}
	case <-ctx.Done():
		return s.cancelWaiter(ownerID, waiter, AnswerAdmissionOutcomeCanceled, ctx.Err())
	case <-timer.C:
		return s.cancelWaiter(
			ownerID,
			waiter,
			AnswerAdmissionOutcomeCapacityTimeout,
			ErrAnswerCapacityExhausted,
		)
	}
}

// release 归还一个 Owner 的执行槽位，并立即尝试唤醒下一批公平 waiter。
func (s *answerAdmissionScheduler) release(
	scope accessdomain.OwnerScope,
) answerAdmissionSnapshot {
	ownerID := scope.OwnerUserID()
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.owners[ownerID]
	if state != nil && state.inFlight > 0 && s.inFlight > 0 {
		state.inFlight--
		s.inFlight--
	}
	s.dispatchLocked()
	s.cleanupOwnerLocked(ownerID, state)
	return s.snapshotLocked(s.owners[ownerID])
}

func (s *answerAdmissionScheduler) cancelWaiter(
	ownerID int64,
	waiter *answerAdmissionWaiter,
	outcome AnswerAdmissionOutcome,
	err error,
) answerAdmissionDecision {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.owners[ownerID]
	// 分配和超时可能在 select 边界同时发生。只要调度器已经完成分配，
	// 就必须让调用方执行并最终 release，不能把已占用槽位泄漏掉。
	if waiter.admitted {
		return answerAdmissionDecision{snapshot: waiter.snapshot}
	}

	if state != nil {
		for index, queued := range state.waiters {
			if queued != waiter {
				continue
			}
			state.waiters = append(state.waiters[:index], state.waiters[index+1:]...)
			s.waiting--
			break
		}
		if len(state.waiters) == 0 && state.inOrder {
			s.removeOwnerFromOrderLocked(ownerID)
			state.inOrder = false
		}
	}
	s.dispatchLocked()
	snapshot := s.snapshotLocked(state)
	s.cleanupOwnerLocked(ownerID, state)
	return answerAdmissionDecision{
		snapshot: snapshot,
		outcome:  outcome,
		err:      err,
	}
}

func (s *answerAdmissionScheduler) dispatchLocked() {
	for s.inFlight < s.limits.MaxConcurrencyGlobal && len(s.ownerOrder) > 0 {
		admitted := false
		ownersToCheck := len(s.ownerOrder)
		for checked := 0; checked < ownersToCheck && len(s.ownerOrder) > 0; checked++ {
			ownerID := s.ownerOrder[0]
			s.ownerOrder = s.ownerOrder[1:]
			state := s.owners[ownerID]
			if state == nil || len(state.waiters) == 0 {
				if state != nil {
					state.inOrder = false
					s.cleanupOwnerLocked(ownerID, state)
				}
				continue
			}

			// 先轮转到队尾。即使当前 Owner 已达到执行上限，其他 Owner
			// 仍可在本轮继续检查，不会发生队头阻塞。
			s.ownerOrder = append(s.ownerOrder, ownerID)
			if state.inFlight >= s.limits.MaxConcurrencyPerOwner {
				continue
			}

			waiter := state.waiters[0]
			state.waiters = state.waiters[1:]
			s.waiting--
			s.inFlight++
			state.inFlight++
			if len(state.waiters) == 0 {
				s.ownerOrder = s.ownerOrder[:len(s.ownerOrder)-1]
				state.inOrder = false
			}

			waiter.admitted = true
			waiter.snapshot = s.snapshotLocked(state)
			close(waiter.ready)
			admitted = true
			break
		}
		if !admitted {
			return
		}
	}
}

func (s *answerAdmissionScheduler) hasEligibleWaiterLocked() bool {
	for _, ownerID := range s.ownerOrder {
		state := s.owners[ownerID]
		if state != nil && len(state.waiters) > 0 &&
			state.inFlight < s.limits.MaxConcurrencyPerOwner {
			return true
		}
	}
	return false
}

func (s *answerAdmissionScheduler) canAdmitLocked(state *answerOwnerAdmissionState) bool {
	return s.inFlight < s.limits.MaxConcurrencyGlobal &&
		state.inFlight < s.limits.MaxConcurrencyPerOwner
}

func (s *answerAdmissionScheduler) ownerStateLocked(
	ownerID int64,
) *answerOwnerAdmissionState {
	state := s.owners[ownerID]
	if state == nil {
		state = &answerOwnerAdmissionState{waiters: make([]*answerAdmissionWaiter, 0)}
		s.owners[ownerID] = state
	}
	return state
}

func (s *answerAdmissionScheduler) snapshot(ownerID int64) answerAdmissionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(s.owners[ownerID])
}

func (s *answerAdmissionScheduler) snapshotLocked(
	state *answerOwnerAdmissionState,
) answerAdmissionSnapshot {
	snapshot := answerAdmissionSnapshot{
		inFlight: s.inFlight,
		waiting:  s.waiting,
	}
	if state != nil {
		snapshot.ownerInFlight = state.inFlight
		snapshot.ownerWaiting = len(state.waiters)
	}
	return snapshot
}

func (s *answerAdmissionScheduler) removeOwnerFromOrderLocked(ownerID int64) {
	for index, queuedOwnerID := range s.ownerOrder {
		if queuedOwnerID == ownerID {
			s.ownerOrder = append(s.ownerOrder[:index], s.ownerOrder[index+1:]...)
			return
		}
	}
}

func (s *answerAdmissionScheduler) cleanupOwnerLocked(
	ownerID int64,
	state *answerOwnerAdmissionState,
) {
	if state != nil && state.inFlight == 0 && len(state.waiters) == 0 && !state.inOrder {
		delete(s.owners, ownerID)
	}
}
