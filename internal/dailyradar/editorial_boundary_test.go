// 独立编辑服务上线后，Weekly 不再承担每周 AI 大事的写入责任
package dailyradar

import "testing"

// TestEditorialSourceStaysOutsideWeekly 防止已撤除的 MVP 来源被重新放入日报写路径
func TestEditorialSourceStaysOutsideWeekly(t *testing.T) {
	request := ImportRequest{
		Source: "ai-weekly-events", SourceRepository: "starcat-app/starcat-ai-events",
		SourceKind: "editorial", SourceSequence: 1, IdempotencyKey: "editorial-boundary",
	}
	if err := ValidateImport(&request, []string{"cdn.dong4j.site"}, []string{"starcat-app/starcat-ai-events"}); err == nil {
		t.Fatal("editorial content must be imported through ai-events, not Weekly")
	}
}
