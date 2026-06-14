package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// nopWriteCloser 让 io.Writer 满足 io.WriteCloser，用于 stub stdin。
type nopWriteCloser struct{ io.Writer }

func (n nopWriteCloser) Close() error { return nil }

// newStubBridgeReady 构造一个已 ready 的 bridge，scanner 从 reader 读，stdin 丢弃。
// 如果 reader 实现了 io.ReadCloser（如 io.Pipe 的 reader 端），同时赋给 b.stdout，
// 让 stop() 能真正关闭它解除阻塞读。
func newStubBridgeReady(reader io.Reader) *OCRBridge {
	b := &OCRBridge{
		stdin:   nopWriteCloser{io.Discard},
		scanner: bufio.NewScanner(reader),
		ready:   true,
	}
	if rc, ok := reader.(io.ReadCloser); ok {
		b.stdout = rc
	}
	return b
}

// TestReadResponseIDMismatchReturnsError 验证响应 id 与期望不匹配时立刻返错。
// 这是第 4 条修复的核心：错位响应不能被当前请求误收。
func TestReadResponseIDMismatchReturnsError(t *testing.T) {
	body := `{"id":"wrong","status":"success","text":"hi"}` + "\n"
	b := newStubBridgeReady(strings.NewReader(body))

	_, err := b.readResponse(context.Background(), "expected")
	if err == nil {
		t.Fatal("expected id mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "id mismatch") {
		t.Fatalf("expected id mismatch error, got: %v", err)
	}
}

// TestReadResponseSuccess 验证正常 id 匹配路径解析正确。
func TestReadResponseSuccess(t *testing.T) {
	body := `{"id":"abc","status":"success","text":"hello"}` + "\n"
	b := newStubBridgeReady(strings.NewReader(body))

	resp, err := b.readResponse(context.Background(), "abc")
	if err != nil {
		t.Fatalf("expected success, got err: %v", err)
	}
	if resp.Text != "hello" {
		t.Fatalf("expected text=hello, got: %s", resp.Text)
	}
}

// TestReadResponseCtxCancelRecoversGoroutine 验证 ctx 取消时 readResponse 立刻
// 返回，且不会泄漏 goroutine（关键：第 4 条 codex 指出的旧 bug）。
func TestReadResponseCtxCancelRecoversGoroutine(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutW.Close()
	b := newStubBridgeReady(stdoutR)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := b.readResponse(ctx, "anything")
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected ctx error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readResponse did not return after ctx cancel — goroutine leaked")
	}
}

// TestEnsureStartedCtxCancelDuringReadyWait 验证 ensureStarted 等 ready 信号时
// ctx 取消立刻返回（第 6 条修复：旧 b.scanner.Scan() 等 ready 没超时）。
func TestEnsureStartedCtxCancelDuringReadyWait(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	defer stdoutW.Close()
	b := &OCRBridge{
		startFn: func(bridge *OCRBridge) error {
			bridge.stdout = stdoutR
			bridge.scanner = bufio.NewScanner(stdoutR)
			bridge.stdin = nopWriteCloser{io.Discard}
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		done <- b.ensureStarted(ctx)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected ctx error, got nil")
		}
		if b.ready {
			t.Fatal("ready should be false after ctx cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ensureStarted did not return after ctx cancel")
	}
}

// TestStopHandlesNilFieldsGracefully 验证 stop 在 cmd/stdin/stdout 都为 nil 时
// 不 panic，并将 ready 置 false。
func TestStopHandlesNilFieldsGracefully(t *testing.T) {
	b := &OCRBridge{ready: true}
	b.stop()
	if b.ready {
		t.Fatal("expected ready=false after stop")
	}
	if b.cmd != nil || b.stdin != nil || b.stdout != nil || b.scanner != nil {
		t.Fatal("expected all fields nil after stop")
	}
}

// TestOCRMultipleTruncatedByImageLimit 验证图片数超过 maxOCRImages 时截断到 12 张，
// 并返回 ErrImageLimit，让 handler 走 image_limit 分支。
func TestOCRMultipleTruncatedByImageLimit(t *testing.T) {
	urls := make([]string, 15)
	for i := range urls {
		urls[i] = "http://example.com/img" + string(rune('a'+i)) + ".jpg"
	}
	called := 0
	b := &OCRBridge{
		cooldown: time.Millisecond, // 加速测试
		ocrFn: func(ctx context.Context, url string) (*OCRResponse, error) {
			called++
			return &OCRResponse{Status: "success", Text: "ok"}, nil
		},
	}

	results, err := b.OCRMultiple(context.Background(), urls)
	if err == nil || err.Error() != ErrImageLimit.Error() {
		t.Fatalf("expected ErrImageLimit, got: %v", err)
	}
	if len(results) != maxOCRImages {
		t.Fatalf("expected %d results, got %d", maxOCRImages, len(results))
	}
	if called != maxOCRImages {
		t.Fatalf("expected ocrFn called %d times, got %d", maxOCRImages, called)
	}
}

// TestOCRMultipleSingleImageFailContinues 验证单图失败不打断循环，
// 失败的图记 ocr_failed + Error 字段，后续图继续处理。
func TestOCRMultipleSingleImageFailContinues(t *testing.T) {
	urls := []string{"u1", "u2", "u3"}
	b := &OCRBridge{
		cooldown: time.Millisecond,
		ocrFn: func(ctx context.Context, url string) (*OCRResponse, error) {
			if url == "u2" {
				return nil, fmt.Errorf("simulated worker crash")
			}
			return &OCRResponse{Status: "success", Text: "ok-" + url}, nil
		},
	}

	results, err := b.OCRMultiple(context.Background(), urls)
	if err != nil {
		t.Fatalf("expected nil error (single img fail should not propagate), got: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[1].Status != "ocr_failed" || results[1].Error == "" {
		t.Fatalf("expected u2 to be ocr_failed with error, got %+v", results[1])
	}
	if results[0].Text != "ok-u1" || results[2].Text != "ok-u3" {
		t.Fatalf("expected u1/u3 success, got %+v / %+v", results[0], results[2])
	}
}

// TestOCRMultipleParentCancel 验证 parent ctx 取消时 OCRMultiple 立刻返回，
// results 含已完成的图、err 为 ctx.Err()。
func TestOCRMultipleParentCancel(t *testing.T) {
	urls := []string{"u1", "u2", "u3", "u4"}
	ctx, cancel := context.WithCancel(context.Background())
	called := 0
	b := &OCRBridge{
		cooldown: time.Millisecond,
		ocrFn: func(ctx context.Context, url string) (*OCRResponse, error) {
			called++
			if called == 2 {
				cancel() // 在第二张完成后取消
			}
			return &OCRResponse{Status: "success", Text: url}, nil
		},
	}

	results, err := b.OCRMultiple(ctx, urls)
	if err == nil {
		t.Fatal("expected ctx error, got nil")
	}
	if len(results) < 2 || len(results) > 3 {
		t.Fatalf("expected 2-3 results (cancel after #2), got %d", len(results))
	}
}

// TestOCRMultipleDeadlineExit 验证内部 deadline 到期时 OCRMultiple 提前返回，
// err 是 ctx.Err()，ctx.Cause 是 ErrExtractionDeadline（让 handler 走 deadline 分支）。
func TestOCRMultipleDeadlineExit(t *testing.T) {
	urls := []string{"u1", "u2", "u3", "u4", "u5"}
	ctx, cancel := context.WithTimeoutCause(
		context.Background(),
		50*time.Millisecond,
		ErrExtractionDeadline,
	)
	defer cancel()

	b := &OCRBridge{
		cooldown: 30 * time.Millisecond, // 几张后 deadline 就到
		ocrFn: func(ctx context.Context, url string) (*OCRResponse, error) {
			return &OCRResponse{Status: "success", Text: url}, nil
		},
	}

	results, err := b.OCRMultiple(ctx, urls)
	if err == nil {
		t.Fatal("expected deadline error, got nil")
	}
	if len(results) >= len(urls) {
		t.Fatalf("expected partial results, got all %d", len(results))
	}
	// handler 用 errors.Is(ctx.Cause, ErrExtractionDeadline) 判定
	cause := context.Cause(ctx)
	if !errors.Is(cause, ErrExtractionDeadline) {
		t.Fatalf("expected cause=ErrExtractionDeadline, got: %v", cause)
	}
}

// TestOCRReentryAfterStop 验证 stop 后再调 OCR，ensureStarted 重新走（验证
// sync.Once 废弃后允许重启）。用 startFn 钩子模拟两次启动 + 成功响应。
func TestOCRReentryAfterStop(t *testing.T) {
	// 第二次启动时用的 pipe
	stdoutR2, stdoutW2 := io.Pipe()
	defer stdoutW2.Close()

	startCalls := 0
	b := &OCRBridge{
		startFn: func(bridge *OCRBridge) error {
			startCalls++
			bridge.stdout = stdoutR2
			bridge.scanner = bufio.NewScanner(stdoutR2)
			bridge.stdin = nopWriteCloser{io.Discard}
			return nil
		},
	}

	// 模拟第一次 stop 后的状态（ready=false）
	b.stop()

	// 异步写 ready + response 信号
	go func() {
		_, _ = stdoutW2.Write([]byte(`{"status":"ready","engine":"test"}` + "\n"))
		_, _ = stdoutW2.Write([]byte(`{"id":"1","status":"success","text":"ok"}` + "\n"))
	}()

	resp, err := b.OCR(context.Background(), "http://example.com/img.jpg")
	if err != nil {
		t.Fatalf("OCR after stop failed: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("expected text=ok, got: %s", resp.Text)
	}
	if startCalls != 1 {
		t.Fatalf("expected startFn called once on reentry, got: %d", startCalls)
	}
	if !b.ready {
		t.Fatal("expected ready=true after successful OCR")
	}
}
