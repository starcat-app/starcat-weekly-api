package spider

import (
	"testing"
	"time"
)

// TestInferYear 覆盖 5 个典型 case：
//  1. 当月，月份小于当前月 → 不跨年
//  2. 上月，月份等于当前月 → 不跨年
//  3. 跨年（Dec → Jan 观察窗口）→ 推断去年
//  4. 非法格式（长度 < 5） → 返回 error
//  5. 月份字段非数字 → 返回 error
//
// 与 trending-api/internal/spider/zread_year_infer_test.go 用例保持一致，
// 以便 4 份代码同步对比。
func TestInferYear(t *testing.T) {
	tests := []struct {
		name     string
		mmdd     string
		now      time.Time
		expected int
		wantErr  bool
	}{
		{
			name:     "Same year, earlier month",
			mmdd:     "05/01",
			now:      time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			expected: 2026,
			wantErr:  false,
		},
		{
			name:     "Cross year (Dec to Jan)",
			mmdd:     "12/28",
			now:      time.Date(2027, 1, 3, 0, 0, 0, 0, time.UTC),
			expected: 2026,
			wantErr:  false,
		},
		{
			name:     "Same month",
			mmdd:     "06/01",
			now:      time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			expected: 2026,
			wantErr:  false,
		},
		{
			name:     "Invalid format",
			mmdd:     "1",
			now:      time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "Non-numeric month",
			mmdd:     "XX/01",
			now:      time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			expected: 0,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := InferYear(tt.mmdd, tt.now)
			if (err != nil) != tt.wantErr {
				t.Errorf("InferYear() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("InferYear() got = %v, want %v", got, tt.expected)
			}
		})
	}
}
