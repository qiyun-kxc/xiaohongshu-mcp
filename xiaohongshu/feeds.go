package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
)

type FeedsListAction struct {
	page *rod.Page
}

func NewFeedsListAction(page *rod.Page) *FeedsListAction {
	pp := page.Timeout(60 * time.Second)

	pp.MustNavigate("https://www.xiaohongshu.com")
	pp.MustWaitDOMStable()
	sleepFixedJitter(800*time.Millisecond, 250*time.Millisecond)

	return &FeedsListAction{page: pp}
}

// GetFeedsList 获取页面的 Feed 列表数据
func (f *FeedsListAction) GetFeedsList(ctx context.Context) ([]Feed, error) {
	page := f.page.Context(ctx)

	sleepFixedJitter(1*time.Second, 300*time.Millisecond)

	if err := CheckVerification(page); err != nil {
		return nil, err
	}

	// 模拟真人浏览行为后再提取数据
	simulateHumanBrowse(page)

	// 间接读取应用状态，不直接引用全局变量名
	result, err := readAppState(page, "feed.feeds")
	if err != nil || result == "" {
		return nil, errors.ErrNoFeeds
	}

	var feeds []Feed
	if err := json.Unmarshal([]byte(result), &feeds); err != nil {
		return nil, fmt.Errorf("failed to unmarshal feeds: %w", err)
	}

	return feeds, nil
}
