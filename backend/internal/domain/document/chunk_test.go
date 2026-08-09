package document

import "testing"

func TestChunkInputHasValidPageRange(t *testing.T) {
	tests := []struct {
		name string

		// 指针同时表达“没有页码（nil）”和“提供了具体页码”。
		pageStart *int
		pageEnd   *int
		want      bool
	}{
		{
			name:      "both start and end are nil",
			pageStart: nil,
			pageEnd:   nil,
			want:      true,
		},
		{
			name:      "start and end are the same positive page",
			pageStart: testIntPointer(1),
			pageEnd:   testIntPointer(1),
			want:      true,
		},
		{
			name:      "start is lower than end",
			pageStart: testIntPointer(2),
			pageEnd:   testIntPointer(5),
			want:      true,
		},
		{
			name:      "start exists but end is nil",
			pageStart: testIntPointer(1),
			pageEnd:   nil,
			want:      false,
		},
		{
			name:      "start is nil but end exists",
			pageStart: nil,
			pageEnd:   testIntPointer(1),
			want:      false,
		},
		{
			name:      "start is zero",
			pageStart: testIntPointer(0),
			pageEnd:   testIntPointer(1),
			want:      false,
		},
		{
			name:      "start is negative",
			pageStart: testIntPointer(-1),
			pageEnd:   testIntPointer(1),
			want:      false,
		},
		{
			name:      "start is greater than end",
			pageStart: testIntPointer(3),
			pageEnd:   testIntPointer(2),
			want:      false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chunk := ChunkInput{
				PageStart: test.pageStart,
				PageEnd:   test.pageEnd,
			}

			got := chunk.HasValidPageRange()
			if got != test.want {
				t.Fatalf(
					"HasValidPageRange() = %v, want %v",
					got,
					test.want,
				)
			}
		})
	}
}

func testIntPointer(value int) *int {
	return &value
}
