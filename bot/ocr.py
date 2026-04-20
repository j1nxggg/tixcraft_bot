from pathlib import Path
from urllib.request import urlopen

import onnxruntime as ort


MODEL_DIR = Path(__file__).resolve().parent / "model"
MODEL_PATH = MODEL_DIR / "model_v2.onnx"
MODEL_DATA_PATH = MODEL_DIR / "model_v2.onnx.data"
MODEL_URL = "https://github.com/j1nxggg/tixcraft_model/releases/download/Model-File-V2.0.0/model_v2.onnx"
MODEL_DATA_URL = "https://github.com/j1nxggg/tixcraft_model/releases/download/Model-File-V2.0.0/model_v2.onnx.data"


def ensure_ocr_model_ready() -> ort.InferenceSession:
    try:
        session = load_ocr_model()
        print("OCR辨識模型載入成功")
        return session
    except Exception as first_error:
        print(f"OCR辨識模型載入失敗，正在重新下載：{first_error}")
        download_ocr_model_files()

    session = load_ocr_model()
    print("OCR辨識模型載入成功")
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
