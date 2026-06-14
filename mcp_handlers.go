package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// MCP 工具处理函数

// parseVisibility 从 MCP 参数中解析可见范围
func parseVisibility(args map[string]interface{}) string {
	v, ok := args["visibility"]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// handleCheckLoginStatus 处理检查登录状态
func (s *AppServer) handleCheckLoginStatus(ctx context.Context) *MCPToolResult {
	logrus.Info("MCP: 检查登录状态")

	status, err := s.xiaohongshuService.CheckLoginStatus(ctx)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "检查登录状态失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 根据 IsLoggedIn 判断并返回友好的提示
	var resultText string
	if status.IsLoggedIn {
		resultText = fmt.Sprintf("✅ 已登录\n用户名: %s\n\n你可以使用其他功能了。", status.Username)
	} else {
		resultText = fmt.Sprintf("❌ 未登录\n\n请使用 get_login_qrcode 工具获取二维码进行登录。")
	}

	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: resultText,
		}},
	}
}

// handleGetLoginQrcode 处理获取登录二维码请求。
// 返回二维码图片的 Base64 编码和超时时间，供前端展示扫码登录。
func (s *AppServer) handleGetLoginQrcode(ctx context.Context) *MCPToolResult {
	logrus.Info("MCP: 获取登录扫码图片")

	result, err := s.xiaohongshuService.GetLoginQrcode(ctx)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "获取登录扫码图片失败: " + err.Error()}},
			IsError: true,
		}
	}

	if result.IsLoggedIn {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "你当前已处于登录状态"}},
		}
	}

	now := time.Now()
	deadline := func() string {
		d, err := time.ParseDuration(result.Timeout)
		if err != nil {
			return now.Format("2006-01-02 15:04:05")
		}
		return now.Add(d).Format("2006-01-02 15:04:05")
	}()

	// 已登录：文本 + 图片
	contents := []MCPContent{
		{Type: "text", Text: "请用小红书 App 在 " + deadline + " 前扫码登录 👇"},
		{
			Type:     "image",
			MimeType: "image/png",
			Data:     strings.TrimPrefix(result.Img, "data:image/png;base64,"),
		},
	}
	return &MCPToolResult{Content: contents}
}

// handleDeleteCookies 处理删除 cookies 请求，用于登录重置
func (s *AppServer) handleDeleteCookies(ctx context.Context) *MCPToolResult {
	logrus.Info("MCP: 删除 cookies，重置登录状态")

	err := s.xiaohongshuService.DeleteCookies(ctx)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "删除 cookies 失败: " + err.Error()}},
			IsError: true,
		}
	}

	cookiePath := cookies.GetCookiesFilePath()
	resultText := fmt.Sprintf("Cookies 已成功删除，登录状态已重置。\n\n删除的文件路径: %s\n\n下次操作时，需要重新登录。", cookiePath)
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: resultText,
		}},
	}
}

// handlePublishContent 处理发布内容
func (s *AppServer) handlePublishContent(ctx context.Context, args map[string]interface{}) *MCPToolResult {
	logrus.Info("MCP: 发布内容")

	// 解析参数
	title, _ := args["title"].(string)
	content, _ := args["content"].(string)
	imagePathsInterface, _ := args["images"].([]interface{})
	tagsInterface, _ := args["tags"].([]interface{})
	productsInterface, _ := args["products"].([]interface{})

	var imagePaths []string
	for _, path := range imagePathsInterface {
		if pathStr, ok := path.(string); ok {
			imagePaths = append(imagePaths, pathStr)
		}
	}

	var tags []string
	for _, tag := range tagsInterface {
		if tagStr, ok := tag.(string); ok {
			tags = append(tags, tagStr)
		}
	}

	var products []string
	for _, p := range productsInterface {
		if pStr, ok := p.(string); ok {
			products = append(products, pStr)
		}
	}

	// 解析定时发布参数
	scheduleAt, _ := args["schedule_at"].(string)
	visibility := parseVisibility(args)

	// 解析原创参数
	isOriginal, _ := args["is_original"].(bool)

	logrus.Infof("MCP: 发布内容 - 标题: %s, 图片数量: %d, 标签数量: %d, 定时: %s, 原创: %v, visibility: %s, 商品: %v", title, len(imagePaths), len(tags), scheduleAt, isOriginal, visibility, products)

	// 构建发布请求
	req := &PublishRequest{
		Title:      title,
		Content:    content,
		Images:     imagePaths,
		Tags:       tags,
		ScheduleAt: scheduleAt,
		IsOriginal: isOriginal,
		Visibility: visibility,
		Products:   products,
	}

	// 执行发布
	result, err := s.xiaohongshuService.PublishContent(ctx, req)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发布失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	resultText := fmt.Sprintf("内容发布成功: %+v", result)
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: resultText,
		}},
	}
}

// handlePublishVideo 处理发布视频内容（仅本地单个视频文件）
func (s *AppServer) handlePublishVideo(ctx context.Context, args map[string]interface{}) *MCPToolResult {
	logrus.Info("MCP: 发布视频内容（本地）")

	title, _ := args["title"].(string)
	content, _ := args["content"].(string)
	videoPath, _ := args["video"].(string)
	tagsInterface, _ := args["tags"].([]interface{})
	productsInterface, _ := args["products"].([]interface{})

	var tags []string
	for _, tag := range tagsInterface {
		if tagStr, ok := tag.(string); ok {
			tags = append(tags, tagStr)
		}
	}

	var products []string
	for _, p := range productsInterface {
		if pStr, ok := p.(string); ok {
			products = append(products, pStr)
		}
	}

	if videoPath == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发布失败: 缺少本地视频文件路径",
			}},
			IsError: true,
		}
	}

	// 解析定时发布参数
	scheduleAt, _ := args["schedule_at"].(string)
	visibility := parseVisibility(args)

	logrus.Infof("MCP: 发布视频 - 标题: %s, 标签数量: %d, 定时: %s, visibility: %s, 商品: %v", title, len(tags), scheduleAt, visibility, products)

	// 构建发布请求
	req := &PublishVideoRequest{
		Title:      title,
		Content:    content,
		Video:      videoPath,
		Tags:       tags,
		ScheduleAt: scheduleAt,
		Visibility: visibility,
		Products:   products,
	}

	// 执行发布
	result, err := s.xiaohongshuService.PublishVideo(ctx, req)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发布失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	resultText := fmt.Sprintf("视频发布成功: %+v", result)
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: resultText,
		}},
	}
}

// formatFeedsCompact 将Feed列表格式化为紧凑纯文本，节省token
func formatFeedsCompact(resp *FeedsListResponse) string {
	var b strings.Builder
	count := 0
	for _, f := range resp.Feeds {
		if f.ModelType == "hot_query" {
			continue
		}
		count++
		nc := f.NoteCard
		typeTag := "图文"
		if nc.Type == "video" {
			typeTag = "视频"
		}
		b.WriteString(fmt.Sprintf("%d. [%s] %s\n   %s (uid:%s) | 👍%s 💬%s ⭐%s\n   id:%s xsec:%s\n\n",
			count, typeTag, nc.DisplayTitle,
			nc.User.Nickname, nc.User.UserID,
			nc.InteractInfo.LikedCount, nc.InteractInfo.CommentCount, nc.InteractInfo.CollectedCount,
			f.ID, f.XsecToken))
	}
	return fmt.Sprintf("共%d条：\n\n%s", count, b.String())
}

// handleListFeeds 处理获取Feeds列表
func (s *AppServer) handleListFeeds(ctx context.Context) *MCPToolResult {
	logrus.Info("MCP: 获取Feeds列表")

	result, err := s.xiaohongshuService.ListFeeds(ctx)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取Feeds列表失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: formatFeedsCompact(result),
		}},
	}
}

// handleSearchFeeds 处理搜索Feeds
func (s *AppServer) handleSearchFeeds(ctx context.Context, args SearchFeedsArgs) *MCPToolResult {
	logrus.Info("MCP: 搜索Feeds")

	if args.Keyword == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "搜索Feeds失败: 缺少关键词参数",
			}},
			IsError: true,
		}
	}

	logrus.Infof("MCP: 搜索Feeds - 关键词: %s", args.Keyword)

	// 将 MCP 的 FilterOption 转换为 xiaohongshu.FilterOption
	filter := xiaohongshu.FilterOption{
		SortBy:      args.Filters.SortBy,
		NoteType:    args.Filters.NoteType,
		PublishTime: args.Filters.PublishTime,
		SearchScope: args.Filters.SearchScope,
		Location:    args.Filters.Location,
	}

	result, err := s.xiaohongshuService.SearchFeeds(ctx, args.Keyword, filter)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "搜索Feeds失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: formatFeedsCompact(result),
		}},
	}
}

// handleGetFeedDetail 处理获取Feed详情
func (s *AppServer) handleGetFeedDetail(ctx context.Context, args map[string]any) *MCPToolResult {
	logrus.Info("MCP: 获取Feed详情")

	// 解析参数
	feedID, ok := args["feed_id"].(string)
	if !ok || feedID == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取Feed详情失败: 缺少feed_id参数",
			}},
			IsError: true,
		}
	}

	xsecToken, _ := args["xsec_token"].(string)
	// xsec_token 现在是可选的，为空时后端会尝试不带token访问
	xsecSource, _ := args["xsec_source"].(string)
	// xsec_source 可选，默认 pc_feed，分享链接用 app_share

	loadAll := false
	if raw, ok := args["load_all_comments"]; ok {
		switch v := raw.(type) {
		case bool:
			loadAll = v
		case string:
			if parsed, err := strconv.ParseBool(v); err == nil {
				loadAll = parsed
			}
		case float64:
			loadAll = v != 0
		}
	}

	// 解析评论配置参数，如果未提供则使用默认值
	config := xiaohongshu.DefaultCommentLoadConfig()

	if raw, ok := args["click_more_replies"]; ok {
		switch v := raw.(type) {
		case bool:
			config.ClickMoreReplies = v
		case string:
			if parsed, err := strconv.ParseBool(v); err == nil {
				config.ClickMoreReplies = parsed
			}
		}
	}

	if raw, ok := args["max_replies_threshold"]; ok {
		switch v := raw.(type) {
		case float64:
			config.MaxRepliesThreshold = int(v)
		case string:
			if parsed, err := strconv.Atoi(v); err == nil {
				config.MaxRepliesThreshold = parsed
			}
		case int:
			config.MaxRepliesThreshold = v
		}
	}

	if raw, ok := args["max_comment_items"]; ok {
		switch v := raw.(type) {
		case float64:
			config.MaxCommentItems = int(v)
		case string:
			if parsed, err := strconv.Atoi(v); err == nil {
				config.MaxCommentItems = parsed
			}
		case int:
			config.MaxCommentItems = v
		}
	}

	if raw, ok := args["scroll_speed"].(string); ok && raw != "" {
		config.ScrollSpeed = raw
	}

	logrus.Infof("MCP: 获取Feed详情 - Feed ID: %s, loadAllComments=%v, config=%+v", feedID, loadAll, config)

	result, err := s.xiaohongshuService.GetFeedDetailWithConfig(ctx, feedID, xsecToken, xsecSource, loadAll, config)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取Feed详情失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 格式化输出，转换为JSON字符串
	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: fmt.Sprintf("获取Feed详情成功，但序列化失败: %v", err),
			}},
			IsError: true,
		}
	}

	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: string(jsonData),
		}},
	}
}

// handleUserProfile 获取用户主页
func (s *AppServer) handleUserProfile(ctx context.Context, args map[string]any) *MCPToolResult {
	logrus.Info("MCP: 获取用户主页")

	// 解析参数
	userID, ok := args["user_id"].(string)
	if !ok || userID == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取用户主页失败: 缺少user_id参数",
			}},
			IsError: true,
		}
	}

	xsecToken, ok := args["xsec_token"].(string)
	if !ok || xsecToken == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取用户主页失败: 缺少xsec_token参数",
			}},
			IsError: true,
		}
	}

	logrus.Infof("MCP: 获取用户主页 - User ID: %s", userID)

	result, err := s.xiaohongshuService.UserProfile(ctx, userID, xsecToken)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "获取用户主页失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 格式化输出，转换为JSON字符串
	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: fmt.Sprintf("获取用户主页，但序列化失败: %v", err),
			}},
			IsError: true,
		}
	}

	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: string(jsonData),
		}},
	}
}

// handleLikeFeed 处理点赞/取消点赞
func (s *AppServer) handleLikeFeed(ctx context.Context, args map[string]interface{}) *MCPToolResult {
	feedID, ok := args["feed_id"].(string)
	if !ok || feedID == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "操作失败: 缺少feed_id参数"}}, IsError: true}
	}
	xsecToken, ok := args["xsec_token"].(string)
	if !ok || xsecToken == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "操作失败: 缺少xsec_token参数"}}, IsError: true}
	}
	unlike, _ := args["unlike"].(bool)

	var res *ActionResult
	var err error

	if unlike {
		res, err = s.xiaohongshuService.UnlikeFeed(ctx, feedID, xsecToken)
	} else {
		res, err = s.xiaohongshuService.LikeFeed(ctx, feedID, xsecToken)
	}

	if err != nil {
		action := "点赞"
		if unlike {
			action = "取消点赞"
		}
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: action + "失败: " + err.Error()}}, IsError: true}
	}

	action := "点赞"
	if unlike {
		action = "取消点赞"
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: fmt.Sprintf("%s成功 - Feed ID: %s", action, res.FeedID)}}}
}

// handleFavoriteFeed 处理收藏/取消收藏
func (s *AppServer) handleFavoriteFeed(ctx context.Context, args map[string]interface{}) *MCPToolResult {
	feedID, ok := args["feed_id"].(string)
	if !ok || feedID == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "操作失败: 缺少feed_id参数"}}, IsError: true}
	}
	xsecToken, ok := args["xsec_token"].(string)
	if !ok || xsecToken == "" {
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: "操作失败: 缺少xsec_token参数"}}, IsError: true}
	}
	unfavorite, _ := args["unfavorite"].(bool)

	var res *ActionResult
	var err error

	if unfavorite {
		res, err = s.xiaohongshuService.UnfavoriteFeed(ctx, feedID, xsecToken)
	} else {
		res, err = s.xiaohongshuService.FavoriteFeed(ctx, feedID, xsecToken)
	}

	if err != nil {
		action := "收藏"
		if unfavorite {
			action = "取消收藏"
		}
		return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: action + "失败: " + err.Error()}}, IsError: true}
	}

	action := "收藏"
	if unfavorite {
		action = "取消收藏"
	}
	return &MCPToolResult{Content: []MCPContent{{Type: "text", Text: fmt.Sprintf("%s成功 - Feed ID: %s", action, res.FeedID)}}}
}

// handlePostComment 处理发表评论到Feed
func (s *AppServer) handlePostComment(ctx context.Context, args map[string]interface{}) *MCPToolResult {
	logrus.Info("MCP: 发表评论到Feed")

	// 解析参数
	feedID, ok := args["feed_id"].(string)
	if !ok || feedID == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发表评论失败: 缺少feed_id参数",
			}},
			IsError: true,
		}
	}

	xsecToken, ok := args["xsec_token"].(string)
	if !ok || xsecToken == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发表评论失败: 缺少xsec_token参数",
			}},
			IsError: true,
		}
	}

	content, ok := args["content"].(string)
	if !ok || content == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发表评论失败: 缺少content参数",
			}},
			IsError: true,
		}
	}

	logrus.Infof("MCP: 发表评论 - Feed ID: %s, 内容长度: %d", feedID, len(content))

	// 发表评论
	result, err := s.xiaohongshuService.PostCommentToFeed(ctx, feedID, xsecToken, content)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "发表评论失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 返回成功结果，只包含feed_id
	resultText := fmt.Sprintf("评论发表成功 - Feed ID: %s", result.FeedID)
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: resultText,
		}},
	}
}

// handleReplyComment 处理回复评论
func (s *AppServer) handleReplyComment(ctx context.Context, args map[string]interface{}) *MCPToolResult {
	logrus.Info("MCP: 回复评论")

	// 解析参数
	feedID, ok := args["feed_id"].(string)
	if !ok || feedID == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "回复评论失败: 缺少feed_id参数",
			}},
			IsError: true,
		}
	}

	xsecToken, ok := args["xsec_token"].(string)
	if !ok || xsecToken == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "回复评论失败: 缺少xsec_token参数",
			}},
			IsError: true,
		}
	}

	commentID, _ := args["comment_id"].(string)
	userID, _ := args["user_id"].(string)
	if commentID == "" && userID == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "回复评论失败: 缺少comment_id或user_id参数",
			}},
			IsError: true,
		}
	}

	content, ok := args["content"].(string)
	if !ok || content == "" {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "回复评论失败: 缺少content参数",
			}},
			IsError: true,
		}
	}

	logrus.Infof("MCP: 回复评论 - Feed ID: %s, Comment ID: %s, User ID: %s, 内容长度: %d", feedID, commentID, userID, len(content))

	// 回复评论
	result, err := s.xiaohongshuService.ReplyCommentToFeed(ctx, feedID, xsecToken, commentID, userID, content)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{
				Type: "text",
				Text: "回复评论失败: " + err.Error(),
			}},
			IsError: true,
		}
	}

	// 返回成功结果
	responseText := fmt.Sprintf("评论回复成功 - Feed ID: %s, Comment ID: %s, User ID: %s", result.FeedID, result.TargetCommentID, result.TargetUserID)
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: responseText,
		}},
	}
}

// handleResolveShortlink 处理解析分享短链
func (s *AppServer) handleResolveShortlink(ctx context.Context, rawURL string) *MCPToolResult {
	logrus.Infof("MCP: 解析短链 - URL: %s", rawURL)

	if rawURL == "" {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "解析失败: 缺少URL参数"}},
			IsError: true,
		}
	}

	result, err := xiaohongshu.ResolveShortlink(rawURL)
	if err != nil {
		// 如果不是短链，尝试直接解析为完整URL
		result, err = xiaohongshu.ParseXHSURL(rawURL)
		if err != nil {
			return &MCPToolResult{
				Content: []MCPContent{{Type: "text", Text: "解析失败: " + err.Error()}},
				IsError: true,
			}
		}
	}

	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "序列化结果失败: " + err.Error()}},
			IsError: true,
		}
	}

	return &MCPToolResult{
		Content: []MCPContent{{Type: "text", Text: string(jsonData)}},
	}
}

// handleExtractTextFromFeed 获取帖子详情并OCR所有图片
func (s *AppServer) handleExtractTextFromFeed(parent context.Context, args map[string]any) *MCPToolResult {
	logrus.Info("MCP: 提取帖子图文")

	// 内部 deadline：180s，比 Drawer 的 210s 小，确保部分结果能在 Drawer 连接存活时返回
	ctx, cancel := context.WithTimeoutCause(parent, 180*time.Second, ErrExtractionDeadline)
	defer cancel()

	feedID, _ := args["feed_id"].(string)
	xsecToken, _ := args["xsec_token"].(string)
	xsecSource, _ := args["xsec_source"].(string)
	shortURL, _ := args["url"].(string)

	// 如果提供了短链，先解析
	if shortURL != "" && feedID == "" {
		result, err := xiaohongshu.ResolveShortlink(shortURL)
		if err != nil {
			// 尝试直接解析完整URL
			result, err = xiaohongshu.ParseXHSURL(shortURL)
			if err != nil {
				return &MCPToolResult{
					Content: []MCPContent{{Type: "text", Text: "短链解析失败: " + err.Error()}},
					IsError: true,
				}
			}
		}
		feedID = result.FeedID
		xsecToken = result.XsecToken
		xsecSource = result.XsecSource
	}

	if feedID == "" {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "需要提供 feed_id 或 url"}},
			IsError: true,
		}
	}

	// 获取帖子详情
	config := xiaohongshu.DefaultCommentLoadConfig()
	detail, err := s.xiaohongshuService.GetFeedDetailWithConfig(ctx, feedID, xsecToken, xsecSource, false, config)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "获取帖子详情失败: " + err.Error()}},
			IsError: true,
		}
	}

	// 提取图片URL列表
	type imageInfo struct {
		URLDefault string `json:"urlDefault"`
	}
	type noteData struct {
		Note struct {
			Title     string      `json:"title"`
			Desc      string      `json:"desc"`
			ImageList []imageInfo `json:"imageList"`
			User      struct {
				Nickname string `json:"nickname"`
			} `json:"user"`
		} `json:"note"`
	}

	// 重新解析detail.Data获取图片URL
	dataBytes, err := json.Marshal(detail.Data)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "解析帖子数据失败: " + err.Error()}},
			IsError: true,
		}
	}
	var note noteData
	if err := json.Unmarshal(dataBytes, &note); err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "解析图片列表失败: " + err.Error()}},
			IsError: true,
		}
	}

	imageURLs := make([]string, 0)
	for _, img := range note.Note.ImageList {
		if img.URLDefault != "" {
			imageURLs = append(imageURLs, img.URLDefault)
		}
	}

	if len(imageURLs) == 0 {
		// 没有图片，直接返回文字内容
		result := map[string]any{
			"feed_id":  feedID,
			"title":    note.Note.Title,
			"author":   note.Note.User.Nickname,
			"desc":     note.Note.Desc,
			"images":   []any{},
			"text_all": note.Note.Desc,
		}
		jsonData, _ := json.MarshalIndent(result, "", "  ")
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: string(jsonData)}},
		}
	}

	// 启动OCR
	bridge, err := GetOCRBridge(ctx)
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "OCR引擎启动失败: " + err.Error()}},
			IsError: true,
		}
	}

	logrus.Infof("开始OCR %d 张图片", len(imageURLs))
	ocrResults, ocrErr := bridge.OCRMultiple(ctx, imageURLs)

	// 组装结果
	textAll := ""
	images := make([]map[string]any, len(ocrResults))
	successCount := 0
	for i, r := range ocrResults {
		images[i] = map[string]any{
			"index":      i,
			"status":     r.Status,
			"text":       r.Text,
			"line_count": r.LineCount,
			"elapsed_ms": r.ElapsedMs,
		}
		if r.Error != "" {
			images[i]["error"] = r.Error
		}
		if r.Status == "success" && r.Text != "" {
			successCount++
			textAll += fmt.Sprintf("[图%d]\n%s\n\n", i+1, r.Text)
		} else if r.Status == "empty_text" {
			textAll += fmt.Sprintf("[图%d]\n(无文字)\n\n", i+1)
		} else {
			textAll += fmt.Sprintf("[图%d]\n(OCR失败: %s)\n\n", i+1, r.Error)
		}
	}

	// 调用方主动取消：不拼装部分结果，直接返回错误。
	if parent.Err() != nil {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "调用已取消: " + parent.Err().Error()}},
			IsError: true,
		}
	}

	ocrStatus := "success"
	if successCount == 0 {
		ocrStatus = "failed"
	} else if successCount < len(ocrResults) {
		ocrStatus = "partial_success"
	}

	// 任务终止状态摘要（PR1 / Step 0 新增）
	items := make([]ImageOCRItem, len(ocrResults))
	for i, r := range ocrResults {
		items[i] = ImageOCRItem{
			Index:     i,
			Status:    r.Status,
			Text:      r.Text,
			LineCount: r.LineCount,
			ElapsedMs: r.ElapsedMs,
			Error:     r.Error,
		}
	}
	var extraction ExtractionResult
	cause := context.Cause(ctx)
	switch {
	// 图片数上限触发：OCRMultiple 直接返 ErrImageLimit。
	case errors.Is(ocrErr, ErrImageLimit):
		extraction = partialResult(len(imageURLs), items, TruncatedByImageLimit)
	// 内部 deadline 到期：ctx.Cause 注入 ErrExtractionDeadline。
	case errors.Is(cause, ErrExtractionDeadline):
		extraction = partialResult(len(imageURLs), items, TruncatedByDeadline)
	// 其他 ctx 错误（非 parent 取消，因前面已 return）：作部分结果带 deadline 语义。
	case ocrErr != nil:
		extraction = partialResult(len(imageURLs), items, TruncatedByDeadline)
	default:
		extraction = completeResult(len(imageURLs), items)
	}

	result := map[string]any{
		"feed_id":               feedID,
		"title":                 note.Note.Title,
		"author":                note.Note.User.Nickname,
		"desc":                  note.Note.Desc,
		"ocr_status":            ocrStatus,
		"ocr_engine":            "rapidocr-onnxruntime",
		"image_count":           len(imageURLs),
		"text_all":              textAll,
		"images":                images,
		"status":                extraction.Status,
		"total_image_count":     extraction.TotalImageCount,
		"processed_image_count": extraction.ProcessedImageCount,
		"remaining_image_count": extraction.RemainingImageCount,
		"truncated":             extraction.Truncated,
	}
	if extraction.Truncated {
		result["truncated_reason"] = extraction.TruncatedReason
	}

	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return &MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: "序列化结果失败: " + err.Error()}},
			IsError: true,
		}
	}

	return &MCPToolResult{
		Content: []MCPContent{{Type: "text", Text: string(jsonData)}},
	}
}
