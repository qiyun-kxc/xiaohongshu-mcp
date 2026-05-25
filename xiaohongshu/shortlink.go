package xiaohongshu

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ShortlinkResult 短链解析结果
type ShortlinkResult struct {
	FeedID     string `json:"feed_id"`
	XsecToken  string `json:"xsec_token"`
	XsecSource string `json:"xsec_source"`
	WebURL     string `json:"web_url"`
}

// ResolveShortlink 解析小红书分享短链，提取 feed_id 和 xsec_token
func ResolveShortlink(shortURL string) (*ShortlinkResult, error) {
	// 不跟随重定向，手动获取 Location
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(shortURL)
	if err != nil {
		return nil, fmt.Errorf("请求短链失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 301 && resp.StatusCode != 302 {
		return nil, fmt.Errorf("短链未返回重定向，状态码: %d", resp.StatusCode)
	}

	location := resp.Header.Get("Location")
	if location == "" {
		return nil, fmt.Errorf("短链重定向无 Location 头")
	}

	return ParseXHSURL(location)
}

// parseXHSURL 从小红书完整URL中提取 feed_id, xsec_token, xsec_source
func ParseXHSURL(rawURL string) (*ShortlinkResult, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("解析URL失败: %w", err)
	}

	// 从路径提取 feed_id
	// 路径格式: /discovery/item/{feed_id} 或 /explore/{feed_id}
	path := u.Path
	feedID := ""
	if strings.Contains(path, "/discovery/item/") {
		parts := strings.Split(path, "/discovery/item/")
		if len(parts) > 1 {
			feedID = parts[1]
		}
	} else if strings.Contains(path, "/explore/") {
		parts := strings.Split(path, "/explore/")
		if len(parts) > 1 {
			feedID = parts[1]
		}
	}

	if feedID == "" {
		return nil, fmt.Errorf("无法从URL中提取 feed_id: %s", rawURL)
	}

	// 清理 feed_id（去掉可能的尾部斜杠或参数）
	feedID = strings.TrimRight(feedID, "/")

	query := u.Query()
	xsecToken := query.Get("xsec_token")
	xsecSource := query.Get("xsec_source")

	if xsecToken == "" {
		return nil, fmt.Errorf("URL中未找到 xsec_token: %s", rawURL)
	}

	// 构建标准 web URL
	webURL := fmt.Sprintf("https://www.xiaohongshu.com/explore/%s?xsec_token=%s&xsec_source=%s",
		feedID, url.QueryEscape(xsecToken), xsecSource)

	return &ShortlinkResult{
		FeedID:     feedID,
		XsecToken:  xsecToken,
		XsecSource: xsecSource,
		WebURL:     webURL,
	}, nil
}
