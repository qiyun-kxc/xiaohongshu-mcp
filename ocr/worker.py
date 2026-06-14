#!/usr/bin/env python3
"""OCR Worker - 常驻进程，stdin/stdout JSON通信
Go服务启动此进程，通过stdin发送请求，stdout接收结果。
每行一个JSON请求，每行一个JSON响应。
"""
import contextlib
import sys
import json
import time
import urllib.request
import tempfile
import os

# 下载安全限制
MAX_IMAGE_BYTES = 15 * 1024 * 1024  # 单图上限 15MB
ALLOWED_TYPES = {"image/jpeg", "image/png", "image/webp"}

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

def _download_image_safe(url, timeout=15):
    """安全下载图片：显式超时、大小上限、Content-Type 白名单。失败抛异常。"""
    request = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0"})
    path = None
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            content_type = response.headers.get_content_type()
            if content_type not in ALLOWED_TYPES:
                raise ValueError(f"unsupported content type: {content_type}")
            with tempfile.NamedTemporaryFile(delete=False) as tmp:
                path = tmp.name
                total = 0
                while True:
                    chunk = response.read(64 * 1024)
                    if not chunk:
                        break
                    total += len(chunk)
                    if total > MAX_IMAGE_BYTES:
                        raise ValueError("image exceeds size limit")
                    tmp.write(chunk)
        return path
    except Exception:
        if path:
            with contextlib.suppress(OSError):
                os.unlink(path)
        raise


def download_image(url, timeout=15):
    """下载图片到临时文件。返回 (path, err)；保留旧契约以便 handle_request 不变。"""
    try:
        return _download_image_safe(url, timeout=timeout), None
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

        req_id = None
        try:
            req = json.loads(line)
            req_id = req.get("id")
            resp = handle_request(req)
        except json.JSONDecodeError as e:
            resp = {"status": "error", "error": f"invalid JSON: {e}"}
        except Exception as e:
            resp = {"status": "error", "error": f"unexpected: {e}"}

        # 协议：请求带 id 时响应必须回 id，让 Go 端校验匹配防错位
        if req_id is not None:
            resp["id"] = req_id

        sys.stdout.write(json.dumps(resp, ensure_ascii=False) + "\n")
        sys.stdout.flush()

if __name__ == "__main__":
    main()
