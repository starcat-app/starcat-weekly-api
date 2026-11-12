package dailyradar

import (
	"testing"

	"github.com/starcat-app/starcat-weekly-api/internal/model"
)

// TestRelatedProjectBadgesBound 验证徽标数量上限与字段裁剪，防止列表随项目数膨胀。
func TestRelatedProjectBadgesBound(t *testing.T) {
	projects := make([]RelatedProject, 0, 8)
	for i := range 8 {
		avatar := "avatar"
		projects = append(projects, RelatedProject{Repo: model.StarcatRepoCardDTO{
			GhRepoID: int64(i), FullName: "owner/repo" + string(rune('a'+i)), OwnerAvatar: &avatar,
		}})
	}
	badges := RelatedProjectBadges(projects)
	if len(badges) != 6 || badges[0].GhRepoID != 0 || badges[5].FullName != "owner/repof" || badges[5].OwnerAvatar == nil {
		t.Fatalf("%+v", badges)
	}
	if len(RelatedProjectBadges(nil)) != 0 {
		t.Fatal("empty projects must serialize as empty badges")
	}
}
