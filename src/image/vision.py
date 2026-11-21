"""Local model sessions. Importing this module does not initialize a GPU runtime."""

import gc
import hashlib
import importlib.util
import json
import os
import sys
import traceback
import warnings
from contextlib import AbstractContextManager, ExitStack
from pathlib import Path

import cv2
import numpy as np
from PIL import Image

from . import rle
from .erase import grow
from .schema import write_json

_KOHARU = Path("mayocream/koharu-layout-rfdetr-seg-2xl-1152")
_OGKALU = Path("ogkalu/comic-text-and-bubble-detector/detector.onnx")
_LAMA = Path("Sanster/big-lama/big-lama.pt")
HAYAI_MODEL = Path("JustANormalTinkerer/hayai-ocr-v2.5-nova")
BABERU_MODEL = Path("genshiai-daichi/baberu-ocr")
_KOHARU_CLASSES = ("text", "onomatopoeia", "bubble", "panel")
_OGKALU_CLASSES = ("bubble", "text_bubble", "text_free")
_KOHARU_MIN_SCORE = 0.20
_OGKALU_MIN_SCORE = 0.40
_MAX_SIDE = 1536


def _models_root(models_root):
    value = models_root or os.getenv("LOCALIZATION_KIT_MODELS_ROOT")
    if not value:
        raise ValueError(
            "Models directory is required: set LOCALIZATION_KIT_MODELS_ROOT"
        )
    return Path(value).resolve()


def _cache_key(digest, use_ogkalu, models_root=None):
    return {
        "version": 1,
        "sha256": digest,
        "koharu": [_KOHARU.as_posix(), _KOHARU_MIN_SCORE],
        "ogkalu": [_OGKALU.as_posix(), _OGKALU_MIN_SCORE] if use_ogkalu else None,
        "max_side": _MAX_SIDE,
    }


def _gpu_torch():
    try:
        import torch
    except ImportError as exc:
        raise RuntimeError("CUDA inference requires pixi install -e image-gpu") from exc
    budget, total = _gpu_budget_with_total(torch)
    torch.cuda.set_per_process_memory_fraction(min(0.5, budget / total))
    return torch


def _gpu_budget_with_total(torch):
    if not torch.cuda.is_available():
        raise RuntimeError("CUDA inference requires an available NVIDIA GPU")
    free, total = torch.cuda.mem_get_info()
    if free < 3 << 30:
        raise RuntimeError(
            f"CUDA inference needs 3 GiB free VRAM ({free >> 20} MiB free)"
        )
    return min(total // 2, free - (1 << 30)), total


def _gpu_budget(torch):
    return _gpu_budget_with_total(torch)[0]


def _inference_size(w, h):
    scale = min(1, _MAX_SIDE / max(w, h))
    return max(1, round(w * scale)), max(1, round(h * scale))


def cached_detection(
    image_path,
    cache_path,
    use_ogkalu,
    models_root,
    detect,
    *,
    redetect=False,
    digest=None,
):
    if cache_path is None:
        return detect(image_path)
    if digest is None:
        with Path(image_path).open("rb") as stream:
            digest = hashlib.file_digest(stream, "sha256").hexdigest()
    key = _cache_key(digest, use_ogkalu, models_root)
    if not redetect:
        try:
            cached = json.loads(Path(cache_path).read_bytes())
        except (FileNotFoundError, ValueError):
            cached = None
        if (
            isinstance(cached, dict)
            and cached.get("key") == key
            and all(isinstance(cached.get(k), list) for k in ("koharu", "ogkalu"))
        ):
            return {k: cached[k] for k in ("koharu", "ogkalu")}
    result = detect(image_path)
    write_json(Path(cache_path), {"key": key, **result})
    return result


class _Session(AbstractContextManager):
    def __init__(self, models_root=None):
        self._models_root_arg = models_root
        self.model = None

    @property
    def root(self):
        return _models_root(self._models_root_arg)

    def __exit__(self, exc_type, error, tb):
        try:
            self.model = None
            # Retained exceptions otherwise keep tensors in completed inference frames.
            if tb is not None:
                traceback.clear_frames(tb)
        finally:
            gc.collect()
            torch = sys.modules.get("torch")
            if torch is not None and torch.cuda.is_initialized():
                torch.cuda.empty_cache()


def _load_koharu(weights):
    from rfdetr import RFDETRSeg2XLarge
    from rfdetr.config import PretrainWeightsCompatibilityWarning
    from safetensors.torch import load_file

    with warnings.catch_warnings():
        warnings.simplefilter("ignore", PretrainWeightsCompatibilityWarning)
        model = RFDETRSeg2XLarge(
            pretrain_weights=None,
            resolution=1152,
            num_select=160,
            num_classes=len(_KOHARU_CLASSES),
            device="cuda",
        )
    model.model.model.load_state_dict(
        load_file(str(weights), device="cpu"), strict=True
    )
    model.model.class_names = list(_KOHARU_CLASSES)
    return model


def _onnx_session(path, budget, *, require_cuda=False):
    import onnxruntime as ort

    providers = ["CPUExecutionProvider"]
    if budget and "CUDAExecutionProvider" in ort.get_available_providers():
        ort.preload_dlls()
        providers.insert(
            0,
            (
                "CUDAExecutionProvider",
                {
                    "gpu_mem_limit": str(budget),
                    "arena_extend_strategy": "kSameAsRequested",
                    "cudnn_conv_algo_search": "HEURISTIC",
                },
            ),
        )
    session = ort.InferenceSession(str(path), providers=providers)
    if require_cuda and "CUDAExecutionProvider" not in session.get_providers():
        raise RuntimeError("Ogkalu requires ONNX Runtime's CUDA execution provider")
    return session


def _ogkalu_predictions(session, image, size):
    array = np.asarray(image.resize((640, 640), Image.Resampling.BILINEAR), np.float32)
    feeds = {
        "images": np.ascontiguousarray(array.transpose(2, 0, 1)[None] / 255),
        # This export's box scaling expects width before height.
        "orig_target_sizes": np.array([size], dtype=np.int64),
    }
    labels, boxes, scores = session.run(None, feeds)
    return [
        {
            "cls": _OGKALU_CLASSES[int(label)],
            "score": float(score),
            "bbox": box.tolist(),
        }
        for label, box, score in zip(labels[0], boxes[0], scores[0], strict=True)
        if score >= _OGKALU_MIN_SCORE
    ]


class DetectorSession(_Session):
    def __init__(self, models_root=None, use_ogkalu=True):
        super().__init__(models_root)
        self.use_ogkalu = use_ogkalu

    def predict(self, image):
        if self.model is None:
            weights = self.root / _KOHARU / "model.safetensors"
            onnx = self.root / _OGKALU
            for path in (weights, onnx) if self.use_ogkalu else (weights,):
                if not path.is_file():
                    raise FileNotFoundError(f"Detection model missing: {path}")
            self.torch = _gpu_torch()
            koharu = _load_koharu(weights)
            ogkalu = (
                _onnx_session(onnx, _gpu_budget(self.torch), require_cuda=True)
                if self.use_ogkalu
                else None
            )
            self.model = koharu, ogkalu
        koharu, ogkalu = self.model
        w, h = image.size
        sw, sh = _inference_size(w, h)
        small = (
            image
            if (sw, sh) == (w, h)
            else image.resize((sw, sh), Image.Resampling.BILINEAR)
        )
        with self.torch.inference_mode():
            predictions = koharu.predict(
                small,
                threshold=_KOHARU_MIN_SCORE,
                shape=(1152, 1152),
                include_source_image=False,
            )
        if predictions.mask is None and len(predictions):
            raise RuntimeError("Koharu did not return segmentation masks")
        found = []
        scale = np.array([w / sw, h / sh, w / sw, h / sh])
        for label, box, score, mask in zip(
            predictions.class_id,
            predictions.xyxy,
            predictions.confidence,
            () if predictions.mask is None else predictions.mask,
            strict=True,
        ):
            if (sw, sh) != (w, h):
                mask = cv2.resize(
                    mask.astype(np.uint8), (w, h), interpolation=cv2.INTER_NEAREST
                )
            found.append(
                {
                    "cls": _KOHARU_CLASSES[int(label)],
                    "score": float(score),
                    "bbox": (box * scale).tolist(),
                    "mask": rle.encode(mask),
                }
            )
        return {
            "koharu": found,
            "ogkalu": _ogkalu_predictions(ogkalu, small, (w, h)) if ogkalu else [],
        }

    def detect(self, image_path, cache_path=None):
        def predict_file(path):
            with Image.open(path) as opened:
                return self.predict(opened.convert("RGB"))

        return cached_detection(
            image_path,
            cache_path,
            self.use_ogkalu,
            self._models_root_arg,
            predict_file,
        )


def detect_image(image_path, *, models_root=None, use_ogkalu=True, cache_path=None):
    with DetectorSession(models_root, use_ogkalu) as session:
        return session.detect(image_path, cache_path)


def _inpaint_crop(torch, model, rgb, mask):
    h, w = mask.shape
    sw, sh = _inference_size(w, h)
    if (sw, sh) != (w, h):
        rgb = cv2.resize(rgb, (sw, sh), interpolation=cv2.INTER_AREA)
        mask = (
            cv2.resize(mask.astype(np.float32), (sw, sh), interpolation=cv2.INTER_AREA)
            > 0
        )
    ph, pw = (-sh) % 8, (-sw) % 8
    image = np.pad(rgb, ((0, ph), (0, pw), (0, 0)), mode="reflect")
    repair = np.pad(mask, ((0, ph), (0, pw)))
    t_img = (
        torch.from_numpy(image).permute(2, 0, 1)[None].to("cuda", torch.float32) / 255
    )
    t_mask = torch.from_numpy(repair)[None, None].to("cuda", torch.float32)
    filled = (
        model(t_img, t_mask)[0]
        .permute(1, 2, 0)
        .clamp(0, 1)
        .mul(255)
        .byte()
        .cpu()
        .numpy()
    )
    filled = filled[:sh, :sw]
    if (sw, sh) != (w, h):
        filled = cv2.resize(filled, (w, h), interpolation=cv2.INTER_CUBIC)
    return filled


class InpaintSession(_Session):
    def inpaint(self, rgb, mask):
        mask = np.asarray(mask, dtype=bool)
        if rgb.dtype != np.uint8 or rgb.shape != (*mask.shape, 3) or mask.ndim != 2:
            raise ValueError("LaMa requires an RGB uint8 image and a matching 2D mask")
        result = rgb.copy()
        if not mask.any():
            return result
        if self.model is None:
            weights = self.root / _LAMA
            if not weights.is_file():
                raise FileNotFoundError(f"LaMa model missing: {weights}")
            self.torch = _gpu_torch()
            self.model = self.torch.jit.load(str(weights), map_location="cuda").eval()
        _, labels, stats, _ = cv2.connectedComponentsWithStats(
            grow(mask, 16).astype(np.uint8)
        )
        h, w = mask.shape
        with self.torch.inference_mode():
            for label, (x, y, bw, bh, _) in enumerate(stats[1:], 1):
                pad = round(0.5 * max(bw, bh))
                ys = slice(max(0, y - pad), min(h, y + bh + pad))
                xs = slice(max(0, x - pad), min(w, x + bw + pad))
                core = mask[ys, xs] & (labels[ys, xs] == label)
                tile = result[ys, xs]
                tile[core] = _inpaint_crop(self.torch, self.model, tile, core)[core]
        return result


def inpaint_image(rgb, mask, *, models_root=None):
    with InpaintSession(models_root) as session:
        return session.inpaint(rgb, mask)


class HayaiSession(_Session):
    def __call__(self, image):
        if self.model is None:
            from transformers import AutoModel, AutoProcessor, PreTrainedTokenizerFast

            self.torch = _gpu_torch()
            path = self.root / HAYAI_MODEL
            processor = "google/siglip2-base-patch16-naflex"
            try:
                self.processor = AutoProcessor.from_pretrained(
                    processor, local_files_only=True
                )
            except OSError:
                self.processor = AutoProcessor.from_pretrained(
                    processor, local_files_only=False
                )
            self.tokenizer = PreTrainedTokenizerFast.from_pretrained(path)
            self.model = (
                AutoModel.from_pretrained(path, trust_remote_code=True).cuda().eval()
            )
        inputs = self.processor(
            images=[image.convert("RGB")], max_num_patches=512, return_tensors="pt"
        ).to("cuda")
        with self.torch.inference_mode():
            return self.model.generate(
                **{
                    k: inputs[k]
                    for k in ("pixel_values", "pixel_attention_mask", "spatial_shapes")
                },
                tokenizer=self.tokenizer,
                max_new_tokens=128,
                repetition_penalty=1.0,
            )[0]


class BaberuSession(_Session):
    def __call__(self, image):
        if self.model is None:
            _gpu_torch()
            path = self.root / BABERU_MODEL
            spec = importlib.util.spec_from_file_location(
                "_baberu_inference", path / "inference.py"
            )
            module = importlib.util.module_from_spec(spec)
            # The upstream entry point imports three sibling modules by their bare names.
            names = ("configuration_baberu", "modeling_baberu", "tokenization_baberu")
            previous = {
                name: sys.modules.pop(name) for name in names if name in sys.modules
            }
            sys.path.insert(0, str(path))
            try:
                spec.loader.exec_module(module)
                self.model = module.BaberuOCR(path, device="cuda")
            finally:
                sys.path.remove(str(path))
                for name in names:
                    sys.modules.pop(name, None)
                sys.modules.update(previous)
        return self.model(image)


class ZhRecSession(_Session):
    def __call__(self, image):
        if self.model is None:
            import torch
            import yaml

            path = self.root / "PaddlePaddle/PP-OCRv5_server_rec_onnx"
            cfg = yaml.safe_load((path / "inference.yml").read_text(encoding="utf-8"))
            self.chars = ["", *cfg["PostProcess"]["character_dict"], " "]
            budget = 0
            if torch.cuda.is_available() and torch.cuda.mem_get_info()[0] >= 3 << 30:
                budget = _gpu_budget(torch)
            self.model = _onnx_session(path / "inference.onnx", budget)
            self.input = self.model.get_inputs()[0].name
        w = max(48, round(image.width * 48 / image.height))
        rgb = image.convert("RGB").resize((w, 48), Image.Resampling.BILINEAR)
        bgr = np.asarray(rgb, np.float32)[:, :, ::-1] / 127.5 - 1
        probs = self.model.run(None, {self.input: bgr.transpose(2, 0, 1)[None].copy()})[
            0
        ][0]
        ids = probs.argmax(axis=1)
        return "".join(
            self.chars[i]
            for i in ids[np.r_[True, ids[1:] != ids[:-1]]]
            if 0 < i < len(self.chars)
        )


class ReadbackSession(ExitStack):
    def __init__(self, models_root=None):
        super().__init__()
        self.detector = self.enter_context(
            DetectorSession(models_root, use_ogkalu=False)
        )
        self.jp = self.enter_context(HayaiSession(models_root))
        self.zh = self.enter_context(ZhRecSession(models_root))

    def _detect_text(self, pixels):
        instances = self.detector.predict(Image.fromarray(pixels))["koharu"]
        return [
            rle.decode(i["mask"])
            for i in instances
            if i["cls"] in {"text", "onomatopoeia"}
            and i["score"] >= (0.25 if i["cls"] == "text" else 0.20)
        ]

    def __call__(self, rendered, cleaned, analysis, reports, plans):
        from .verify import readback_page

        return readback_page(
            rendered,
            cleaned,
            analysis,
            reports,
            plans,
            detect_text_fn=self._detect_text,
            read_jp_fn=self.jp,
            read_zh_fn=self.zh,
        )
