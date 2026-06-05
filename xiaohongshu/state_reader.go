package xiaohongshu

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
)

// readAppState 间接读取页面应用状态，避免在注入JS中出现明文的全局变量名。
//
// 原理：
//  1. 变量名拆成多段拼接，静态扫描无法匹配完整字符串
//  2. 每次调用随机选一种拼接策略，避免固定模式
//  3. 通过 bracket notation (window[key]) 访问，不用 dot notation
//
// path 示例: "feed.feeds" → 读取 state.feed.feeds
//
//	"note.noteDetailMap" → 读取 state.note.noteDetailMap
func readAppState(page *rod.Page, path string) (string, error) {
	// 随机选一种变量名拼接方式
	stateAccessor := randomStateAccessor()

	// path 拆成链式 bracket access: "feed.feeds" → ['feed']['feeds']
	var chainBuf strings.Builder
	for _, seg := range strings.Split(path, ".") {
		chainBuf.WriteString(fmt.Sprintf("['%s']", seg))
	}
	chain := chainBuf.String()

	// 组装 JS：先拿到根对象 s，再沿 path 取值
	// 变量名随机化，不用固定的 s/root/state
	varName := randomVarName()

	js := fmt.Sprintf(`() => {
		var %s = %s;
		if (!%s) return "";
		var d = %s%s;
		if (!d) return "";
		if (d.value !== undefined) d = d.value;
		else if (d._value !== undefined) d = d._value;
		if (!d) return "";
		return JSON.stringify(d);
	}`, varName, stateAccessor, varName, varName, chain)

	obj, err := page.Timeout(5 * time.Second).Eval(js)
	if err != nil {
		logrus.Debugf("readAppState(%s) eval error: %v", path, err)
		return "", err
	}

	result := obj.Value.Str()
	if result == "" {
		return "", fmt.Errorf("readAppState(%s): empty result", path)
	}

	return result, nil
}

// randomStateAccessor 返回一种间接引用全局状态的JS表达式。
// 每次调用随机选择拼接策略，避免模式固定。
func randomStateAccessor() string {
	strategies := []string{
		// 策略1: 两段拼接
		`window['__INIT'+'IAL_ST'+'ATE__']`,
		// 策略2: 三段拼接
		`window['__INI'+'TIAL_'+'STATE'+'__']`,
		// 策略3: 数组join
		`window[['__IN','ITIAL','_STAT','E__'].join('')]`,
		// 策略4: charCode
		`window[String.fromCharCode(95,95,73,78,73,84,73,65,76,95,83,84,65,84,69,95,95)]`,
		// 策略5: reverse
		`window['__ETATS_LAITINI__'.split('').reverse().join('')]`,
	}

	behaviorRNG.Lock()
	idx := behaviorRNG.r.Intn(len(strategies))
	behaviorRNG.Unlock()

	return strategies[idx]
}

// randomVarName 生成随机的JS局部变量名，避免固定模式。
func randomVarName() string {
	prefixes := []string{"_r", "_d", "_v", "_s", "_t", "_q", "_p", "_m"}

	behaviorRNG.Lock()
	idx := behaviorRNG.r.Intn(len(prefixes))
	suffix := behaviorRNG.r.Intn(900) + 100
	behaviorRNG.Unlock()

	return fmt.Sprintf("%s%d", prefixes[idx], suffix)
}

// simulateHumanBrowse 在页面加载后模拟真人浏览行为。
// 在提取数据前调用，让页面活动看起来更自然。
func simulateHumanBrowse(page *rod.Page) {
	if !behaviorGuardEnabled() {
		return
	}

	// 随机决定做几个动作（1-3个）
	behaviorRNG.Lock()
	actions := behaviorRNG.r.Intn(3) + 1
	behaviorRNG.Unlock()

	for i := 0; i < actions; i++ {
		behaviorRNG.Lock()
		action := behaviorRNG.r.Intn(3)
		behaviorRNG.Unlock()

		switch action {
		case 0:
			// 小幅滚动
			scrollDelta := randInt(80, 250)
			_, _ = page.Eval(fmt.Sprintf(`() => { window.scrollBy(0, %d); }`, scrollDelta))
			sleepRandom(scrollWaitRange.min, scrollWaitRange.max)

		case 1:
			// 鼠标移到随机位置
			x := float64(randInt(100, 900))
			y := float64(randInt(100, 500))
			page.Mouse.MustMoveTo(x, y)
			sleepRandom(hoverTimeRange.min, hoverTimeRange.max)

		case 2:
			// 单纯停留"阅读"
			sleepRandom(readTimeRange.min, readTimeRange.max)
		}
	}
}

func randInt(min, max int) int {
	if max <= min {
		return min
	}
	return min + rand.Intn(max-min)
}
