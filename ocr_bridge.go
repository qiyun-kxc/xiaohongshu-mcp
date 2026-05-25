package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// OCRRequest worker请求
type OCRRequest struct {
	Action string `json:"action"`
	URL    string `json:"url,omitempty"`
	Path   string `json:"path,omitempty"`
}

// OCRLine 单行OCR结果
type OCRLine struct {
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
}

// OCRResponse worker响应
type OCRResponse struct {
	Status    string    `json:"status"`
	Text      string    `json:"text,omitempty"`
	Lines     []OCRLine `json:"lines,omitempty"`
	LineCount int       `json:"line_count,omitempty"`
	ElapsedMs int       `json:"elapsed_ms,omitempty"`
	Error     string    `json:"error,omitempty"`
	Engine    string    `json:"engine,omitempty"`
}

// OCRBridge 管理Python OCR worker进程
type OCRBridge struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	ready   bool
}

var ocrBridge *OCRBridge
var ocrOnce sync.Once

// GetOCRBridge 获取全局OCR bridge实例（懒初始化）
func GetOCRBridge() (*OCRBridge, error) {
	var initErr error
	ocrOnce.Do(func() {
		bridge := &OCRBridge{}
		if err := bridge.start(); err != nil {
			initErr = err
			return
		}
		ocrBridge = bridge
	})
	if initErr != nil {
		return nil, initErr
	}
	if ocrBridge == nil {
		return nil, fmt.Errorf("OCR bridge not initialized")
	}
	return ocrBridge, nil
}

func (b *OCRBridge) start() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	pythonPath := "/home/ubuntu/ocr-env/bin/python3"
	workerPath := "/home/ubuntu/xiaohongshu-mcp/ocr/worker.py"

	b.cmd = exec.Command(pythonPath, workerPath)

	var err error
	b.stdin, err = b.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	stdout, err := b.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	b.scanner = bufio.NewScanner(stdout)
	b.scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer

	if err := b.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start OCR worker: %w", err)
	}

	// 等待 ready 信号
	if b.scanner.Scan() {
		var resp OCRResponse
		if err := json.Unmarshal(b.scanner.Bytes(), &resp); err == nil && resp.Status == "ready" {
			b.ready = true
			logrus.Infof("OCR worker ready, engine: %s", resp.Engine)
			return nil
		}
	}

	return fmt.Errorf("OCR worker failed to send ready signal")
}

// OCR 对图片URL执行OCR
func (b *OCRBridge) OCR(imageURL string) (*OCRResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.ready {
		return nil, fmt.Errorf("OCR worker not ready")
	}

	req := OCRRequest{Action: "ocr_url", URL: imageURL}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	// 发送请求
	if _, err := b.stdin.Write(append(reqBytes, '\n')); err != nil {
		return nil, fmt.Errorf("failed to write to OCR worker: %w", err)
	}

	// 读取响应（带超时）
	done := make(chan *OCRResponse, 1)
	errCh := make(chan error, 1)

	go func() {
		if b.scanner.Scan() {
			var resp OCRResponse
			if err := json.Unmarshal(b.scanner.Bytes(), &resp); err != nil {
				errCh <- err
				return
			}
			done <- &resp
		} else {
			errCh <- fmt.Errorf("OCR worker closed unexpectedly")
		}
	}()

	select {
	case resp := <-done:
		return resp, nil
	case err := <-errCh:
		return nil, err
	case <-time.After(60 * time.Second):
		return nil, fmt.Errorf("OCR timeout after 60s")
	}
}

// OCRMultiple 对多张图片URL执行OCR（串行，带延迟）
func (b *OCRBridge) OCRMultiple(urls []string) []*OCRResponse {
	results := make([]*OCRResponse, len(urls))
	for i, url := range urls {
		if i > 0 {
			time.Sleep(500 * time.Millisecond) // 图片间延迟
		}
		logrus.Infof("OCR图片 %d/%d: %s", i+1, len(urls), url[:80])
		resp, err := b.OCR(url)
		if err != nil {
			results[i] = &OCRResponse{
				Status: "ocr_failed",
				Error:  err.Error(),
			}
		} else {
			results[i] = resp
		}
	}
	return results
}
