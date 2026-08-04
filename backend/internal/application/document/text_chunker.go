package document

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

const defaultTextChunkRuneLimit = 1000

var (
	// ErrEmptyTextDocument 表示文件中没有可以持久化的有效文本。
	ErrEmptyTextDocument = errors.New("text document is empty")

	// errInvalidTextChunkRuneLimit 表示内部传入的分块上限不合法。
	errInvalidTextChunkRuneLimit = errors.New(
		"text chunk rune limit must be positive",
	)
)

// chunkText 流式读取 UTF-8 文本，并生成大小受控的规范化文本块。
//
// 连续空白会被折叠为一个普通空格；每块按 Unicode 字符数限制大小，
// 避免按照字节切分时破坏中文等多字节字符。
func chunkText(
	ctx context.Context,
	reader io.Reader,
	maxRunes int,
) ([]documentdomain.ChunkInput, error) {
	if maxRunes <= 0 {
		return nil, errInvalidTextChunkRuneLimit
	}

	bufferedReader := bufio.NewReader(reader)
	chunks := make([]documentdomain.ChunkInput, 0)
	var content strings.Builder
	contentRunes := 0
	pendingSpace := false
	firstRune := true

	flush := func() {
		if contentRunes == 0 {
			return
		}

		chunks = append(chunks, documentdomain.ChunkInput{
			Index:   len(chunks),
			Content: content.String(),
		})
		content.Reset()
		contentRunes = 0
		pendingSpace = false
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("chunk text document: %w", err)
		}

		currentRune, size, err := bufferedReader.ReadRune()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read text document: %w", err)
		}
		if currentRune == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf(
				"chunk text document: %w",
				ErrInvalidTextContent,
			)
		}

		// UTF-8 BOM 只可能出现在文本开头，不属于正文内容。
		if firstRune && currentRune == '\uFEFF' {
			firstRune = false
			continue
		}
		firstRune = false

		if unicode.IsSpace(currentRune) {
			if contentRunes > 0 {
				pendingSpace = true
			}
			continue
		}

		if pendingSpace && contentRunes > 0 {
			if contentRunes+1 >= maxRunes {
				flush()
			} else {
				content.WriteByte(' ')
				contentRunes++
			}
		}
		pendingSpace = false

		if contentRunes >= maxRunes {
			flush()
		}

		content.WriteRune(currentRune)
		contentRunes++
	}

	flush()
	if len(chunks) == 0 {
		return nil, ErrEmptyTextDocument
	}

	return chunks, nil
}
