package answer

import (
	"context"
	"errors"
	"fmt"
	"math"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

const (
	// DefaultAnswerJobPage 是没有提供 page 时使用的默认页码。
	DefaultAnswerJobPage int64 = 1

	// DefaultAnswerJobPageSize 是没有提供 page_size 时使用的默认每页数量。
	DefaultAnswerJobPageSize int64 = 20

	// MaxAnswerJobPageSize 防止单次响应返回过多任务及完整答案快照。
	MaxAnswerJobPageSize int64 = 100
)

var (
	// ErrAnswerJobServiceDependencies 表示异步任务服务缺少持久化端口。
	ErrAnswerJobServiceDependencies = errors.New(
		"answer job service dependencies must be provided",
	)

	// ErrInvalidAnswerJobID 表示路径中的任务 ID 不是正整数。
	ErrInvalidAnswerJobID = errors.New("answer job ID must be positive")

	// ErrInvalidAnswerJobPage 表示任务列表页码无效或计算 offset 会溢出。
	ErrInvalidAnswerJobPage = errors.New("answer job page must be positive")

	// ErrInvalidAnswerJobPageSize 表示每页任务数量不在允许范围内。
	ErrInvalidAnswerJobPageSize = errors.New(
		"answer job page size must be between 1 and 100",
	)
)

// JobListInput 是任务列表用例接收的页码参数。
type JobListInput struct {
	Page     int64
	PageSize int64
}

// JobListOutput 是任务列表用例返回的当前页和分页元数据。
type JobListOutput struct {
	Jobs       []Job
	Page       int64
	PageSize   int64
	Total      int64
	TotalPages int64
}

// JobService 编排当前用户创建、查询和取消异步问答任务。
type JobService struct {
	jobs ScopedJobRepository
}

type ConversationQueueInput struct {
	ConversationID     int64
	UserMessageID      int64
	AssistantMessageID int64
	Input              Input
}

// NewJobService 创建异步问答任务应用服务。
func NewJobService(jobs ScopedJobRepository) (*JobService, error) {
	if jobs == nil {
		return nil, ErrAnswerJobServiceDependencies
	}
	return &JobService{jobs: jobs}, nil
}

// Queue 先完成零费用输入校验，再把规范化后的任务交给 Repository。
func (s *JobService) Queue(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input Input,
) (Job, error) {
	query, err := embeddingapplication.ValidateSemanticSearchInput(
		embeddingapplication.SemanticSearchInput{
			Query:      input.Query,
			DocumentID: input.DocumentID,
			TopK:       input.TopK,
		},
	)
	if err != nil {
		return Job{}, err
	}
	language, err := normalizeResponseLanguagePreference(input.ResponseLanguage)
	if err != nil {
		return Job{}, err
	}

	input.Query = query
	input.ResponseLanguage = language
	job, err := s.jobs.CreateAnswerJob(ctx, scope, input)
	if err != nil {
		return Job{}, fmt.Errorf("create answer job: %w", err)
	}
	return job, nil
}

// QueueConversation 把一个已经持久化的会话 turn 接入异步回答队列。
// 同一用户消息重复提交时直接返回已有任务，保持端到端幂等。
func (s *JobService) QueueConversation(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input ConversationQueueInput,
) (Job, error) {
	if input.ConversationID <= 0 || input.UserMessageID <= 0 || input.AssistantMessageID <= 0 {
		return Job{}, ErrInvalidAnswerJobID
	}

	existing, err := s.jobs.GetAnswerJobByUserMessageID(ctx, scope, input.UserMessageID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrAnswerJobNotFound) {
		return Job{}, fmt.Errorf("find conversation answer job: %w", err)
	}

	conversationID := input.ConversationID
	userMessageID := input.UserMessageID
	assistantMessageID := input.AssistantMessageID
	input.Input.ConversationID = &conversationID
	input.Input.UserMessageID = &userMessageID
	input.Input.AssistantMessageID = &assistantMessageID
	job, err := s.Queue(ctx, scope, input.Input)
	if errors.Is(err, ErrConversationAnswerActive) {
		// 并发请求可能在查询之后先创建任务；回读已存在任务即可。
		job, lookupErr := s.jobs.GetAnswerJobByUserMessageID(ctx, scope, input.UserMessageID)
		if lookupErr == nil {
			return job, nil
		}
		return Job{}, fmt.Errorf("resolve concurrent conversation answer job: %w", lookupErr)
	}
	return job, err
}

// GetByID 查询当前用户可见的任务快照。
func (s *JobService) GetByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (Job, error) {
	if jobID <= 0 {
		return Job{}, ErrInvalidAnswerJobID
	}
	job, err := s.jobs.GetAnswerJobByID(ctx, scope, jobID)
	if err != nil {
		return Job{}, fmt.Errorf("get answer job: %w", err)
	}
	return job, nil
}

// GetActiveConversationJob 查询当前用户会话仍处于 queued 或 processing 的回答任务。
func (s *JobService) GetActiveConversationJob(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (Job, error) {
	if conversationID <= 0 {
		return Job{}, ErrInvalidAnswerJobID
	}
	job, err := s.jobs.GetActiveAnswerJobByConversationID(ctx, scope, conversationID)
	if err != nil {
		return Job{}, fmt.Errorf("get active conversation answer job: %w", err)
	}
	return job, nil
}

// GetLatestConversationJob 查询当前用户会话保留期内最新的回答任务。
func (s *JobService) GetLatestConversationJob(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (Job, error) {
	if conversationID <= 0 {
		return Job{}, ErrInvalidAnswerJobID
	}
	job, err := s.jobs.GetLatestAnswerJobByConversationID(ctx, scope, conversationID)
	if err != nil {
		return Job{}, fmt.Errorf("get latest conversation answer job: %w", err)
	}
	return job, nil
}

// List 查询当前用户仍在保留期内的 standalone 异步问答任务。
func (s *JobService) List(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input JobListInput,
) (JobListOutput, error) {
	if input.Page <= 0 {
		return JobListOutput{}, ErrInvalidAnswerJobPage
	}
	if input.PageSize <= 0 || input.PageSize > MaxAnswerJobPageSize {
		return JobListOutput{}, ErrInvalidAnswerJobPageSize
	}
	if input.Page-1 > math.MaxInt64/input.PageSize {
		return JobListOutput{}, ErrInvalidAnswerJobPage
	}

	offset := (input.Page - 1) * input.PageSize
	result, err := s.jobs.ListAnswerJobs(
		ctx,
		scope,
		JobListOptions{Limit: input.PageSize, Offset: offset},
	)
	if err != nil {
		return JobListOutput{}, fmt.Errorf("list answer jobs: %w", err)
	}
	if result.Jobs == nil {
		result.Jobs = make([]Job, 0)
	}

	totalPages := result.Total / input.PageSize
	if result.Total%input.PageSize != 0 {
		totalPages++
	}
	return JobListOutput{
		Jobs:       result.Jobs,
		Page:       input.Page,
		PageSize:   input.PageSize,
		Total:      result.Total,
		TotalPages: totalPages,
	}, nil
}

// Cancel 原子取消 queued 任务；processing 和终态由 Repository 返回稳定错误。
func (s *JobService) Cancel(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (Job, error) {
	if jobID <= 0 {
		return Job{}, ErrInvalidAnswerJobID
	}
	job, err := s.jobs.CancelAnswerJob(ctx, scope, jobID)
	if err != nil {
		return Job{}, fmt.Errorf("cancel answer job: %w", err)
	}
	return job, nil
}
