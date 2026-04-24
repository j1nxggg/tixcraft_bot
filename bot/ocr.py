import io
from pathlib import Path
from urllib.request import urlopen

import numpy as np
import onnxruntime as ort
from PIL import Image

from logger import log


MODEL_DIR = Path(__file__).resolve().parent / "model"
MODEL_PATH = MODEL_DIR / "model_v2.onnx"
MODEL_DATA_PATH = MODEL_DIR / "model_v2.onnx.data"
MODEL_URL = "https://github.com/j1nxggg/tixcraft_model/releases/download/Model-File-V2.0.0/model_v2.onnx"
MODEL_DATA_URL = "https://github.com/j1nxggg/tixcraft_model/releases/download/Model-File-V2.0.0/model_v2.onnx.data"

# 模型規格 輸入 (1,1,32,128) float32 灰階，mean 0.5 / std 0.5；輸出 (1,4,26) logits
_CAPTCHA_CHARS = "abcdefghijklmnopqrstuvwxyz"
_CAPTCHA_INPUT_NAME = "input"
_CAPTCHA_TARGET_WH = (128, 32)  # PIL.resize 吃 (width, height)


def ensure_ocr_model_ready() -> ort.InferenceSession:
    try:
        session = load_ocr_model()
        log("OCR辨識模型載入成功")
        return session
    except FileNotFoundError as missing:
        # 模型檔真的缺失時才下載
        log(f"模型檔缺失,正在下載：{missing}")
        download_ocr_model_files()

    session = load_ocr_model()
    log("OCR辨識模型載入成功")
    return session


def load_ocr_model() -> ort.InferenceSession:
    if not MODEL_PATH.exists():
        raise FileNotFoundError(f"找不到模型檔：{MODEL_PATH}")
    if not MODEL_DATA_PATH.exists():
        raise FileNotFoundError(f"找不到模型資料檔：{MODEL_DATA_PATH}")

    return ort.InferenceSession(str(MODEL_PATH), providers=["CPUExecutionProvider"])


def download_ocr_model_files() -> None:
    MODEL_DIR.mkdir(parents=True, exist_ok=True)
    download_file(MODEL_URL, MODEL_PATH)
    download_file(MODEL_DATA_URL, MODEL_DATA_PATH)


def download_file(url: str, destination: Path) -> None:
    with urlopen(url) as response:
        data = response.read()
    destination.write_bytes(data)


def recognize_captcha_bytes(session: ort.InferenceSession, image_bytes: bytes) -> str:
    with Image.open(io.BytesIO(image_bytes)) as img:
        gray = img.convert("L").resize(_CAPTCHA_TARGET_WH, Image.Resampling.BILINEAR)

    arr = np.asarray(gray, dtype=np.float32) / 255.0
    arr = (arr - 0.5) / 0.5
    arr = arr[np.newaxis, np.newaxis, :, :]

    outputs = session.run(None, {_CAPTCHA_INPUT_NAME: arr})
    logits = outputs[0]
    if not isinstance(logits, np.ndarray):
        raise TypeError(f"Unexpected OCR output type: {type(logits)!r}")
    indices = logits[0].argmax(axis=-1)
    return "".join(_CAPTCHA_CHARS[int(i)] for i in indices)


def recognize_captcha(session: ort.InferenceSession, image_path: Path) -> str:
    # 保留讀檔版本以防其他地方要用
    return recognize_captcha_bytes(session, Path(image_path).read_bytes())
