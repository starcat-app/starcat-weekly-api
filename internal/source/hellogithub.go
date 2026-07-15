package source

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

const (
	defaultHelloGitHubAPIBase = "https://abroad.hellogithub.com"
	defaultHelloGitHubWebBase = "https://hellogithub.com"
)

// HelloGitHubClient 只负责读取并校验 HelloGitHub 的公开数据结构。
// 它不会访问 GitHub API，避免来源抓取阶段与 enrich 配额耦合。
type HelloGitHubClient struct {
	httpClient *http.Client
	apiBase    string
	webBase    string
}

// HelloGitHubVolume 是一期月刊的结构化快照。
type HelloGitHubVolume struct {
	Number      int
	Latest      int
	PublishedAt time.Time
	Candidates  []model.IngestCandidate
}

// NewHelloGitHubClient 创建公开来源客户端；nil client 会使用带超时的默认客户端。
func NewHelloGitHubClient(client *http.Client) *HelloGitHubClient {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &HelloGitHubClient{
		httpClient: client,
		apiBase:    defaultHelloGitHubAPIBase,
		webBase:    defaultHelloGitHubWebBase,
	}
}

// FetchFeaturedPage 读取一页精选项目。空页是合法的分页终点，但响应结构异常会返回错误，
// 防止上游页面改版后把“解析成 0 条”误当成成功。
func (c *HelloGitHubClient) FetchFeaturedPage(ctx context.Context, page int) ([]model.IngestCandidate, error) {
	if page < 1 {
		return nil, fmt.Errorf("HelloGitHub featured page must be positive")
	}
	endpoint, err := url.Parse(c.apiBase + "/v1/")
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("sort_by", "featured")
	query.Set("page", strconv.Itoa(page))
	query.Set("rank_by", "newest")
	query.Set("tid", "all")
	endpoint.RawQuery = query.Encode()

	body, err := c.get(ctx, endpoint.String())
	if err != nil {
		return nil, err
	}
	var response helloGitHubFeaturedResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode HelloGitHub featured page %d: %w", page, err)
	}
	if !response.Success || response.Page != page || response.Data == nil {
		return nil, fmt.Errorf("unexpected HelloGitHub featured response: success=%t page=%d data_nil=%t", response.Success, response.Page, response.Data == nil)
	}

	candidates := make([]model.IngestCandidate, 0, len(response.Data))
	for rank, item := range response.Data {
		owner, repo, err := splitFullName(item.FullName)
		if err != nil {
			return nil, fmt.Errorf("featured item %q: %w", item.ItemID, err)
		}
		occurredAt, err := parseHelloGitHubTime(item.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("featured item %q updated_at: %w", item.ItemID, err)
		}
		position := rank + 1
		candidates = append(candidates, model.IngestCandidate{
			Owner: owner, Repo: repo,
			ExternalKey: "featured:" + item.ItemID,
			OccurredAt:  occurredAt,
			SourceURL:   c.webBase + "/repository/" + item.ItemID,
			Title:       item.Title,
			Summary:     item.Summary,
			Rank:        &position,
			Payload: map[string]any{
				"item_id": item.ItemID, "title_en": item.TitleEN, "summary_en": item.SummaryEN,
				"primary_language": item.PrimaryLanguage, "is_hot": item.IsHot, "featured_page": page,
			},
		})
	}
	return candidates, nil
}

// FetchVolume 从 Next.js SSR 的 __NEXT_DATA__ 中读取一期月刊。
// 依赖结构化 JSON 而不是可见文案或 DOM class，降低样式改版对历史回填的影响。
func (c *HelloGitHubClient) FetchVolume(ctx context.Context, number int) (HelloGitHubVolume, error) {
	if number < 1 {
		return HelloGitHubVolume{}, fmt.Errorf("HelloGitHub volume must be positive")
	}
	pageURL := fmt.Sprintf("%s/periodical/volume/%d", c.webBase, number)
	body, err := c.get(ctx, pageURL)
	if err != nil {
		return HelloGitHubVolume{}, err
	}
	nextData, err := extractNextData(body)
	if err != nil {
		return HelloGitHubVolume{}, fmt.Errorf("parse HelloGitHub volume %d: %w", number, err)
	}
	var document helloGitHubNextData
	if err := json.Unmarshal(nextData, &document); err != nil {
		return HelloGitHubVolume{}, fmt.Errorf("decode HelloGitHub volume %d: %w", number, err)
	}
	volume := document.Props.PageProps.Volume
	if !volume.Success || volume.CurrentNumber != number || volume.Total < number || volume.Data == nil {
		return HelloGitHubVolume{}, fmt.Errorf("unexpected HelloGitHub volume response: success=%t current=%d total=%d data_nil=%t", volume.Success, volume.CurrentNumber, volume.Total, volume.Data == nil)
	}
	publishedAt, err := parseHelloGitHubTime(volume.PublishAt)
	if err != nil {
		return HelloGitHubVolume{}, fmt.Errorf("volume %d publish_at: %w", number, err)
	}

	candidates := make([]model.IngestCandidate, 0)
	for _, category := range volume.Data {
		for rank, item := range category.Items {
			owner, repo, err := splitFullName(item.FullName)
			if err != nil {
				return HelloGitHubVolume{}, fmt.Errorf("volume %d item %q: %w", number, item.RID, err)
			}
			occurredAt := publishedAt
			if item.PublishAt != "" {
				occurredAt, err = parseHelloGitHubTime(item.PublishAt)
				if err != nil {
					return HelloGitHubVolume{}, fmt.Errorf("volume %d item %q publish_at: %w", number, item.RID, err)
				}
			}
			position := rank + 1
			candidates = append(candidates, model.IngestCandidate{
				Owner: owner, Repo: repo,
				ExternalKey: fmt.Sprintf("volume:%d:%s", number, strings.ToLower(item.FullName)),
				OccurredAt:  occurredAt,
				SourceURL:   pageURL,
				Title:       item.Name,
				Summary:     item.Description,
				Rank:        &position,
				Payload: map[string]any{
					"rid": item.RID, "volume": number, "category_id": category.ID,
					"category_name": category.Name, "description_en": item.DescriptionEN,
				},
			})
		}
	}
	if len(candidates) == 0 {
		return HelloGitHubVolume{}, fmt.Errorf("HelloGitHub volume %d contains no repositories", number)
	}
	return HelloGitHubVolume{Number: number, Latest: volume.Total, PublishedAt: publishedAt, Candidates: candidates}, nil
}

func (c *HelloGitHubClient) get(ctx context.Context, endpoint string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json,text/html;q=0.9")
	request.Header.Set("User-Agent", "starcat-weekly-api/1.0")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", endpoint, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: status %d", endpoint, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", endpoint, err)
	}
	return body, nil
}

func extractNextData(body []byte) ([]byte, error) {
	const marker = `<script id="__NEXT_DATA__" type="application/json">`
	text := string(body)
	start := strings.Index(text, marker)
	if start < 0 {
		return nil, fmt.Errorf("__NEXT_DATA__ marker not found")
	}
	start += len(marker)
	end := strings.Index(text[start:], "</script>")
	if end < 0 {
		return nil, fmt.Errorf("__NEXT_DATA__ closing tag not found")
	}
	data := html.UnescapeString(text[start : start+end])
	if !json.Valid([]byte(data)) {
		return nil, fmt.Errorf("__NEXT_DATA__ is not valid JSON")
	}
	return []byte(data), nil
}

func splitFullName(fullName string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(fullName), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid full_name %q", fullName)
	}
	return parts[0], parts[1], nil
}

// HelloGitHub 时间字段不带时区。官方按 UTC 发布，统一补 UTC 后写入数据库。
func parseHelloGitHubTime(value string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02T15:04:05", value, time.UTC)
}

type helloGitHubFeaturedResponse struct {
	Success bool                      `json:"success"`
	Page    int                       `json:"page"`
	Data    []helloGitHubFeaturedItem `json:"data"`
}

type helloGitHubFeaturedItem struct {
	ItemID          string `json:"item_id"`
	FullName        string `json:"full_name"`
	Title           string `json:"title"`
	TitleEN         string `json:"title_en"`
	Summary         string `json:"summary"`
	SummaryEN       string `json:"summary_en"`
	IsHot           bool   `json:"is_hot"`
	PrimaryLanguage string `json:"primary_lang"`
	UpdatedAt       string `json:"updated_at"`
}

type helloGitHubNextData struct {
	Props struct {
		PageProps struct {
			Volume helloGitHubVolumeResponse `json:"volume"`
		} `json:"pageProps"`
	} `json:"props"`
}

type helloGitHubVolumeResponse struct {
	Success       bool                        `json:"success"`
	Total         int                         `json:"total"`
	CurrentNumber int                         `json:"current_num"`
	PublishAt     string                      `json:"publish_at"`
	Data          []helloGitHubVolumeCategory `json:"data"`
}

type helloGitHubVolumeCategory struct {
	ID    int                     `json:"category_id"`
	Name  string                  `json:"category_name"`
	Items []helloGitHubVolumeItem `json:"items"`
}

type helloGitHubVolumeItem struct {
	RID           string `json:"rid"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	DescriptionEN string `json:"description_en"`
	PublishAt     string `json:"publish_at"`
}
