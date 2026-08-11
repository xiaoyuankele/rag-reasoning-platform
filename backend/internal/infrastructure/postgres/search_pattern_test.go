package postgres

import "testing"

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
