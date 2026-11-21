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
   默认 `--inpaint cpu`。`--inpaint auto` 优先使用已安装的 GPU 环境中的 LaMa，`--inpaint lama` 强制要求 GPU 环境。两种模式在检测到复杂背景或覆写指定 `erase = "lama"` 时调用 LaMa；纯 CPU 渲染遇到需要 LaMa 的单元时保留原图并记为质检失败。
5. **核对审图指纹**：
   ```sh
   pixi run localization-kit image fingerprint --work-dir work/page01 --rendered work/page01/render/rendered.png
   ```

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
