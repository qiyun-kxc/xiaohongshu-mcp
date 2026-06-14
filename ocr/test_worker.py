"""worker.py 下载安全测试：超时、大小、Content-Type 白名单、临时文件清理"""
import os
import socket
import sys
import unittest
from unittest.mock import MagicMock, patch

# import worker 前注入假的 rapidocr_onnxruntime，避免运行时引入真实 OCR 依赖
sys.modules.setdefault("rapidocr_onnxruntime", MagicMock())

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import worker  # noqa: E402


class _FakeResponse:
    """模拟 urlopen 返回的 response（context manager + headers + read 分块）"""

    def __init__(self, content_type, chunks):
        self._chunks = list(chunks)
        self.headers = MagicMock()
        self.headers.get_content_type.return_value = content_type

    def read(self, size):
        if not self._chunks:
            return b""
        return self._chunks.pop(0)

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        return False


class TestDownloadNormal(unittest.TestCase):
    def test_returns_path_and_writes_content(self):
        body = b"FAKE_JPEG_BYTES" * 64  # ~ 960 bytes
        fake_resp = _FakeResponse("image/jpeg", [body])
        with patch.object(worker.urllib.request, "urlopen", return_value=fake_resp):
            path = worker._download_image_safe("http://example.com/x.jpg", timeout=15)
        try:
            self.assertTrue(os.path.exists(path))
            with open(path, "rb") as f:
                self.assertEqual(f.read(), body)
        finally:
            if os.path.exists(path):
                os.unlink(path)


class TestDownloadSlow(unittest.TestCase):
    def test_timeout_raises_and_leaves_no_tempfile(self):
        before = _list_tempfiles()
        with patch.object(
            worker.urllib.request, "urlopen", side_effect=socket.timeout("timed out")
        ):
            with self.assertRaises(socket.timeout):
                worker._download_image_safe("http://example.com/x.jpg", timeout=1)
        after = _list_tempfiles()
        self.assertEqual(before, after, "socket.timeout 路径不应残留临时文件")

    def test_download_image_wrapper_returns_err_on_timeout(self):
        with patch.object(
            worker.urllib.request, "urlopen", side_effect=socket.timeout("timed out")
        ):
            path, err = worker.download_image("http://example.com/x.jpg", timeout=1)
        self.assertIsNone(path)
        self.assertIn("timed out", err)


class TestDownloadOversize(unittest.TestCase):
    def test_oversize_raises_and_cleans_tempfile(self):
        # 两块 8MB 共 16MB > 15MB 上限，第二块写入前应早退
        oversize_chunk = b"\x00" * (8 * 1024 * 1024)
        fake_resp = _FakeResponse("image/jpeg", [oversize_chunk, oversize_chunk])
        before = _list_tempfiles()
        with patch.object(worker.urllib.request, "urlopen", return_value=fake_resp):
            with self.assertRaisesRegex(ValueError, "image exceeds size limit"):
                worker._download_image_safe("http://example.com/huge.jpg", timeout=15)
        after = _list_tempfiles()
        self.assertEqual(before, after, "超大文件路径必须清理临时文件")


class TestDownloadWrongContentType(unittest.TestCase):
    def test_html_content_type_raises_before_tempfile(self):
        fake_resp = _FakeResponse("text/html", [b"<html></html>"])
        before = _list_tempfiles()
        with patch.object(worker.urllib.request, "urlopen", return_value=fake_resp):
            with self.assertRaisesRegex(ValueError, "unsupported content type"):
                worker._download_image_safe("http://example.com/x.html", timeout=15)
        after = _list_tempfiles()
        self.assertEqual(before, after, "Content-Type 拒绝路径不应建临时文件")


def _list_tempfiles():
    """快照临时目录状态以便检测残留"""
    import tempfile

    d = tempfile.gettempdir()
    return set(os.listdir(d))


if __name__ == "__main__":
    unittest.main()
