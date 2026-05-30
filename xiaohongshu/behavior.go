package xiaohongshu

import (
	"math"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// delayConfig 以毫秒为单位的延迟范围。
// 保留原名，避免 feed_detail.go 等已有调用大面积改动。
type delayConfig struct {
	min int
	max int
}

var (
	humanDelayRange   = delayConfig{300, 700}
	reactionTimeRange = delayConfig{300, 800}
	hoverTimeRange    = delayConfig{100, 300}
	readTimeRange     = delayConfig{500, 1200}
	shortReadRange    = delayConfig{600, 1200}
	scrollWaitRange   = delayConfig{100, 200}
	postScrollRange   = delayConfig{300, 500}
)

var behaviorRNG = struct {
	sync.Mutex
	r *rand.Rand
}{
	r: rand.New(rand.NewSource(time.Now().UnixNano())),
}

// ========== 环境变量 / 开关 ==========

func behaviorGuardEnabled() bool {
	return !envOff("XHS_BEHAVIOR_GUARD")
}

func delayMode() string {
	if !behaviorGuardEnabled() {
		return "legacy"
	}

	mode := strings.ToLower(strings.TrimSpace(os.Getenv("XHS_DELAY_MODE")))
	switch mode {
	case "", "normal":
		return "normal"
	case "legacy", "uniform", "off":
		return mode
	default:
		logrus.Warnf("unknown XHS_DELAY_MODE=%q, fallback to normal", mode)
		return "normal"
	}
}

func verificationBackoffEnabled() bool {
	return behaviorGuardEnabled() && !envOff("XHS_VERIFICATION_BACKOFF")
}

// ========== 延迟函数 ==========

// sleepRandom 保留原函数名。
// 默认 normal: 截断正态分布。
// legacy / uniform: 旧均匀分布。
// off: 测试用，不等待。
func sleepRandom(minMs, maxMs int) {
	if minMs < 0 {
		minMs = 0
	}
	if maxMs < minMs {
		maxMs = minMs
	}

	switch delayMode() {
	case "off":
		return
	case "legacy", "uniform":
		time.Sleep(randomDurationUniform(
			time.Duration(minMs)*time.Millisecond,
			time.Duration(maxMs)*time.Millisecond,
		))
	default:
		time.Sleep(randomDurationNormal(
			time.Duration(minMs)*time.Millisecond,
			time.Duration(maxMs)*time.Millisecond,
		))
	}
}

// sleepFixedJitter 用来替换"语义等待"的固定 time.Sleep。
// 不要替换 10ms/50ms/100ms 这种 UI 轮询。
// XHS_DELAY_MODE=legacy 时退回精确固定 sleep，完整回滚。
func sleepFixedJitter(base, jitter time.Duration) {
	if base < 0 {
		base = 0
	}
	if jitter < 0 {
		jitter = 0
	}

	switch delayMode() {
	case "off":
		return
	case "legacy":
		time.Sleep(base)
	case "uniform":
		min := base - jitter
		max := base + jitter
		if min < 0 {
			min = 0
		}
		time.Sleep(randomDurationUniform(min, max))
	default:
		min := base - jitter
		max := base + jitter
		if min < 0 {
			min = 0
		}
		time.Sleep(randomDurationNormal(min, max))
	}
}

func getScrollInterval(speed string) time.Duration {
	switch speed {
	case "slow":
		return sampledDelay(1200*time.Millisecond, 1500*time.Millisecond)
	case "fast":
		return sampledDelay(300*time.Millisecond, 400*time.Millisecond)
	default:
		return sampledDelay(600*time.Millisecond, 800*time.Millisecond)
	}
}

func sampledDelay(min, max time.Duration) time.Duration {
	switch delayMode() {
	case "off":
		return 0
	case "legacy", "uniform":
		return randomDurationUniform(min, max)
	default:
		return randomDurationNormal(min, max)
	}
}

// ========== 验证码退避 ==========

var verifyBackoff = struct {
	sync.Mutex
	hits         int
	penaltyUntil time.Time
}{}

// RecordVerificationBackoff 在验证码检测命中时调用。
// 不自动重试，只把后续操作冷却窗口拉长。
func RecordVerificationBackoff(reason string) {
	if !verificationBackoffEnabled() {
		return
	}

	base := envDurationMs("XHS_VERIFICATION_BACKOFF_BASE_MS", 30_000)
	maxBackoff := envDurationMs("XHS_VERIFICATION_BACKOFF_MAX_MS", 10*60*1000)

	verifyBackoff.Lock()
	defer verifyBackoff.Unlock()

	verifyBackoff.hits++

	multiplier := 1 << minInt(verifyBackoff.hits-1, 4) // 1, 2, 4, 8, 16
	backoff := time.Duration(multiplier) * base
	if backoff > maxBackoff {
		backoff = maxBackoff
	}

	verifyBackoff.penaltyUntil = time.Now().Add(backoff)

	logrus.Warnf(
		"verification backoff: hits=%d backoff=%s reason=%s",
		verifyBackoff.hits,
		backoff.Round(time.Millisecond),
		reason,
	)
}

// ResetVerificationBackoff 当操作正常完成（无验证码）时归零。
func ResetVerificationBackoff() {
	verifyBackoff.Lock()
	verifyBackoff.hits = 0
	verifyBackoff.Unlock()
}

// VerificationPenaltyRemaining 返回当前验证码惩罚剩余时间。
// 导出给 main 包的 operation_guard 在全局节流时参考。
func VerificationPenaltyRemaining() time.Duration {
	if !verificationBackoffEnabled() {
		return 0
	}

	verifyBackoff.Lock()
	until := verifyBackoff.penaltyUntil
	verifyBackoff.Unlock()

	remaining := time.Until(until)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// ========== 随机分布 ==========

func randomDurationUniform(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}

	behaviorRNG.Lock()
	n := behaviorRNG.r.Int63n(int64(max - min))
	behaviorRNG.Unlock()

	return min + time.Duration(n)
}

// randomDurationNormal 返回 [min, max] 内的截断正态分布。
// mean = 中点，stddev = 宽度 / 6，使绝大多数样本自然落在边界内。
func randomDurationNormal(min, max time.Duration) time.Duration {
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

	behaviorRNG.Lock()
	defer behaviorRNG.Unlock()

	for i := 0; i < 8; i++ {
		v := behaviorRNG.r.NormFloat64()*stddev + mean
		if v >= minF && v <= maxF {
			return time.Duration(v)
		}
	}

	v := behaviorRNG.r.NormFloat64()*stddev + mean
	v = math.Max(minF, math.Min(maxF, v))
	return time.Duration(v)
}

// ========== 工具 ==========

func envOff(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "0" || v == "false" || v == "off" || v == "no"
}

func envDurationMs(name string, defaultMs int) time.Duration {
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
