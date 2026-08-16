package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"rag-reasoning-platform/backend/internal/api"
	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	authapplication "rag-reasoning-platform/backend/internal/application/auth"
	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	generationdomain "rag-reasoning-platform/backend/internal/domain/generation"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	sessioninfrastructure "rag-reasoning-platform/backend/internal/infrastructure/session"
	"rag-reasoning-platform/backend/internal/observability"
)

const semanticOwnerIntegrationDimensions = 1536
const semanticOwnerIntegrationModel = "owner-integration-model"

// fixedOwnerIntegrationEmbedder 为权限集成测试提供固定查询向量，
// 不访问任何远程 Embedding API。
type fixedOwnerIntegrationEmbedder struct {
	vector []float32
	calls  int
}

func (e *fixedOwnerIntegrationEmbedder) Embed(
	_ context.Context,
	request embeddingdomain.EmbedRequest,
) (embeddingdomain.EmbedResult, error) {
	e.calls++
	if len(request.Inputs) != 1 ||
		request.Model != semanticOwnerIntegrationModel ||
		request.Dimensions != semanticOwnerIntegrationDimensions {
		return embeddingdomain.EmbedResult{}, fmt.Errorf(
			"unexpected owner integration embed request: %+v",
			request,
		)
	}
	return embeddingdomain.EmbedResult{
		Vectors: [][]float32{append([]float32(nil), e.vector...)},
	}, nil
}

// fixedOwnerIntegrationGenerator 返回固定回答，证明 AnswerService 只能消费
// 已经按 OwnerScope 隔离的语义证据，并且不会产生远程生成费用。
type fixedOwnerIntegrationGenerator struct {
	calls int
}

func (g *fixedOwnerIntegrationGenerator) Generate(
	_ context.Context,
	_ generationdomain.GenerateRequest,
) (generationdomain.GenerateResult, error) {
	g.calls++
	return generationdomain.GenerateResult{
		Text:             "scoped answer [1]",
		PromptTokens:     10,
		CompletionTokens: 4,
		TotalTokens:      14,
	}, nil
}

// TestSemanticAndAnswerOwnerHTTPWithPostgreSQL 验证语义检索和问答来源
// 都不能越过关联文档的所有者边界。
func TestSemanticAndAnswerOwnerHTTPWithPostgreSQL(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openDocumentOwnerIntegrationPool(t, ctx)
	if err := database.RefreshVectorTypes(ctx, pool); err != nil {
		t.Fatalf("refresh pgvector types for owner HTTP test: %v", err)
	}

	uniqueSuffix := time.Now().UnixNano()
	ownerAID := insertDocumentOwnerIntegrationUser(
		t,
		ctx,
		pool,
		fmt.Sprintf("semantic-http-a-%d@example.com", uniqueSuffix),
	)
	ownerBID := insertDocumentOwnerIntegrationUser(
		t,
		ctx,
		pool,
		fmt.Sprintf("semantic-http-b-%d@example.com", uniqueSuffix),
	)
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(
			cleanupContext,
			"DELETE FROM documents WHERE owner_user_id = ANY($1::bigint[])",
			[]int64{ownerAID, ownerBID},
		)
		_, _ = pool.Exec(
			cleanupContext,
			"DELETE FROM users WHERE id = ANY($1::bigint[])",
			[]int64{ownerAID, ownerBID},
		)
	})

	ownerA, err := accessdomain.NewOwnerScope(ownerAID)
	if err != nil {
		t.Fatalf("create owner A scope: %v", err)
	}
	ownerB, err := accessdomain.NewOwnerScope(ownerBID)
	if err != nil {
		t.Fatalf("create owner B scope: %v", err)
	}
	queryVector := semanticOwnerIntegrationVector(1, 0)
	ownerADocument := createSemanticOwnerIntegrationDocument(
		t,
		ctx,
		pool,
		ownerA,
		fmt.Sprintf("semantic-owner-a-%d", uniqueSuffix),
		"a",
		"owner A private semantic evidence",
		queryVector,
	)
	ownerBDocument := createSemanticOwnerIntegrationDocument(
		t,
		ctx,
		pool,
		ownerB,
		fmt.Sprintf("semantic-owner-b-%d", uniqueSuffix),
		"b",
		"owner B private semantic evidence",
		queryVector,
	)

	sessionRepository := postgresrepository.NewAuthSessionRepository(pool)
	tokenManager := sessioninfrastructure.NewTokenGenerator()
	ownerACookie := createDocumentOwnerIntegrationSession(
		t, ctx, sessionRepository, tokenManager, ownerAID,
	)
	ownerBCookie := createDocumentOwnerIntegrationSession(
		t, ctx, sessionRepository, tokenManager, ownerBID,
	)
	sessionService, err := authapplication.NewSessionService(
		sessionRepository,
		tokenManager,
		time.Now,
	)
	if err != nil {
		t.Fatalf("create session service: %v", err)
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	embedder := &fixedOwnerIntegrationEmbedder{vector: queryVector}
	scopedChunks := postgresrepository.NewScopedChunkRepository(pool)
	semanticService, err := embeddingapplication.NewSemanticSearchService(
		embedder,
		scopedChunks,
		semanticOwnerIntegrationModel,
		semanticOwnerIntegrationDimensions,
	)
	if err != nil {
		t.Fatalf("create scoped semantic service: %v", err)
	}
	generator := &fixedOwnerIntegrationGenerator{}
	answerService, err := answerapplication.NewService(
		semanticService,
		generator,
		observability.NewGenerationCallLogger(logger),
		"owner-integration-generation-model",
		128,
		0,
	)
	if err != nil {
		t.Fatalf("create scoped answer service: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := api.NewRouter(logger)
	protectedRoutes := router.Group("")
	protectedRoutes.Use(api.NewAuthMiddleware(sessionService, logger).Require)
	api.NewSemanticSearchHandler(semanticService).RegisterRoutes(protectedRoutes)
	api.NewAnswerHandler(answerService).RegisterRoutes(protectedRoutes)

	const semanticBody = `{"query":"shared semantic question","top_k":5}`
	unauthenticatedSemantic := performDocumentOwnerRequest(
		router,
		http.MethodPost,
		"/semantic-search",
		strings.NewReader(semanticBody),
		"",
		"application/json",
	)
	assertProcessingOwnerStatus(
		t,
		unauthenticatedSemantic.Code,
		http.StatusUnauthorized,
		unauthenticatedSemantic.Body.String(),
	)
	if embedder.calls != 0 {
		t.Fatalf("embedder calls after unauthenticated search=%d want=0", embedder.calls)
	}

	ownerASemantic := performDocumentOwnerRequest(
		router,
		http.MethodPost,
		"/semantic-search",
		strings.NewReader(semanticBody),
		ownerACookie,
		"application/json",
	)
	assertSemanticAnswerOwnerSource(
		t,
		ownerASemantic.Code,
		ownerASemantic.Body.Bytes(),
		"hits",
		ownerADocument.ID,
		"owner A",
	)
	ownerBSemantic := performDocumentOwnerRequest(
		router,
		http.MethodPost,
		"/semantic-search",
		strings.NewReader(semanticBody),
		ownerBCookie,
		"application/json",
	)
	assertSemanticAnswerOwnerSource(
		t,
		ownerBSemantic.Code,
		ownerBSemantic.Body.Bytes(),
		"hits",
		ownerBDocument.ID,
		"owner B",
	)

	foreignSemanticBody := fmt.Sprintf(
		`{"query":"private question","document_id":%d,"top_k":5}`,
		ownerADocument.ID,
	)
	embedCallsBeforeForeign := embedder.calls
	ownerBForeignSemantic := performDocumentOwnerRequest(
		router,
		http.MethodPost,
		"/semantic-search",
		strings.NewReader(foreignSemanticBody),
		ownerBCookie,
		"application/json",
	)
	assertProcessingOwnerStatus(
		t,
		ownerBForeignSemantic.Code,
		http.StatusNotFound,
		ownerBForeignSemantic.Body.String(),
	)
	if embedder.calls != embedCallsBeforeForeign {
		t.Fatal("foreign document semantic search called Embedder before owner rejection")
	}

	const answerBody = `{"query":"shared semantic question","top_k":5,"response_language":"en"}`
	unauthenticatedAnswer := performDocumentOwnerRequest(
		router,
		http.MethodPost,
		"/answers",
		strings.NewReader(answerBody),
		"",
		"application/json",
	)
	assertProcessingOwnerStatus(
		t,
		unauthenticatedAnswer.Code,
		http.StatusUnauthorized,
		unauthenticatedAnswer.Body.String(),
	)
	if generator.calls != 0 {
		t.Fatalf("generator calls after unauthenticated answer=%d want=0", generator.calls)
	}

	ownerAAnswer := performDocumentOwnerRequest(
		router,
		http.MethodPost,
		"/answers",
		strings.NewReader(answerBody),
		ownerACookie,
		"application/json",
	)
	assertSemanticAnswerOwnerSource(
		t,
		ownerAAnswer.Code,
		ownerAAnswer.Body.Bytes(),
		"sources",
		ownerADocument.ID,
		"",
	)
	ownerBAnswer := performDocumentOwnerRequest(
		router,
		http.MethodPost,
		"/answers",
		strings.NewReader(answerBody),
		ownerBCookie,
		"application/json",
	)
	assertSemanticAnswerOwnerSource(
		t,
		ownerBAnswer.Code,
		ownerBAnswer.Body.Bytes(),
		"sources",
		ownerBDocument.ID,
		"",
	)

	foreignAnswerBody := fmt.Sprintf(
		`{"query":"private question","document_id":%d,"top_k":5}`,
		ownerADocument.ID,
	)
	embedCallsBeforeForeignAnswer := embedder.calls
	generationCallsBeforeForeignAnswer := generator.calls
	ownerBForeignAnswer := performDocumentOwnerRequest(
		router,
		http.MethodPost,
		"/answers",
		strings.NewReader(foreignAnswerBody),
		ownerBCookie,
		"application/json",
	)
	assertProcessingOwnerStatus(
		t,
		ownerBForeignAnswer.Code,
		http.StatusNotFound,
		ownerBForeignAnswer.Body.String(),
	)
	if embedder.calls != embedCallsBeforeForeignAnswer ||
		generator.calls != generationCallsBeforeForeignAnswer {
		t.Fatal("foreign document answer called a remote-capable provider before owner rejection")
	}
}

// createSemanticOwnerIntegrationDocument 创建 ready 文档、文本块、成功向量任务
// 和一条真实 pgvector 记录，供完整 HTTP 权限链路使用。
func createSemanticOwnerIntegrationDocument(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	scope accessdomain.OwnerScope,
	name string,
	hashCharacter string,
	content string,
	vector []float32,
) documentdomain.Document {
	t.Helper()
	documents := postgresrepository.NewScopedDocumentRepository(pool)
	document, err := documents.Create(
		ctx,
		scope,
		documentdomain.CreateInput{
			OriginalName: name + ".md",
			StoragePath:  "documents/" + name + ".md",
			MIMEType:     "text/markdown",
			SizeBytes:    int64(len(content)),
			SHA256:       strings.Repeat(hashCharacter, 64),
		},
	)
	if err != nil {
		t.Fatalf("create semantic owner document %q: %v", name, err)
	}

	chunks := postgresrepository.NewChunkRepository(pool)
	if err := chunks.ReplaceForDocument(
		ctx,
		document.ID,
		[]documentdomain.ChunkInput{{Index: 0, Content: content}},
	); err != nil {
		t.Fatalf("replace semantic owner chunks %q: %v", name, err)
	}
	persistedChunks, err := chunks.ListByDocumentID(ctx, document.ID)
	if err != nil || len(persistedChunks) != 1 {
		t.Fatalf(
			"list semantic owner chunks %q = (%+v, %v), want one",
			name,
			persistedChunks,
			err,
		)
	}
	if _, err := pool.Exec(
		ctx,
		"UPDATE documents SET status = 'ready', title = $2 WHERE id = $1",
		document.ID,
		name+" title",
	); err != nil {
		t.Fatalf("mark semantic owner document %q ready: %v", name, err)
	}

	jobs := postgresrepository.NewEmbeddingJobRepository(pool)
	job, err := jobs.CreateEmbeddingJob(
		ctx,
		document.ID,
		semanticOwnerIntegrationModel,
		semanticOwnerIntegrationDimensions,
	)
	if err != nil {
		t.Fatalf("create semantic owner embedding job %q: %v", name, err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE embedding_jobs
		 SET status = 'processing', started_at = CURRENT_TIMESTAMP
		 WHERE id = $1`,
		job.ID,
	); err != nil {
		t.Fatalf("mark semantic owner embedding job %q processing: %v", name, err)
	}
	if err := jobs.MarkEmbeddingJobSucceeded(
		ctx,
		job.ID,
		embeddingdomain.JobCompletion{
			Vectors: []embeddingdomain.ChunkVector{{
				ChunkID: persistedChunks[0].ID,
				Values:  vector,
			}},
			PromptTokens: 1,
			TotalTokens:  1,
		},
	); err != nil {
		t.Fatalf("complete semantic owner embedding job %q: %v", name, err)
	}

	return document
}

// semanticOwnerIntegrationVector 创建数据库固定维度的测试向量。
func semanticOwnerIntegrationVector(first float32, second float32) []float32 {
	values := make([]float32, semanticOwnerIntegrationDimensions)
	values[0] = first
	values[1] = second
	return values
}

// assertSemanticAnswerOwnerSource 核对 hits 或 sources 只包含预期文档。
func assertSemanticAnswerOwnerSource(
	t *testing.T,
	status int,
	body []byte,
	field string,
	wantedDocumentID int64,
	wantedContentMarker string,
) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("%s status=%d want=200 body=%s", field, status, string(body))
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode %s response: %v", field, err)
	}
	var sources []struct {
		DocumentID int64  `json:"document_id"`
		Content    string `json:"content"`
	}
	if err := json.Unmarshal(payload[field], &sources); err != nil {
		t.Fatalf("decode %s list: %v body=%s", field, err, string(body))
	}
	if len(sources) != 1 || sources[0].DocumentID != wantedDocumentID {
		t.Fatalf("%s=%+v want only document %d", field, sources, wantedDocumentID)
	}
	if wantedContentMarker != "" &&
		!strings.Contains(sources[0].Content, wantedContentMarker) {
		t.Fatalf("%s content=%q want marker %q", field, sources[0].Content, wantedContentMarker)
	}
}
