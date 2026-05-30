package browser

import (
	"regexp"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

var chromeVerRe = regexp.MustCompile(`Chrome/(\d+)\.(\d+\.\d+\.\d+)`)

// NormalizePage 在 stealth 之后, 把 stealth 弄出来的"自相矛盾"纠正回诚实的 Linux Chromium.
// 原则: 只纠正矛盾, 不新增伪装. 必须在页面导航之前调用.
//
// 纠正项:
//  1. 拆 Mac UA, 版本从浏览器自身读(不写死)
//  2. UA / platform / UA-CH 三者统一为 Linux
//  3. 语言统一 zh-CN(JS + 请求头)
//  4. 修 screen/viewport 的物理矛盾
//  5. WebGL 去 Mac 化(Linux 软渲染风格, 不碰 Windows/D3D11)
func NormalizePage(page *rod.Page) {
	_ = proto.NetworkEnable{}.Call(page)

	// 1. 真实版本号: 优先 Product(真实内核版本), UserAgent 已被 launcher/stealth 改过, 不可信
	major, full := "138", "138.0.0.0"
	if v, err := (proto.BrowserGetVersion{}).Call(page); err == nil {
		if m := chromeVerRe.FindStringSubmatch(v.Product); m != nil {
			major, full = m[1], m[1]+"."+m[2]
		} else if m := chromeVerRe.FindStringSubmatch(v.UserAgent); m != nil {
			major, full = m[1], m[1]+"."+m[2]
		}
	}

	// Linux UA, 去掉 Headless 标记; Chromium 用 Chrome token 历史上是惯例
	ua := "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + full + " Safari/537.36"

	// 2. UA-CH: binary 是 Chromium, 不塞 Google Chrome, 用 Chromium + GREASE
	meta := &proto.EmulationUserAgentMetadata{
		Brands: []*proto.EmulationUserAgentBrandVersion{
			{Brand: "Chromium", Version: major},
			{Brand: "Not(A:Brand", Version: "24"},
		},
		FullVersionList: []*proto.EmulationUserAgentBrandVersion{
			{Brand: "Chromium", Version: full},
			{Brand: "Not(A:Brand", Version: "24.0.0.0"},
		},
		Platform:        "Linux",
		PlatformVersion: "",
		Architecture:    "x86",
		Model:           "",
		Mobile:          false,
		Bitness:         "64",
	}

	// 3. UA + 语言 + platform + Client Hints 一把对齐
	_ = proto.NetworkSetUserAgentOverride{
		UserAgent:         ua,
		AcceptLanguage:    "zh-CN,zh,en",
		Platform:          "Linux x86_64",
		UserAgentMetadata: meta,
	}.Call(page)

	// Intl locale 对齐 zh-CN(时区保持系统 Asia/Tokyo, 不动)
	_ = proto.EmulationSetLocaleOverride{Locale: "zh-CN"}.Call(page)

	// 4. 普通桌面视口(消除 headless 怪尺寸)
	page.MustSetViewport(1280, 800, 1, false)

	// 5. 注入(IIFE 立即执行): 修 screen 物理矛盾 + languages + WebGL 去 Mac 化
	_, _ = page.EvalOnNewDocument(normalizeJS)
}

const normalizeJS = `(() => {
  const def = (o, k, v) => { try { Object.defineProperty(o, k, { get: () => v, configurable: true }); } catch (e) {} };

  // 语言数组
  def(navigator, 'languages', ['zh-CN', 'zh']);
  def(navigator, 'language', 'zh-CN');

  // screen: 1920x1080, 保证 视口 <= avail <= screen, 消除 innerWidth>screen.width 的物理矛盾
  def(screen, 'width', 1920);
  def(screen, 'height', 1080);
  def(screen, 'availWidth', 1920);
  def(screen, 'availHeight', 1050);
  try { Object.defineProperty(window, 'outerWidth', { get: () => window.innerWidth, configurable: true }); } catch (e) {}
  try { Object.defineProperty(window, 'outerHeight', { get: () => window.innerHeight + 88, configurable: true }); } catch (e) {}

  // WebGL 去 Mac 化: Linux ANGLE + 软渲染(llvmpipe), 与 platform=Linux 自洽, 不碰 Windows/D3D11
  const fix = (p) => {
    if (!p) return;
    const gp = p.getParameter;
    p.getParameter = function (x) {
      if (x === 37445) return 'Google Inc. (Mesa)';
      if (x === 37446) return 'ANGLE (Mesa, llvmpipe (LLVM 15.0.7, 256 bits), OpenGL 4.5 (Core Profile) Mesa 23.2.1)';
      return gp.apply(this, arguments);
    };
  };
  fix(window.WebGLRenderingContext && WebGLRenderingContext.prototype);
  fix(window.WebGL2RenderingContext && WebGL2RenderingContext.prototype);
})()`
