package postgres

import (
	"reflect"
	"testing"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

func TestLiteralSubstringPattern(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "wraps ordinary keyword",
			query: "maglev",
			want:  "%maglev%",
		},
		{
			name:  "escapes percent and underscore",
			query: `%_`,
			want:  `%\%\_%`,
		},
		{
			name:  "escapes the escape character first",
			query: `path\document`,
			want:  `%path\\document%`,
		},
		{
			name:  "preserves Chinese keyword",
			query: `协同控制`,
			want:  `%协同控制%`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := literalSubstringPattern(test.query)
			if got != test.want {
				t.Fatalf(
					"literalSubstringPattern(%q) = %q, want %q",
					test.query,
					got,
					test.want,
				)
			}
		})
	}
}

func TestKeywordMatchClause(t *testing.T) {
	tests := []struct {
		name     string
		options  documentdomain.SearchOptions
		wantSQL  string
		wantArgs []any
	}{
		{
			name:     "legacy phrase",
			options:  documentdomain.SearchOptions{Query: "maglev"},
			wantSQL:  `(chunk.content ILIKE $2 ESCAPE E'\\')`,
			wantArgs: []any{"%maglev%"},
		},
		{
			name: "all terms",
			options: documentdomain.SearchOptions{
				Terms:    []string{"磁悬浮", "振动"},
				Operator: documentdomain.SearchOperatorAll,
			},
			wantSQL:  `(chunk.content ILIKE $3 ESCAPE E'\\' AND chunk.content ILIKE $4 ESCAPE E'\\')`,
			wantArgs: []any{"%磁悬浮%", "%振动%"},
		},
		{
			name: "any terms",
			options: documentdomain.SearchOptions{
				Terms:    []string{"control", "%_"},
				Operator: documentdomain.SearchOperatorAny,
			},
			wantSQL:  `(chunk.content ILIKE $1 ESCAPE E'\\' OR chunk.content ILIKE $2 ESCAPE E'\\')`,
			wantArgs: []any{"%control%", `%\%\_%`},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			firstPlaceholder := 1
			if test.name == "legacy phrase" {
				firstPlaceholder = 2
			} else if test.name == "all terms" {
				firstPlaceholder = 3
			}
			gotSQL, gotArgs, err := keywordMatchClause(test.options, firstPlaceholder)
			if err != nil {
				t.Fatalf("keywordMatchClause() error = %v", err)
			}
			if gotSQL != test.wantSQL || !reflect.DeepEqual(gotArgs, test.wantArgs) {
				t.Fatalf("keywordMatchClause() = (%q, %#v), want (%q, %#v)", gotSQL, gotArgs, test.wantSQL, test.wantArgs)
			}
		})
	}
}
