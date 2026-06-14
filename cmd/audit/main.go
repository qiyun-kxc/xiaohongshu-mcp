package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-rod/rod"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
)

func main() {
	var normalize bool
	var port int
	flag.BoolVar(&normalize, "normalize", false, "走 NewNormalizedPage 入口(受 XHS_BROWSER_NORMALIZE 开关控制)")
	flag.IntVar(&port, "port", 18090, "echo server 固定端口(localStorage 跨运行验证需同 origin)")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(r.Header)
	})
	go func() { _ = http.ListenAndServe("127.0.0.1:"+strconv.Itoa(port), mux) }()
	time.Sleep(300 * time.Millisecond)
	addr := "http://127.0.0.1:" + strconv.Itoa(port) + "/"

	b, err := browser.NewBrowser(context.Background(), true)
	if err != nil {
		log.Fatalf("failed to create browser: %v", err)
	}
	defer b.Close()

	var page *rod.Page
	if normalize {
		page = b.NewNormalizedPage() // 走生产同一入口
	} else {
		page = b.NewPage()
	}
	page.MustNavigate(addr).MustWaitLoad()
	time.Sleep(500 * time.Millisecond)

	fmt.Printf("===== REAL REQUEST HEADERS (normalize=%v) =====\n", normalize)
	fmt.Println(page.MustElement("body").MustText())

	js := `async () => {
	  function gl_() {
	    const c = document.createElement("canvas");
	    const g = c.getContext("webgl") || c.getContext("experimental-webgl");
	    if (!g) return null;
	    const d = g.getExtension("WEBGL_debug_renderer_info");
	    return {
	      vendor: d ? g.getParameter(d.UNMASKED_VENDOR_WEBGL) : g.getParameter(g.VENDOR),
	      renderer: d ? g.getParameter(d.UNMASKED_RENDERER_WEBGL) : g.getParameter(g.RENDERER),
	    };
	  }
	  return {
	    ua: navigator.userAgent,
	    platform: navigator.platform,
	    uaCH_platform: navigator.userAgentData ? navigator.userAgentData.platform : null,
	    language: navigator.language,
	    languages: navigator.languages,
	    screen: screen.width + "x" + screen.height + " inner=" + window.innerWidth + "x" + window.innerHeight,
	    intlLocale: Intl.DateTimeFormat().resolvedOptions().locale,
	    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
	    webgl: gl_(),
	  };
	}`
	fmt.Printf("\n===== JS FINGERPRINT (normalize=%v) =====\n", normalize)
	fmt.Println(page.MustEval(js).String())

	// B: profile 持久化验证 —— 读旧 marker, 再写新 marker
	old := page.MustEval(`() => localStorage.getItem('xhs_audit_marker') || '(none)'`).Str()
	page.MustEval(`() => localStorage.setItem('xhs_audit_marker', 'seeded_' + Date.now())`)
	fmt.Printf("\n===== PROFILE PERSISTENCE =====\nlocalStorage.xhs_audit_marker 本次读到的旧值: %s\n", old)
	time.Sleep(1300 * time.Millisecond)
}
