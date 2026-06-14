package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
)

// OCRRequest worker请求。ID 用于请求/响应匹配，防止超时后 worker 迟到的回复被
// 当前请求误读。
type OCRRequest struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	URL    string `json:"url,omitempty"`
	Path   string `json:"path,omitempty"`
}

// OCRLine 单行OCR结果
type OCRLine struct {
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
}

// OCRResponse worker响应。ID 与请求 ID 严格匹配，不匹配则摧毁 worker。
type OCRResponse struct {
	ID        string    `json:"id,omitempty"`
	Status    string    `json:"status"`
	Text      string    `json:"text,omitempty"`
	Lines     []OCRLine `json:"lines,omitempty"`
	LineCount int       `json:"line_count,omitempty"`
	ElapsedMs int       `json:"elapsed_ms,omitempty"`
	Error     string    `json:"error,omitempty"`
	Engine    string    `json:"engine,omitempty"`
}

// maxOCRImages 单次提取任务的图片硬上限。超出截断到 12 张，并返回 ErrImageLimit。
const maxOCRImages = 12

// defaultOCRCooldown 图片间冷却时长，避免 worker 资源压力。
const defaultOCRCooldown = 500 * time.Millisecond

// OCRBridge 管理Python OCR worker进程。
// 任何读错误（ctx 取消 / id 不匹配 / scanner 关闭）都摧毁 worker，
// 下次 OCR 调用会重新启动一个全新的进程。
type OCRBridge struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	scanner *bufio.Scanner
	ready   bool
	reqSeq  uint64

	// startFn 测试钩子：非 nil 时 ensureStarted 用它代替真启动 worker。
	startFn func(b *OCRBridge) error
	// ocrFn 测试钩子：非 nil 时 OCRMultiple 用它代替 b.OCR，方便注入单图行为。
	ocrFn func(ctx context.Context, url string) (*OCRResponse, error)
	// cooldown 图片间冷却，0 时取 defaultOCRCooldown。测试可设短值。
	cooldown time.Duration
}

var (
	bridgeMu       sync.Mutex
	bridgeInstance *OCRBridge
)

// GetOCRBridge 获取全局 OCR bridge 单例。
// 废弃了 sync.Once：worker 崩溃后下次 OCR 调用会通过 ensureStarted 重启。
// ctx 形参为接口对齐保留，当前 init 仅构造空实例（无 IO），不消费 ctx。
func GetOCRBridge(ctx context.Context) (*OCRBridge, error) {
	_ = ctx
	bridgeMu.Lock()
	defer bridgeMu.Unlock()
	if bridgeInstance == nil {
		bridgeInstance = &OCRBridge{}
	}
	return bridgeInstance, nil
}

// OCR 对图片URL执行OCR。任何 IO 错误或超时都会摧毁底层 worker。
func (b *OCRBridge) OCR(ctx context.Context, imageURL string) (*OCRResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.ensureStarted(ctx); err != nil {
		return nil, err
	}

	id := fmt.Sprintf("%x", atomic.AddUint64(&b.reqSeq, 1))
	if err := b.writeRequest(id, imageURL); err != nil {
		b.stop()
		return nil, err
	}

	resp, err := b.readResponse(ctx, id)
	if err != nil {
		b.stop()
		return nil, err
	}
	return resp, nil
}

// ensureStarted 启动 worker（如果未启动），等待 ready 信号。
// ctx 取消则立即摧毁 worker 并回收 ready 读取 goroutine。
func (b *OCRBridge) ensureStarted(ctx context.Context) error {
	if b.ready {
		return nil
	}

	if b.startFn != nil {
		if err := b.startFn(b); err != nil {
			return err
		}
	} else {
		if err := b.startWorker(); err != nil {
			return err
		}
	}

	// 本地引用：stop() 会把 b.scanner 设 nil，goroutine 不能再访问 b.scanner
	scanner := b.scanner
	readyCh := make(chan error, 1)
	go func() {
		if !scanner.Scan() {
			readyCh <- fmt.Errorf("worker stdout closed before ready: %w", scanner.Err())
			return
		}
		var resp OCRResponse
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			readyCh <- fmt.Errorf("invalid ready signal: %w", err)
			return
		}
		if resp.Status != "ready" {
			readyCh <- fmt.Errorf("expected ready, got status=%s", resp.Status)
			return
		}
		logrus.Infof("OCR worker ready, engine: %s", resp.Engine)
		readyCh <- nil
	}()

	select {
	case err := <-readyCh:
		if err != nil {
			b.stop()
			return err
		}
		b.ready = true
		return nil
	case <-ctx.Done():
		b.stop()
		<-readyCh // 回收 goroutine，stop() 已关 stdout 让 Scan 返回
		return ctx.Err()
	}
}

func (b *OCRBridge) startWorker() error {
	pythonPath := "/home/ubuntu/ocr-env/bin/python3"
	workerPath := "/home/ubuntu/xiaohongshu-mcp/ocr/worker.py"

	b.cmd = exec.Command(pythonPath, workerPath)

	stdin, err := b.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := b.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	b.stdin = stdin
	b.stdout = stdout
	b.scanner = bufio.NewScanner(stdout)
	b.scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	if err := b.cmd.Start(); err != nil {
		return fmt.Errorf("start worker: %w", err)
	}
	return nil
}

func (b *OCRBridge) writeRequest(id, imageURL string) error {
	req := OCRRequest{ID: id, Action: "ocr_url", URL: imageURL}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = b.stdin.Write(append(data, '\n'))
	return err
}

// readResponse 是 scanner 的唯一读取入口。
// ctx 取消时摧毁 worker 让 Scan 返回，然后等 goroutine 退出再返回（防泄漏）。
func (b *OCRBridge) readResponse(ctx context.Context, expectID string) (*OCRResponse, error) {
	type readResult struct {
		resp *OCRResponse
		err  error
	}
	// 本地引用：stop() 会把 b.scanner 设 nil，goroutine 不能再访问 b.scanner
	scanner := b.scanner
	result := make(chan readResult, 1)
	go func() {
		if !scanner.Scan() {
			result <- readResult{nil, fmt.Errorf("scanner closed: %w", scanner.Err())}
			return
		}
		var resp OCRResponse
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			result <- readResult{nil, fmt.Errorf("invalid response JSON: %w", err)}
			return
		}
		if resp.ID != expectID {
			result <- readResult{nil, fmt.Errorf("id mismatch: want %s got %s", expectID, resp.ID)}
			return
		}
		result <- readResult{&resp, nil}
	}()

	select {
	case r := <-result:
		return r.resp, r.err
	case <-ctx.Done():
		b.stop()
		<-result // 回收 goroutine
		return nil, ctx.Err()
	}
}

// stop 摧毁 worker。顺序：先 Process.Kill 解除阻塞 IO，再关 pipe，最后 Wait 回收 zombie。
func (b *OCRBridge) stop() {
	b.ready = false
	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
	}
	if b.stdin != nil {
		_ = b.stdin.Close()
	}
	if b.stdout != nil {
		_ = b.stdout.Close()
	}
	if b.cmd != nil {
		_ = b.cmd.Wait()
	}
	b.cmd = nil
	b.stdin = nil
	b.stdout = nil
	b.scanner = nil
}

// OCRMultiple 对多张图片URL执行OCR（串行，带延迟）。
//
// 返回 (results, err)：
//   - 全部完成且未截断：err == nil
//   - 图片数超过 maxOCRImages，截断到 12 张后处理完：err == ErrImageLimit
//   - ctx 取消或 deadline 到期：err == ctx.Err()，results 含已完成部分
//
// 单张图片错位/超时会摧毁 worker，本张计为 ocr_failed 并继续下一张。
func (b *OCRBridge) OCRMultiple(ctx context.Context, urls []string) ([]*OCRResponse, error) {
	truncatedByLimit := len(urls) > maxOCRImages
	if truncatedByLimit {
		urls = urls[:maxOCRImages]
	}

	ocr := b.OCR
	if b.ocrFn != nil {
		ocr = b.ocrFn
	}
	cooldown := b.cooldown
	if cooldown == 0 {
		cooldown = defaultOCRCooldown
	}

	results := make([]*OCRResponse, 0, len(urls))
	for i, url := range urls {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		if i > 0 {
			select {
			case <-time.After(cooldown):
			case <-ctx.Done():
				return results, ctx.Err()
			}
		}
		shown := url
		if len(shown) > 80 {
			shown = shown[:80]
		}
		logrus.Infof("OCR图片 %d/%d: %s", i+1, len(urls), shown)
		resp, err := ocr(ctx, url)
		if err != nil {
			results = append(results, &OCRResponse{
				Status: "ocr_failed",
				Error:  err.Error(),
			})
		} else {
			results = append(results, resp)
		}
	}

	if truncatedByLimit {
		return results, ErrImageLimit
	}
	return results, nil
}
