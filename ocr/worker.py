#!/usr/bin/env python3
"""OCR Worker - 常驻进程，stdin/stdout JSON通信
Go服务启动此进程，通过stdin发送请求，stdout接收结果。
每行一个JSON请求，每行一个JSON响应。
"""
import sys
import json
import time
import urllib.request
import tempfile
import os

# 初始化OCR引擎（只加载一次）
from rapidocr_onnxruntime import RapidOCR
ocr = RapidOCR()

def process_image(image_path):
    """对单张图片执行OCR"""
    try:
        start = time.time()
        result, elapse = ocr(image_path)
        elapsed_ms = int((time.time() - start) * 1000)
        
        if not result:
            return {
                "status": "empty_text",
                "text": "",
                "lines": [],
                "elapsed_ms": elapsed_ms
            }
        
        lines = []
        texts = []
        for line in result:
            bbox, text, confidence = line
            texts.append(text)
            lines.append({
                "text": text,
                "confidence": round(confidence, 3)
            })
        
        return {
            "status": "success",
            "text": "\n".join(texts),
            "lines": lines,
            "line_count": len(lines),
            "elapsed_ms": elapsed_ms
        }
    except Exception as e:
        return {
            "status": "ocr_failed",
            "text": "",
            "lines": [],
            "error": str(e),
            "elapsed_ms": 0
        }

def download_image(url, timeout=10):
    """下载图片到临时文件"""
    try:
        tmp = tempfile.NamedTemporaryFile(suffix=".webp", delete=False)
        urllib.request.urlretrieve(url, tmp.name)
        return tmp.name, None
    except Exception as e:
        return None, str(e)

def handle_request(req):
    """处理单个请求"""
    action = req.get("action", "ocr")
    
    if action == "ping":
        return {"status": "pong"}
    
    if action == "ocr_url":
        url = req.get("url", "")
        if not url:
            return {"status": "error", "error": "missing url"}
        
        # 下载图片
        path, err = download_image(url)
        if err:
            return {"status": "download_failed", "error": err}
        
        try:
            result = process_image(path)
            return result
        finally:
            os.unlink(path)
    
    elif action == "ocr_file":
        path = req.get("path", "")
        if not path or not os.path.exists(path):
            return {"status": "error", "error": f"file not found: {path}"}
        return process_image(path)
    
    return {"status": "error", "error": f"unknown action: {action}"}

def main():
    # 通知Go服务worker已就绪
    sys.stdout.write(json.dumps({"status": "ready", "engine": "rapidocr-onnxruntime"}) + "\n")
    sys.stdout.flush()
    
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        
        try:
            req = json.loads(line)
            resp = handle_request(req)
        except json.JSONDecodeError as e:
            resp = {"status": "error", "error": f"invalid JSON: {e}"}
        except Exception as e:
            resp = {"status": "error", "error": f"unexpected: {e}"}
        
        sys.stdout.write(json.dumps(resp, ensure_ascii=False) + "\n")
        sys.stdout.flush()

if __name__ == "__main__":
    main()
