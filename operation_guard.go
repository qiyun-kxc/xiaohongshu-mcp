package main

import (
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

	if b.Browser != nil {
		b.Browser.Close()
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
