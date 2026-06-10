// Package spider 包含 zread 周 trending 接入 weekly-api 的辅助函数。
//
// 从 starcat-trending-api/internal/spider/zread_year_infer.go 复制并改造。
// v0.5 把 zread 从 trending-api 迁出到 weekly-api,本包由 weekly-api 独立持有。
package spider

import (
	"fmt"
	"log"
	"math"
	"time"
)

// InferYear 从 MM/DD 格式推断真实的年份。
//
// 逻辑：假设 zread 榜单的日期通常在当前日期之前（不会预知未来很久）。
// 如果月份大于当前月份，则很有可能是去年的（比如 1 月初看 12 月底的榜单）。
//
// 异常告警：如果推断的年份与当前年份相差 > 1 年（绝对值），说明跨年推断
// 仍可能出错，打 Warning 方便 2027 年元旦前后观察表现。
func InferYear(mmdd string, now time.Time) (int, error) {
	if len(mmdd) < 5 {
		return 0, fmt.Errorf("invalid format: %s", mmdd)
	}

	var m int
	if _, err := fmt.Sscanf(mmdd[0:2], "%d", &m); err != nil {
		return 0, fmt.Errorf("parse month: %w", err)
	}

	inferred := now.Year()
	if m > int(now.Month()) {
		inferred = now.Year() - 1
	}

	// 异常告警：阈值 "> 1"（不是 ">= 1"）以保留正常的"跨年周"情况。
	// 详见 19-wiki集成.md §8.2.1.1 阈值选择理由。
	if math.Abs(float64(inferred-now.Year())) > 1 {
		log.Printf("[zread_infer] WARN: inferred year %d differs from current year %d by > 1 for input %q",
			inferred, now.Year(), mmdd)
	}

	return inferred, nil
}
