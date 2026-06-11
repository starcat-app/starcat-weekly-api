package spider

import (
	"testing"
	"time"
)

// TestInferYear 覆盖 5 个典型 case（输入格式 DD/MM）：
//  1. 当月，月份小于当前月 → 不跨年
//  2. 上月，月份等于当前月 → 不跨年
//  3. 跨年（Dec → Jan 观察窗口）→ 推断去年
//  4. 非法格式（长度 < 5） → 返回 error
//  5. 月份字段非数字 → 返回 error
//
// 与 trending-api/internal/spider/zread_year_infer_test.go 用例保持一致。
func TestInferYear(t *testing.T) {
	tests := []struct {
		name     string
		ddmm     string
		now      time.Time
		expected int
		wantErr  bool
	}{
		{
			name:     "Same year, earlier month",
			ddmm:     "01/05", // DD/MM: May 1, month=5 ≤ 6 → 同一年
			now:      time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			expected: 2026,
			wantErr:  false,
		},
		{
			name:     "Cross year (Dec to Jan)",
			ddmm:     "28/12", // DD/MM: Dec 28, month=12 > 1 → 去年
			now:      time.Date(2027, 1, 3, 0, 0, 0, 0, time.UTC),
			expected: 2026,
			wantErr:  false,
		},
		{
			name:     "Same month",
			ddmm:     "10/06", // DD/MM: Jun 10, month=6 ≤ 6 → 同一年
			now:      time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			expected: 2026,
			wantErr:  false,
		},
		{
			name:     "Invalid format",
			ddmm:     "1",
			now:      time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "Non-numeric month",
			ddmm:     "01/XX", // DD/MM: month 位置非数字
			now:      time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			expected: 0,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := InferYear(tt.ddmm, tt.now)
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
