# 漫画本地化流水线

`localization-kit image` 提供漫画图像的页面导入、布局检测、双 OCR、文本导出、背景擦除（CPU / LaMa）、中文嵌字排版及质检回读能力。漫画译文经 `export-text` 转为标准 JSONL 后，直接复用与小说/游戏相同的初翻、润色、校对工作流。

## 环境与字体、模型准备

- **下载嵌字字体**（漫画渲染与 `test-py` 必需）：
  ```sh
  pixi run -e image download-fonts
  ```
- **纯 CPU 环境**（仅渲染已有 `analysis.json` 的作品、导文本、启动审图）：
  ```sh
  pixi install -e image --locked
  ```
- **CUDA GPU 环境（支持 Windows / Linux x64）**（启用检测、双 OCR、LaMa 补画及 `--readback` 成品回读，需可用 NVIDIA GPU 且至少 3 GiB 空闲显存）：
  ```sh
  pixi install -e image-gpu --locked
  ```
- 可通过 `LOCALIZATION_KIT_IMAGE_PYTHON` 指定自定义兼容 Python 解释器。

### 模型目录布局

通过环境变量 `LOCALIZATION_KIT_MODELS_ROOT` 指定本地模型根目录。目录需包含以下按 HuggingFace 仓库相对路径存放的模型：

| 用途 | 相对于模型根目录的路径 | 说明与条款 |
| --- | --- | --- |
| 布局与气泡检测（主） | `mayocream/koharu-layout-rfdetr-seg-2xl-1152/model.safetensors` | 必需（检测与 `--readback`）。请遵守上游模型卡与 Manga109 使用条款 |
| 文本与气泡候选检测（辅） | `ogkalu/comic-text-and-bubble-detector/detector.onnx` | 默认开启，可传 `--no-ogkalu` 跳过 |
| 日文 OCR（主） | `JustANormalTinkerer/hayai-ocr-v2.5-nova/` | 首次加载需本地或联网缓存 `google/siglip2-base-patch16-naflex` |
| 日文 OCR（备选参考） | `genshiai-daichi/baberu-ocr/` | 需保留模型目录中的 `inference.py` 及配套文件 |
| 复杂背景补画 | `Sanster/big-lama/big-lama.pt` | `--inpaint lama` 或 `--inpaint auto` 在复杂背景时按需加载 |
| 中文成品回读 | `PaddlePaddle/PP-OCRv5_server_rec_onnx/`（含 `inference.yml` 与 `inference.onnx`） | 仅 `render --readback` 需要 |

## 单图准备与渲染

建议将实验输出目录放在已被 `.gitignore` 忽略的 `work/` 下，避免误暂存图片：

1. **页面分析**：
   ```sh
   pixi run localization-kit image prepare --image original.png --out-dir work/page01
   ```
   复制原图，写入 `analysis.json`、`detect-cache.json`、`preview.png` 和 `overrides.toml`。`--page-id` 指定页号（默认 `c01/p001`），`--no-ogkalu` 仅运行 Koharu 检测。
2. **填入译文或人工覆写**：
   对照 `preview.png` 的编号，在 `overrides.toml` 的对应单元填入 `dst` 或 `action = "skip"`。也可通过工作目录 `translations.jsonl` 或 `--translations` 传入 JSONL 译文（`overrides.toml` 优先级更高）。
3. **渲染与质检**：
   ```sh
   pixi run localization-kit image render --work-dir work/page01
   ```
   将原图、成品、`qa.json` 和 `review-manifest.json` 写入 `work/page01/render/`。重渲染时可用 `--out-dir` 指定新目录，`--baseline old.png` 可加入对照图。缺译、溢出等问题写入 `qa.json`，质检失败时命令返回非零。
4. **擦除模式说明**：
   默认使用 `--inpaint cpu`。`erase = "auto"` 在平坦背景上填色，在渐变背景上使用 Telea，遇到复杂纹理时请求 LaMa。`erase = "telea"` 始终使用 CPU Telea，即使已启用 LaMa。`--inpaint auto` 可在已安装的 GPU 环境中提供 LaMa，`--inpaint lama` 要求 GPU 环境。CPU 渲染遇到需要 LaMa 的单元时保留原图并记为质检失败。
5. **核对审图指纹**：
   ```sh
   pixi run localization-kit image fingerprint --work-dir work/page01 --rendered work/page01/render/rendered.png
   ```

### 用干净底图修复跨框文字

厚描边、发光字或跨分格线的原文可先在 Photoshop 中修复，也可用修图脚本、智能体或外部模型生成底图。从原图按页面像素坐标裁出矩形，右边和下边坐标不包含在内。保留底图原尺寸与 RGB / RGBA 模式，RGBA 同时保留透明度。只改需要修复的像素，把尚未嵌入译文的 PNG 存为 `work/page01/retouch/b05.png`。

在该页的 `overrides.toml` 中填写以下内容。示例裁片为 110 × 235 像素，`source_sha256` 换成该页 `analysis.json` 的 `image` 所指原文件的 SHA-256。

```toml
[units.b05]
dst = "等等"

[units.b05.clean_patch]
file = "retouch/b05.png"
bbox = [100, 960, 210, 1195]
source_sha256 = "4ef4bd8eba4e032593d9ad98aba79f702e6bbda5cc0ddaa643b3d79acdcc5494"
```

```sh
pixi run -e image python -c "from pathlib import Path; import hashlib; print(hashlib.sha256(Path('work/page01/original.png').read_bytes()).hexdigest())"
pixi run localization-kit image render --work-dir work/page01 --inpaint cpu --out-dir work/page01/patched
```

裁片须覆盖该单元的部分或全部原文。改动可以重建本单元的边线，其他单元的原文、轮廓、已擦除或嵌字的像素以及跳过区域的保护边距均受保护。同一单元的 `clean_patch` 与 `erase` 互斥。`qa.json` 记录裁片文件、坐标、摘要、变化像素数及其中的边线像素数（`edge_pixels`）。修改译文、字号或字重后可复用同一裁片。替换裁片文件会改变页面指纹。

### 审批前的视觉核对

- 核对 `qa.json` 中的 `pass`、单元覆盖、保护区和额外改动、排版溢出、擦除边界指标。启用 `--readback` 时，日文残留与中文回读也计入 `pass`。
- 对每个改动过或被反馈标记的单元，打开并放大 `crops/*.png`，对照左侧原图与右侧成品，检查原字残影、描边和阴影、分格线连续性、纹理接缝与译文位置。
- `qa.pass == true` 表示本次运行的自动检查通过。确认局部修图质量后，再在工作台标记「通过」。

## 多页漫画流水线

`ingest` 支持单图、平面图片目录、按话分目录的漫画以及 `.zip`/`.cbz`/`.rar`/`.cbr` 图包（`.rar`/`.cbr` 需要系统已安装 7-Zip 且 `7z` 在 `PATH` 中）。每页在 `work/book/pages/<章节>/<页号>/` 下保存独立工作目录。

```sh
pixi run localization-kit image ingest --source scans --project work/book
pixi run localization-kit image prepare --project work/book
pixi run localization-kit image ocr --project work/book
pixi run localization-kit image export-text --project work/book --out work/job/source/input.jsonl
pixi run localization-kit init work/job --adapter jsonl
# 编辑 work/job/config.toml 后执行翻译：
pixi run localization-kit translate work/job
```

- **缓存与重跑**：`prepare --redetect` 刷新检测缓存；`ocr --force` 重新识别选页并更新 `work/book/cache/ocr.json`。重检时尽量继承现有单元 ID，并为新区域单调递增分配新 ID；若已编辑的覆写无法唯一匹配新区域，会报错并保留该页原分析。
- **译文导出注意**：导出的 JSONL 默认按章节分组（`--group-by page` 改为逐页分组），条目 ID 形如 `c01/p002#b01`。译文可用 `【】` 标记需要加粗的文本。**若使用 `--pages` 局部重导，建议指定独立的 `--out` 文件名，以免覆盖整书导出文件**（0 条目导出不会覆盖已有有效文件）。
- **整书渲染与回读质检**：
  ```sh
  pixi run localization-kit export work/job --stage translate --out work/translations.jsonl
  pixi run localization-kit image render --project work/book --translations work/translations.jsonl --out-dir work/review --inpaint auto --readback
  pixi run localization-kit image review-serve --dir work/review
  ```
  `prepare`、`ocr`、`export-text`、`render` 均支持 `--pages c01/p002,c01/p005` 选页。`--readback` 会在成品上检测残留日文并以 PP-OCRv5 回读中文。逐页 QA 位于 `work/review/pages/<章节>/<页号>/qa.json`，全书汇总与审图清单分别为 `work/review/qa-summary.json` 和 `work/review/review-manifest.json`。
