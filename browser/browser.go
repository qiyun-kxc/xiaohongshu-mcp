package browser

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sync"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
)

// browserMu 进程级串行锁. 当前架构每个请求新建一个 browser, 接入持久化 UserDataDir 后,
// 同一 profile 同一时刻只能被一个 Chrome 打开. 锁在 NewBrowser 获取、Close 释放,
// 覆盖整个 browser 生命周期, 避免并发/重试/超时未释放导致的 SingletonLock 冲突.
var browserMu sync.Mutex

// Browser 项目自管的浏览器封装(替代 headless_browser 库, 以支持持久化 UserDataDir).
type Browser struct {
	browser           *rod.Browser
	launcher          *launcher.Launcher
	persistentProfile bool
	fileLock          *profileLock // 跨进程锁(仅持久 profile 时非 nil)
}

type browserConfig struct {
	binPath string
}

type Option func(*browserConfig)

func WithBinPath(binPath string) Option {
	return func(c *browserConfig) { c.binPath = binPath }
}

// maskProxyCredentials 隐藏代理 URL 中的账号密码, 便于安全地打日志.
func maskProxyCredentials(proxyURL string) string {
	u, err := url.Parse(proxyURL)
	if err != nil || u.User == nil {
		return proxyURL
	}
	if _, has := u.User.Password(); has {
		u.User = url.UserPassword("***", "***")
	} else {
		u.User = url.User("***")
	}
	return u.String()
}

// 临时默认 UA(Linux); 实际会被 NewPage 的 stealth + NormalizePage 覆盖.
const defaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"

func NewBrowser(headless bool, options ...Option) *Browser {
	cfg := &browserConfig{}
	for _, opt := range options {
		opt(cfg)
	}

	// 进程级锁: 创建失败(panic)时由 defer 释放, 成功则把锁留给 Close 释放.
	browserMu.Lock()
	created := false
	var flock *profileLock
	defer func() {
		if !created {
			flock.release() // 释放可能已获取的文件锁
			browserMu.Unlock()
		}
	}()

	l := launcher.New().
		Headless(headless).
		Set("no-sandbox").
		Set("user-agent", defaultUserAgent)

	if cfg.binPath != "" {
		l = l.Bin(cfg.binPath)
	}

	if proxy := os.Getenv("XHS_PROXY"); proxy != "" {
		l = l.Proxy(proxy)
		logrus.Infof("Using proxy: %s", maskProxyCredentials(proxy))
	}

	// 可配置的持久化 profile 目录; 留空则 go-rod 用临时目录(旧行为, 天然回滚).
	persistent := false
	if dir := os.Getenv("XHS_BROWSER_USER_DATA_DIR"); dir != "" {
		// 跨进程锁: 防止 service/cmd-login/audit 同时打开同一 profile.
		lk, err := lockProfileDir(dir)
		if err != nil {
			panic(fmt.Sprintf("profile %q 正被另一进程占用(或加锁失败): %v; "+
				"请先停止占用进程(如 cmd/login), 或该进程改用临时 profile", dir, err))
		}
		flock = lk
		l = l.UserDataDir(dir)
		persistent = true
		logrus.Infof("using persistent user-data-dir: %s", dir)
	}

	browser := rod.New().ControlURL(l.MustLaunch()).MustConnect()

	// 加载 cookies(失败不致命)
	cookiePath := cookies.GetCookiesFilePath()
	if data, err := cookies.NewLoadCookie(cookiePath).LoadCookies(); err == nil {
		var cs []*proto.NetworkCookie
		if err := json.Unmarshal(data, &cs); err != nil {
			logrus.Warnf("failed to unmarshal cookies: %v", err)
		} else {
			browser.MustSetCookies(cs...)
		}
	} else {
		logrus.Warnf("failed to load cookies: %v", err)
	}

	created = true
	return &Browser{browser: browser, launcher: l, persistentProfile: persistent, fileLock: flock}
}

// NewPage 创建启用 stealth 的页面.
func (b *Browser) NewPage() *rod.Page {
	return stealth.MustPage(b.browser)
}

// NewNormalizedPage 创建 stealth 页面并纠正 stealth 引入的画像矛盾(诚实 Linux).
// 生产代码统一用本方法替代 NewPage, 避免漏掉某个页面跑旧画像.
// 设 XHS_BROWSER_NORMALIZE=0 可关闭纠正(回旧画像), 用于完整回滚.
func (b *Browser) NewNormalizedPage() *rod.Page {
	page := b.NewPage()
	if os.Getenv("XHS_BROWSER_NORMALIZE") != "0" {
		NormalizePage(page)
	}
	return page
}

// SaveFreshCookies 从浏览器获取当前所有 cookie 并保存到磁盘。
// 每次操作后调用，保活短期反爬 cookie（acw_tc、websectiga 等），
// 让下次请求带着新鲜的安全凭证出门。
func (b *Browser) SaveFreshCookies() {
	cks, err := b.browser.GetCookies()
	if err != nil {
		logrus.Warnf("failed to get cookies from browser: %v", err)
		return
	}
	if len(cks) == 0 {
		return
	}

	data, err := json.Marshal(cks)
	if err != nil {
		logrus.Warnf("failed to marshal cookies: %v", err)
		return
	}

	cookiePath := cookies.GetCookiesFilePath()
	if err := cookies.NewLoadCookie(cookiePath).SaveCookies(data); err != nil {
		logrus.Warnf("failed to save fresh cookies: %v", err)
	} else {
		logrus.Debugf("saved %d fresh cookies", len(cks))
	}
}

// Close 关闭浏览器、释放跨进程文件锁和进程级锁.
// 持久化 profile 时绝不调 launcher.Cleanup(), 因为它会 os.RemoveAll 整个
// user-data-dir, 删光 cookie/登录态.
func (b *Browser) Close() {
	b.browser.MustClose()
	if !b.persistentProfile {
		b.launcher.Cleanup()
	}
	b.fileLock.release()
	browserMu.Unlock()
}
