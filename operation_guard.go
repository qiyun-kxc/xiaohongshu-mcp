package main

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// guardedBrowser 包装 browser.Browser，在 Close 时自动标记操作结束。
// 这样 15 个 public method 不用每个都 defer markOperationEnd。
type guardedBrowser struct {
	*browser.Browser
	opName string
	closed bool
}

func (b *guardedBrowser) Close() {
	if b == nil || b.closed {
		return
	}
	b.closed = true

	// 关闭前保存 cookie：每次操作结束后把浏览器里的新鲜 cookie 存回磁盘，
	// 保活短期反爬 cookie（acw_tc、websectiga 等）。
	if b.Browser != nil {
		b.Browser.SaveFreshCookies()
	}

	// 给浏览器关闭加超时保护：Chrome 卡死时 30s 后强制释放锁，
	// 防止单次请求卡住导致全局互斥锁（browserMu）永远不释放。
	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logrus.Warnf("browser close panicked: %v", r)
			}
			close(done)
		}()
		if b.Browser != nil {
			b.Browser.Close()
		}
	}()

	select {
	case <-done:
		// 正常关闭
	case <-time.After(30 * time.Second):
		logrus.Warn("browser close timed out after 30s, forcing lock release")
	}

	markOperationEnd(b.opName)
}

// ========== 全局操作节流 ==========

var operationGuard = struct {
	sync.Mutex
	lastEnd time.Time

	rngMu sync.Mutex
	rng   *rand.Rand
}{
	rng: rand.New(rand.NewSource(time.Now().UnixNano())),
}

func behaviorGuardEnabledMain() bool {
	return !envOffMain("XHS_BEHAVIOR_GUARD")
}

func operationThrottleEnabled() bool {
	return behaviorGuardEnabledMain() && !envOffMain("XHS_OPERATION_THROTTLE")
}

// waitGlobalOperationCooldown 在每次创建 browser 前调用。
// 语义：距上一次 browser Close 至少间隔 minInterval + jitter。
// 若验证码退避生效，取退避惩罚和正常冷却中较长者。
func waitGlobalOperationCooldown(opName string) {
	if !operationThrottleEnabled() {
		return
	}

	// 初期保守。浏览器冷启动本身已有 2-5s 间隔。
	minInterval := envDurationMsMain("XHS_OPERATION_MIN_INTERVAL_MS", 1200)
	jitter := envDurationMsMain("XHS_OPERATION_JITTER_MS", 1000)

	operationGuard.Lock()
	lastEnd := operationGuard.lastEnd
	operationGuard.Unlock()

	var wait time.Duration

	if !lastEnd.IsZero() {
		target := lastEnd.Add(minInterval + randomDurationNormalMain(0, jitter))
		if w := time.Until(target); w > wait {
			wait = w
		}
	}

	// 参考验证码退避惩罚
	if penalty := xiaohongshu.VerificationPenaltyRemaining(); penalty > wait {
		wait = penalty
	}

	if wait <= 0 {
		return
	}

	logrus.Infof("operation cooldown before %s: %s", opName, wait.Round(time.Millisecond))
	time.Sleep(wait)
}

func markOperationEnd(opName string) {
	if !operationThrottleEnabled() {
		return
	}

	operationGuard.Lock()
	operationGuard.lastEnd = time.Now()
	operationGuard.Unlock()

	logrus.Debugf("operation ended: %s", opName)
}

// ========== 每日调用限制 ==========

var dailyLimit = struct {
	sync.Mutex
	date  string // "2006-01-02"
	count int
}{}

// checkDailyLimit 检查今天是否还有调用额度。
// 默认 50 次/天，通过 XHS_DAILY_LIMIT 环境变量可调。
// 返回 nil 表示放行，返回 error 表示超限。
func checkDailyLimit(opName string) error {
	if !behaviorGuardEnabledMain() || envOffMain("XHS_DAILY_LIMIT_CHECK") {
		return nil
	}

	limit := envIntMain("XHS_DAILY_LIMIT", 50)
	today := time.Now().Format("2006-01-02")

	dailyLimit.Lock()
	defer dailyLimit.Unlock()

	// 日期翻转，重置计数
	if dailyLimit.date != today {
		dailyLimit.date = today
		dailyLimit.count = 0
	}

	dailyLimit.count++

	if dailyLimit.count > limit {
		logrus.Warnf("daily limit reached: %d/%d, rejecting %s", dailyLimit.count, limit, opName)
		return fmt.Errorf("今日操作次数已达上限（%d次），明天再来吧", limit)
	}

	logrus.Debugf("daily usage: %d/%d (%s)", dailyLimit.count, limit, opName)
	return nil
}

// GetDailyUsage 返回当日已用次数和上限，供 MCP status 接口调用。
func GetDailyUsage() (used int, limit int) {
	today := time.Now().Format("2006-01-02")
	lim := envIntMain("XHS_DAILY_LIMIT", 50)

	dailyLimit.Lock()
	defer dailyLimit.Unlock()

	if dailyLimit.date != today {
		return 0, lim
	}
	return dailyLimit.count, lim
}

// ========== 随机分布 ==========

func randomDurationNormalMain(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}

	minF := float64(min)
	maxF := float64(max)
	mean := (minF + maxF) / 2
	stddev := (maxF - minF) / 6
	if stddev <= 0 {
		return min
	}

	operationGuard.rngMu.Lock()
	defer operationGuard.rngMu.Unlock()

	for i := 0; i < 8; i++ {
		v := operationGuard.rng.NormFloat64()*stddev + mean
		if v >= minF && v <= maxF {
			return time.Duration(v)
		}
	}

	v := operationGuard.rng.NormFloat64()*stddev + mean
	v = math.Max(minF, math.Min(maxF, v))
	return time.Duration(v)
}

// ========== 工具 ==========

func envOffMain(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "0" || v == "false" || v == "off" || v == "no"
}

func envDurationMsMain(name string, defaultMs int) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return time.Duration(defaultMs) * time.Millisecond
	}

	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		logrus.Warnf("invalid %s=%q, fallback to %dms", name, raw, defaultMs)
		return time.Duration(defaultMs) * time.Millisecond
	}

	return time.Duration(n) * time.Millisecond
}

func envIntMain(name string, defaultVal int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultVal
	}

	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		logrus.Warnf("invalid %s=%q, fallback to %d", name, raw, defaultVal)
		return defaultVal
	}
	return n
}
