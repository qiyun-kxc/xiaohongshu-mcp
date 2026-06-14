package browser

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestAcquireBrowserImmediateCancel: ctx 已取消时 acquire 立即返错, 不进队列.
func TestAcquireBrowserImmediateCancel(t *testing.T) {
	// 占满唯一槽位
	if err := acquireBrowser(context.Background()); err != nil {
		t.Fatalf("preflight acquire failed: %v", err)
	}
	t.Cleanup(releaseBrowser)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := acquireBrowser(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected ctx error, got nil")
	}
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("acquireBrowser returned too late: %v", elapsed)
	}
}

// TestAcquireBrowserCancelDuringWait: 等锁过程中 ctx 取消, 立即返错.
func TestAcquireBrowserCancelDuringWait(t *testing.T) {
	if err := acquireBrowser(context.Background()); err != nil {
		t.Fatalf("preflight acquire failed: %v", err)
	}
	t.Cleanup(releaseBrowser)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- acquireBrowser(ctx)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("acquireBrowser did not return after ctx cancel")
	}
}

// TestCloseReleasesLockOnPanic: 即使 closeFn panic, Close 也必须经 defer 释放槽位.
// 这是修第 3 条 (operation_guard 假强制释放) 的核心保证.
func TestCloseReleasesLockOnPanic(t *testing.T) {
	if err := acquireBrowser(context.Background()); err != nil {
		t.Fatalf("preflight acquire failed: %v", err)
	}

	b := &Browser{
		closeFn: func() {
			panic("simulated rod close panic")
		},
	}
	b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := acquireBrowser(ctx); err != nil {
		t.Fatalf("lock not released after Close panic: %v", err)
	}
	releaseBrowser()
}

// TestCloseTimeoutReleasesLock: closeFn 永久阻塞时, 超过 closeTimeout 后必须释放槽位.
// 修第 3 条的另一面: rod 卡死也不能锁全局.
func TestCloseTimeoutReleasesLock(t *testing.T) {
	if err := acquireBrowser(context.Background()); err != nil {
		t.Fatalf("preflight acquire failed: %v", err)
	}

	blockCh := make(chan struct{})
	t.Cleanup(func() { close(blockCh) })

	var closeCalled atomic.Bool
	b := &Browser{
		closeFn: func() {
			closeCalled.Store(true)
			<-blockCh
		},
		closeTimeout: 50 * time.Millisecond,
	}

	start := time.Now()
	b.Close()
	elapsed := time.Since(start)

	if !closeCalled.Load() {
		t.Fatal("closeFn was never called")
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("Close did not respect timeout, took %v", elapsed)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := acquireBrowser(ctx); err != nil {
		t.Fatalf("lock not released after Close timeout: %v", err)
	}
	releaseBrowser()
}

// TestReleaseBrowserDoubleReleaseSafe: 重复释放走 default 分支, 不阻塞不 panic.
func TestReleaseBrowserDoubleReleaseSafe(t *testing.T) {
	if err := acquireBrowser(context.Background()); err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	releaseBrowser()
	releaseBrowser() // 已空, 应安全
}

// TestNewBrowserCtxCancel: NewBrowser 在拿槽位前 ctx 已取消, 立即返错不启动 chrome.
func TestNewBrowserCtxCancel(t *testing.T) {
	if err := acquireBrowser(context.Background()); err != nil {
		t.Fatalf("preflight acquire failed: %v", err)
	}
	t.Cleanup(releaseBrowser)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	b, err := NewBrowser(ctx, true)
	elapsed := time.Since(start)

	if b != nil {
		t.Fatal("expected nil browser on ctx cancel")
	}
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("NewBrowser hung past ctx cancel: %v", elapsed)
	}
}
