package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestCompleteResult 全部图片处理完成应返回 status=complete, truncated=false。
func TestCompleteResult(t *testing.T) {
	items := []ImageOCRItem{
		{Index: 0, Status: "success", Text: "a"},
		{Index: 1, Status: "success", Text: "b"},
	}
	got := completeResult(2, items)

	if got.Status != StatusComplete {
		t.Errorf("status = %q, want %q", got.Status, StatusComplete)
	}
	if got.TotalImageCount != 2 || got.ProcessedImageCount != 2 || got.RemainingImageCount != 0 {
		t.Errorf("counts wrong: total=%d processed=%d remaining=%d", got.TotalImageCount, got.ProcessedImageCount, got.RemainingImageCount)
	}
	if got.Truncated {
		t.Errorf("Truncated should be false")
	}
	if got.TruncatedReason != "" {
		t.Errorf("TruncatedReason should be empty, got %q", got.TruncatedReason)
	}
}

// TestPartialResultByDeadline 因 deadline 部分成功。
func TestPartialResultByDeadline(t *testing.T) {
	items := []ImageOCRItem{{Index: 0, Status: "success", Text: "a"}}
	got := partialResult(5, items, TruncatedByDeadline)

	if got.Status != StatusPartialSuccess {
		t.Errorf("status = %q, want %q", got.Status, StatusPartialSuccess)
	}
	if got.TotalImageCount != 5 || got.ProcessedImageCount != 1 || got.RemainingImageCount != 4 {
		t.Errorf("counts wrong: total=%d processed=%d remaining=%d", got.TotalImageCount, got.ProcessedImageCount, got.RemainingImageCount)
	}
	if !got.Truncated {
		t.Errorf("Truncated should be true")
	}
	if got.TruncatedReason != TruncatedByDeadline {
		t.Errorf("TruncatedReason = %q, want %q", got.TruncatedReason, TruncatedByDeadline)
	}
}

// TestPartialResultByImageLimit 因图片上限部分成功（PR4-a 实装触发）。
func TestPartialResultByImageLimit(t *testing.T) {
	items := make([]ImageOCRItem, 12)
	got := partialResult(20, items, TruncatedByImageLimit)

	if got.Status != StatusPartialSuccess {
		t.Errorf("status = %q, want %q", got.Status, StatusPartialSuccess)
	}
	if got.TruncatedReason != TruncatedByImageLimit {
		t.Errorf("TruncatedReason = %q, want %q", got.TruncatedReason, TruncatedByImageLimit)
	}
	if got.RemainingImageCount != 8 {
		t.Errorf("RemainingImageCount = %d, want 8", got.RemainingImageCount)
	}
}

// TestCancelledResult 调用方主动取消，不拼装 Images。
func TestCancelledResult(t *testing.T) {
	got := cancelledResult(9, context.Canceled)

	if got.Status != StatusCancelled {
		t.Errorf("status = %q, want %q", got.Status, StatusCancelled)
	}
	if got.TotalImageCount != 9 || got.ProcessedImageCount != 0 || got.RemainingImageCount != 9 {
		t.Errorf("counts wrong: total=%d processed=%d remaining=%d", got.TotalImageCount, got.ProcessedImageCount, got.RemainingImageCount)
	}
	if got.Images != nil {
		t.Errorf("Images should be nil, got %v", got.Images)
	}
	if got.CancelReason == "" {
		t.Errorf("CancelReason should not be empty")
	}
}

// TestExtractionResultJSON 校验 JSON 字段名与拍板结构一致。
func TestExtractionResultJSON(t *testing.T) {
	r := ExtractionResult{
		Status:              StatusPartialSuccess,
		TotalImageCount:     9,
		ProcessedImageCount: 4,
		RemainingImageCount: 5,
		Truncated:           true,
		TruncatedReason:     TruncatedByDeadline,
		Images:              []ImageOCRItem{{Index: 0, Status: "success", Text: "hi"}},
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)

	expected := []string{
		`"status":"partial_success"`,
		`"total_image_count":9`,
		`"processed_image_count":4`,
		`"remaining_image_count":5`,
		`"truncated":true`,
		`"truncated_reason":"deadline"`,
		`"images":`,
	}
	for _, fragment := range expected {
		if !strings.Contains(s, fragment) {
			t.Errorf("missing %q in %s", fragment, s)
		}
	}
}

// TestContextWithDeadlineCause 验证 WithTimeoutCause + context.Cause 链路。
func TestContextWithDeadlineCause(t *testing.T) {
	ctx, cancel := context.WithTimeoutCause(context.Background(), 10*time.Millisecond, ErrExtractionDeadline)
	defer cancel()

	time.Sleep(30 * time.Millisecond)

	if !errors.Is(context.Cause(ctx), ErrExtractionDeadline) {
		t.Fatalf("context.Cause = %v, want %v", context.Cause(ctx), ErrExtractionDeadline)
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Errorf("ctx.Err = %v, want DeadlineExceeded", ctx.Err())
	}
}

// TestContextParentCancelOverridesCause 验证 parent 取消时 cause 不是 ErrExtractionDeadline。
func TestContextParentCancelOverridesCause(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())
	ctx, cancel := context.WithTimeoutCause(parent, 1*time.Second, ErrExtractionDeadline)
	defer cancel()

	parentCancel() // 调用方主动取消
	time.Sleep(5 * time.Millisecond)

	if errors.Is(context.Cause(ctx), ErrExtractionDeadline) {
		t.Errorf("parent cancel should not look like deadline; cause=%v", context.Cause(ctx))
	}
	if parent.Err() == nil {
		t.Errorf("parent.Err() should be non-nil after cancel")
	}
}

// TestOCRMultipleEarlyExitOnCancelledCtx OCRMultiple 在 ctx 已取消时应立即返回 0 个结果。
func TestOCRMultipleEarlyExitOnCancelledCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	bridge := &OCRBridge{} // 不需要 ready，因为根本进不到 OCR 调用
	results, err := bridge.OCRMultiple(ctx, []string{"https://example.com/a.jpg", "https://example.com/b.jpg"})

	if len(results) != 0 {
		t.Errorf("expected 0 results on cancelled ctx, got %d", len(results))
	}
	if err == nil {
		t.Errorf("expected ctx error on cancelled ctx, got nil")
	}
}
