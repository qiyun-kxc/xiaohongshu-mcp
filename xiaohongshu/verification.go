package xiaohongshu

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
	xherrors "github.com/xpzouying/xiaohongshu-mcp/errors"
)

type verificationProbeResult struct {
	Reason string `json:"reason"`
	URL    string `json:"url"`
}

func verificationCheckEnabled() bool {
	return behaviorGuardEnabled() && !envOff("XHS_VERIFICATION_CHECK")
}

// CheckVerification 检测验证码/安全验证页面。
// 命中后触发退避并返回 *errors.VerificationError。
// 只识别和停止，不绕过验证。
func CheckVerification(page *rod.Page) error {
	if page == nil || !verificationCheckEnabled() {
		return nil
	}

	result, ok := detectVerification(page)
	if !ok || strings.TrimSpace(result.Reason) == "" {
		// 正常页面，归零退避计数
		ResetVerificationBackoff()
		return nil
	}

	// 触发退避
	RecordVerificationBackoff(result.Reason)

	err := &xherrors.VerificationError{
		Reason: result.Reason,
		URL:    result.URL,
	}

	logrus.Warnf("xiaohongshu verification required: %v", err)
	return err
}

// CheckVerificationAfterDelay 等待一段时间后检测验证码。
// 用于提交/点赞等动作之后。
func CheckVerificationAfterDelay(page *rod.Page, delay time.Duration) error {
	if delay > 0 {
		sleepFixedJitter(delay, delay/4)
	}
	return CheckVerification(page)
}

func detectVerification(page *rod.Page) (verificationProbeResult, bool) {
	probeTimeout := envDurationMs("XHS_VERIFICATION_PROBE_TIMEOUT_MS", 1500)

	js := `() => {
		const keywords = [
			'滑块验证',
			'拖动滑块',
			'请完成安全验证',
			'安全验证',
			'人机验证',
			'验证码',
			'访问异常',
			'环境异常',
			'账号异常',
			'操作频繁',
			'请稍后再试'
		];

		const selectors = [
			'[class*="captcha" i]',
			'[id*="captcha" i]',
			'[class*="verify" i]',
			'[id*="verify" i]',
			'[class*="slider" i]',
			'[id*="slider" i]',
			'[class*="geetest" i]',
			'[id*="geetest" i]',
			'[class*="risk" i]',
			'[id*="risk" i]',
			'iframe[src*="captcha" i]',
			'iframe[src*="verify" i]',
			'iframe[src*="geetest" i]',
			'iframe[title*="验证" i]',
			'iframe[name*="verify" i]'
		];

		const visible = (el) => {
			if (!el) return false;
			const style = window.getComputedStyle(el);
			if (!style || style.display === 'none' || style.visibility === 'hidden' || Number(style.opacity) === 0) {
				return false;
			}
			const rect = el.getBoundingClientRect();
			return rect.width > 8 && rect.height > 8;
		};

		const attrs = (el) => [
			el.innerText || '',
			el.textContent || '',
			el.getAttribute('aria-label') || '',
			el.getAttribute('title') || '',
			el.getAttribute('name') || '',
			el.getAttribute('src') || '',
			String(el.className || ''),
			String(el.id || '')
		].join(' ');

		for (const selector of selectors) {
			let nodes = [];
			try {
				nodes = Array.from(document.querySelectorAll(selector));
			} catch (_) {}

			for (const node of nodes) {
				const text = attrs(node);
				const hitKeyword = keywords.find(k => text.includes(k));

				if ((visible(node) || node.tagName === 'IFRAME') && hitKeyword) {
					return JSON.stringify({
						reason: 'verification element: ' + selector + ' keyword=' + hitKeyword,
						url: location.href
					});
				}

				if (
					(visible(node) || node.tagName === 'IFRAME') &&
					/captcha|geetest/i.test(text)
				) {
					return JSON.stringify({
						reason: 'verification-like element: ' + selector,
						url: location.href
					});
				}
			}
		}

		const bodyText = (document.body && document.body.innerText) ? document.body.innerText.trim() : '';
		if (bodyText && bodyText.length < 2500) {
			const hitKeyword = keywords.find(k => bodyText.includes(k));
			if (hitKeyword) {
				return JSON.stringify({
					reason: 'verification page text keyword=' + hitKeyword,
					url: location.href
				});
			}
		}

		return JSON.stringify({ reason: '', url: location.href });
	}`

	obj, err := page.Timeout(probeTimeout).Eval(js)
	if err != nil || obj == nil {
		if err != nil {
			logrus.Debugf("verification probe skipped: %v", err)
		}
		return verificationProbeResult{}, false
	}

	raw := obj.Value.Str()
	if raw == "" {
		return verificationProbeResult{}, false
	}

	var result verificationProbeResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		logrus.Debugf("verification probe decode failed: raw=%q err=%v", raw, err)
		return verificationProbeResult{}, false
	}

	return result, true
}
