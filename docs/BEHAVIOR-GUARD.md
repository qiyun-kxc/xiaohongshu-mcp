## 改动全景

基于上游 xpzouying/xiaohongshu-mcp，在 `feature/behavior-guard` 分支上做了以下本地改动，目标是降低账号被小红书风控标记的风险。

### 改动分三个阶段

**阶段一：身份层（已上线）**

| commit | 内容 |
|--------|------|
| `5e36f1e` | xsec_source 自动轮换（pc_feed → pc_search → app_share） |
| `8a94799` | 轮换重试间 1-3s 随机冷却 |
| `725be0f` | 浏览器身份归一化 + 持久化 profile |
| `706e130` | gitignore 忽略 profile/锁文件 |

**阶段二：行为层（本轮新增）**

| commit | 内容 |
|--------|------|
| `e9abedc` | behavior guard 主体 — 统一延迟 + 全局节流 + 验证码检测退避 |
| `02253e0` | 搜索/浏览/用户主页加入延迟和验证码检测 |

**阶段三：环境层（本轮新增，运维侧）**

- 安装系统级 Google Chrome 148，替换 go-rod 内置的 Chromium 128
- `ROD_BROWSER_BIN=/usr/bin/google-chrome` 注入 pm2 环境变量

---

## 新增文件说明

### `xiaohongshu/behavior.go`（核心延迟系统）

- `delayConfig` / `sleepRandom` / `getScrollInterval` 从 feed_detail.go 提取为公共模块
- 新增 `sleepFixedJitter(base, jitter)` 替换固定 `time.Sleep`
- 默认用**截断正态分布**（中间密、两端稀），比均匀分布更像人
- 验证码退避：`RecordVerificationBackoff` / `ResetVerificationBackoff` / `VerificationPenaltyRemaining`（导出给 main 包用）
- 退避逻辑：指数退避 30s → 60s → 120s → 240s → 480s，封顶 10 分钟

### `xiaohongshu/verification.go`（验证码检测）

- `CheckVerification(page)` — 注入 JS 探测验证码元素和关键词
- `CheckVerificationAfterDelay(page, delay)` — 等待后检测，用于提交/点赞后
- 检测逻辑：CSS 选择器（captcha/verify/geetest/slider/risk）+ 中文关键词（滑块验证/操作频繁 等）
- **只检测和停止，不绕过验证**
- 命中后触发 `RecordVerificationBackoff`，正常页面触发 `ResetVerificationBackoff`

### `errors/verification.go`

- `VerificationError` 类型，支持 `errors.Is(err, ErrVerificationRequired)` 判断

### `operation_guard.go`（根目录，main 包）

- `guardedBrowser` 包装 `*browser.Browser`，Close 时自动 `markOperationEnd`
- 全局操作节流：`newBrowser()` 前等待 `max(正常冷却, 验证码退避惩罚)`
- 冷却参数：`XHS_OPERATION_MIN_INTERVAL_MS=1200` + `XHS_OPERATION_JITTER_MS=1000`

---

## 修改文件说明

### `service.go`
- `newBrowser()` 返回 `*guardedBrowser`，创建前自动全局冷却
- 15 个调用点不用改（通过嵌入访问 `NewNormalizedPage()` / `Close()`）

### `xiaohongshu/feed_detail.go`
- 删除局部 `delayConfig` / `sleepRandom` / `getScrollInterval` 定义（移至 behavior.go）
- `checkPageAccessible` 前置 `CheckVerification`

### `xiaohongshu/comment_feed.go`（试点替换）
- 12 处固定 `time.Sleep` → `sleepFixedJitter`
- 4 处验证码检查（导航后 ×2、提交后 ×2）
- **10ms/50ms/100ms 的 UI 轮询 sleep 保留不动**

### `xiaohongshu/search.go` / `feeds.go` / `user_profile.go`
- 导航后加 `sleepFixedJitter(800ms, 250ms)` + `CheckVerification`

---

## 踩坑点

### 1. 包结构：main 包 vs xiaohongshu 包

全局节流（`waitGlobalOperationCooldown`）必须放 main 包（因为 `service.go` 在 main 包），验证码退避状态放 xiaohongshu 包（因为 `CheckVerification` 在 xiaohongshu 包）。main 包通过导出函数 `VerificationPenaltyRemaining()` 查询退避状态。**不能反过来依赖，否则循环 import。**

### 2. newBrowser 的返回类型变了

从 `*browser.Browser` 变成 `*guardedBrowser`。因为 `guardedBrowser` 嵌入了 `*browser.Browser`，所有通过嵌入访问的方法（`NewNormalizedPage()`、`Close()`）能正常工作。但如果将来有代码把 `b` 作为 `*browser.Browser` 类型传参，会编译报错——到时候需要 `b.Browser` 取出内部类型。

### 3. 不要替换 UI 轮询 sleep

`time.Sleep(10ms)` / `time.Sleep(50ms)` / `time.Sleep(100ms)` 这些是等 DOM 渲染的轮询，不是用户节奏，不要换成 `sleepFixedJitter`。

### 4. Chrome 版本 vs go-rod 内置 Chromium

go-rod v0.116.2 内置 Chromium 128（2024 年 8 月），已经严重过时。安装了系统级 Google Chrome 148 并通过 `ROD_BROWSER_BIN` 指向它。**go-rod 更新版本时不会自动更新系统 Chrome**，需要单独维护（`apt upgrade google-chrome-stable`）。

### 5. normalize.go 自动读取 Chrome 版本

`normalize.go` 从 `proto.BrowserGetVersion{}.Call(page).Product` 动态读取实际 Chrome 版本号注入 UA，不需要硬编码版本。但 `browser.go` 里的 `defaultUserAgent` 常量还写着 `Chrome/128`——这个只影响 launcher 阶段的初始请求头，stealth + normalize 介入后会被覆盖。

### 6. 验证码检测的 JS 无法穿透 iframe

小红书的滑块验证可能在 iframe 里，`document.querySelectorAll` 看不到 iframe 内部。但代码检测了 `iframe[src*="captcha"]` 等外部标记，能发现 iframe 的存在（虽然看不到里面内容）。

### 7. 持久化 profile 的并发锁

`browser.go` 有进程级 `sync.Mutex` + 跨进程 `flock`。如果同时跑 `cmd/audit` 和生产服务，且都配了同一个 `XHS_BROWSER_USER_DATA_DIR`，会因为锁冲突 panic。audit 工具不带持久化 profile 就没事。

---

## 环境变量参考

```bash
# 行为层总开关（默认启用）
XHS_BEHAVIOR_GUARD=0          # 完整关闭行为层

# 延迟模式
XHS_DELAY_MODE=normal          # 默认：截断正态分布
XHS_DELAY_MODE=legacy          # 退回固定 sleep
XHS_DELAY_MODE=off             # 测试用：不等待

# 全局操作节流
XHS_OPERATION_THROTTLE=0       # 关闭
XHS_OPERATION_MIN_INTERVAL_MS=1200  # 操作间最小间隔
XHS_OPERATION_JITTER_MS=1000   # 间隔抖动范围

# 验证码检测
XHS_VERIFICATION_CHECK=0       # 关闭检测

# 验证码退避
XHS_VERIFICATION_BACKOFF=0     # 关闭退避
XHS_VERIFICATION_BACKOFF_BASE_MS=30000     # 基础退避 30s
XHS_VERIFICATION_BACKOFF_MAX_MS=600000     # 封顶 10min

# 验证码探测超时
XHS_VERIFICATION_PROBE_TIMEOUT_MS=1500

# 浏览器
ROD_BROWSER_BIN=/usr/bin/google-chrome     # 系统 Chrome 路径
XHS_BROWSER_USER_DATA_DIR=/opt/xhs-mcp/profile/chrome  # 持久化 profile
XHS_BROWSER_NORMALIZE=0        # 关闭身份归一化
```

---

## 后续注意

1. **Chrome 需要定期更新**：`sudo apt update && sudo apt upgrade google-chrome-stable`，否则版本又会落后。可以考虑加 cron。

2. **代理是下一步最重要的事**：当前 IP 是腾讯云数据中心（AS132203），这是被标记的最大原因。`XHS_PROXY` 环境变量已预留，配好住宅代理后加到 pm2 环境变量即可。

3. **publish.go / like_favorite.go 的固定 sleep 还没替换**：当前只读权限用不到，但将来开放写权限时需要铺开。替换规则同 comment_feed.go。

4. **WebGL renderer 仍是 llvmpipe 软件渲染**：normalize.go 设的值（`ANGLE (Mesa, llvmpipe ...)`）暴露了无 GPU 环境。如果代理上线后仍被标记，考虑改成常见桌面 GPU（如 `Intel UHD Graphics 630`）。

5. **上游合并时注意冲突**：如果 upstream 更新了 feed_detail.go 的 delay 部分，合并时 behavior.go 会冲突。处理原则：保留 behavior.go 的公共定义，删掉 upstream 新增的局部定义。

6. **回滚路径**：
   - 最彻底：`cp /opt/xhs-mcp/xiaohongshu-mcp-linux-amd64.bak-2026-05-30-183927 /opt/xhs-mcp/xiaohongshu-mcp-linux-amd64 && pm2 restart xhs-mcp`
   - 软回滚行为层：`XHS_BEHAVIOR_GUARD=0 pm2 restart xhs-mcp --update-env`
   - 只退回固定 sleep：`XHS_DELAY_MODE=legacy pm2 restart xhs-mcp --update-env`
