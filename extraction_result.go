package main

import "errors"

// ErrExtractionDeadline 提取任务的内部 deadline 到期。
// 通过 context.WithTimeoutCause 注入，调用方用 context.Cause(ctx) 判定。
var ErrExtractionDeadline = errors.New("extraction deadline reached")

// ErrImageLimit 触发图片数上限。PR4-a 实装触发逻辑。
var ErrImageLimit = errors.New("image limit reached")

// ExtractionStatus 提取任务的终止状态。
type ExtractionStatus string

const (
	StatusComplete       ExtractionStatus = "complete"
	StatusPartialSuccess ExtractionStatus = "partial_success"
	StatusCancelled      ExtractionStatus = "cancelled"
)

// TruncatedReason 部分成功时的截断原因。
type TruncatedReason string

const (
	TruncatedByDeadline   TruncatedReason = "deadline"
	TruncatedByImageLimit TruncatedReason = "image_limit"
)

// ImageOCRItem 单张图片的 OCR 结果。字段名与现有 handleExtractTextFromFeed 输出对齐。
type ImageOCRItem struct {
	Index     int    `json:"index"`
	Status    string `json:"status"`
	Text      string `json:"text,omitempty"`
	LineCount int    `json:"line_count,omitempty"`
	ElapsedMs int    `json:"elapsed_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ExtractionResult 提取任务的状态摘要，handler 把它的字段合并进最终 JSON。
type ExtractionResult struct {
	Status              ExtractionStatus `json:"status"`
	TotalImageCount     int              `json:"total_image_count"`
	ProcessedImageCount int              `json:"processed_image_count"`
	RemainingImageCount int              `json:"remaining_image_count"`
	Truncated           bool             `json:"truncated"`
	TruncatedReason     TruncatedReason  `json:"truncated_reason,omitempty"`
	Images              []ImageOCRItem   `json:"images"`
	CancelReason        string           `json:"cancel_reason,omitempty"`
}

// completeResult 全部图片处理完成。
func completeResult(total int, images []ImageOCRItem) ExtractionResult {
	return ExtractionResult{
		Status:              StatusComplete,
		TotalImageCount:     total,
		ProcessedImageCount: len(images),
		RemainingImageCount: total - len(images),
		Truncated:           false,
		Images:              images,
	}
}

// partialResult 因 deadline 或 image_limit 提前结束，返回已完成的图片。
func partialResult(total int, images []ImageOCRItem, reason TruncatedReason) ExtractionResult {
	return ExtractionResult{
		Status:              StatusPartialSuccess,
		TotalImageCount:     total,
		ProcessedImageCount: len(images),
		RemainingImageCount: total - len(images),
		Truncated:           true,
		TruncatedReason:     reason,
		Images:              images,
	}
}

// cancelledResult 调用方主动取消，不拼装部分结果。
func cancelledResult(total int, cause error) ExtractionResult {
	return ExtractionResult{
		Status:              StatusCancelled,
		TotalImageCount:     total,
		ProcessedImageCount: 0,
		RemainingImageCount: total,
		Truncated:           false,
		CancelReason:        cause.Error(),
		Images:              nil,
	}
}
