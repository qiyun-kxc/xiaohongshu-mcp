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
//  6. 补 plugins / Notification / connection 等 headless 暴露点
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

	// 5. 注入(IIFE 立即执行): 修 screen 物理矛盾 + languages + WebGL 去 Mac 化 + headless 暴露点
	_, _ = page.EvalOnNewDocument(normalizeJS)
}

const normalizeJS = `(() => {
  const def = (o, k, v) => { try { Object.defineProperty(o, k, { get: () => v, configurable: true }); } catch (e) {} };

  // ========== 语言 ==========
  def(navigator, 'languages', ['zh-CN', 'zh', 'en']);
  def(navigator, 'language', 'zh-CN');

  // ========== screen: 1920x1080, 保证 视口 <= avail <= screen ==========
  def(screen, 'width', 1920);
  def(screen, 'height', 1080);
  def(screen, 'availWidth', 1920);
  def(screen, 'availHeight', 1050);
  def(screen, 'colorDepth', 24);
  def(screen, 'pixelDepth', 24);
  try { Object.defineProperty(window, 'outerWidth', { get: () => window.innerWidth, configurable: true }); } catch (e) {}
  try { Object.defineProperty(window, 'outerHeight', { get: () => window.innerHeight + 88, configurable: true }); } catch (e) {}

  // ========== plugins: headless 默认为空, 真实 Chrome 有内置插件 ==========
  const makeMimeType = (type, suffixes, desc, plugin) => {
    const mt = Object.create(MimeType.prototype);
    def(mt, 'type', type);
    def(mt, 'suffixes', suffixes);
    def(mt, 'description', desc);
    def(mt, 'enabledPlugin', plugin);
    return mt;
  };

  const makePlugin = (name, desc, filename, mimeTypes) => {
    const p = Object.create(Plugin.prototype);
    def(p, 'name', name);
    def(p, 'description', desc);
    def(p, 'filename', filename);
    def(p, 'length', mimeTypes.length);
    mimeTypes.forEach((mt, i) => {
      try { Object.defineProperty(p, i, { get: () => mt, enumerable: false, configurable: true }); } catch (e) {}
    });
    return p;
  };

  const pdfPlugin = makePlugin(
    'PDF Viewer', 'Portable Document Format', 'internal-pdf-viewer',
    []
  );
  const pdfMime = makeMimeType('application/pdf', 'pdf', 'Portable Document Format', pdfPlugin);
  const xpdfMime = makeMimeType('application/x-pdf', 'pdf', '', pdfPlugin);
  try {
    Object.defineProperty(pdfPlugin, 0, { get: () => pdfMime, enumerable: false, configurable: true });
    Object.defineProperty(pdfPlugin, 1, { get: () => xpdfMime, enumerable: false, configurable: true });
    def(pdfPlugin, 'length', 2);
  } catch (e) {}

  const chromePdfPlugin = makePlugin(
    'Chrome PDF Viewer', 'Portable Document Format', 'internal-pdf-viewer',
    []
  );
  const cpdfMime = makeMimeType('application/pdf', 'pdf', 'Portable Document Format', chromePdfPlugin);
  try {
    Object.defineProperty(chromePdfPlugin, 0, { get: () => cpdfMime, enumerable: false, configurable: true });
    def(chromePdfPlugin, 'length', 1);
  } catch (e) {}

  const nativePlugin = makePlugin(
    'Chromium PDF Plugin', 'Portable Document Format', 'internal-pdf-viewer',
    []
  );
  const npdfMime = makeMimeType('application/x-google-chrome-pdf', 'pdf', 'Portable Document Format', nativePlugin);
  try {
    Object.defineProperty(nativePlugin, 0, { get: () => npdfMime, enumerable: false, configurable: true });
    def(nativePlugin, 'length', 1);
  } catch (e) {}

  const pluginList = [pdfPlugin, chromePdfPlugin, nativePlugin];

  try {
    Object.defineProperty(navigator, 'plugins', {
      get: () => {
        const pl = Object.create(PluginArray.prototype);
        def(pl, 'length', pluginList.length);
        pluginList.forEach((p, i) => {
          try { Object.defineProperty(pl, i, { get: () => p, enumerable: true, configurable: true }); } catch (e) {}
        });
        pl.item = (i) => pluginList[i] || null;
        pl.namedItem = (name) => pluginList.find(p => p.name === name) || null;
        pl.refresh = () => {};
        return pl;
      },
      configurable: true
    });
  } catch (e) {}

  // ========== connection: rtt=0 是 headless 特征 ==========
  if (navigator.connection) {
    try {
      const conn = navigator.connection;
      def(conn, 'rtt', 50);
      def(conn, 'downlink', 8.55);
      def(conn, 'effectiveType', '4g');
      def(conn, 'saveData', false);
    } catch (e) {}
  }

  // ========== Notification.permission: headless 默认 "denied" ==========
  if (typeof Notification !== 'undefined') {
    def(Notification, 'permission', 'default');
  }

  // ========== permissions.query: 补 notification 的合理返回 ==========
  if (navigator.permissions) {
    const origQuery = navigator.permissions.query.bind(navigator.permissions);
    navigator.permissions.query = (params) => {
      if (params && params.name === 'notifications') {
        return Promise.resolve({ state: 'prompt', onchange: null });
      }
      return origQuery(params);
    };
  }

  // ========== chrome 对象: headless 可能缺失 ==========
  if (!window.chrome) {
    window.chrome = {};
  }
  if (!window.chrome.runtime) {
    window.chrome.runtime = {
      connect: function() { return { onMessage: { addListener: function() {} }, postMessage: function() {} }; },
      sendMessage: function() {}
    };
  }

  // ========== WebGL: Linux Intel Mesa 集显 ==========
  const fix = (p) => {
    if (!p) return;
    const gp = p.getParameter;
    p.getParameter = function (x) {
      if (x === 37445) return 'Google Inc. (Intel)';
      if (x === 37446) return 'ANGLE (Intel, Mesa Intel(R) UHD Graphics 630 (CFL GT2), OpenGL 4.6 (Core Profile) Mesa 23.2.1)';
      return gp.apply(this, arguments);
    };
  };
  fix(window.WebGLRenderingContext && WebGLRenderingContext.prototype);
  fix(window.WebGL2RenderingContext && WebGL2RenderingContext.prototype);

  // ========== webdriver: 双重保险(stealth 可能漏掉的情况) ==========
  def(navigator, 'webdriver', false);

  // ========== hardwareConcurrency: 2核太少, 设成常见值 ==========
  if (navigator.hardwareConcurrency <= 2) {
    def(navigator, 'hardwareConcurrency', 4);
  }
})()`
