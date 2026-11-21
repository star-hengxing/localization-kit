# localization-kit

通用文本与图像/漫画本地化工具链，结合 **Go 工作区与文本翻译引擎**、**Python 漫画视觉流水线** 以及 **React 多场景审图工作台**，在统一的作品目录中管理原文、术语、角色卡和三阶段译文（初翻 → 润色 → 校对）。内建字符检查与默认参考词表以日→中为例，并可通过 `config.toml` 与自定义词表适配多语种本地化流程。

## 核心能力

| 场景 | 流程与交付物 | 文档 |
| --- | --- | --- |
| 分段文本（JSONL） | 读入带稳定条目 ID 的 JSONL，完成初翻、人工/API 润色与校对并导出译文 | [命令行流程示例](docs/usage/cli-workflow-demo.md) |
| 字幕（VTT） | 读入单条或多条 WebVTT，导出保留时间轴与相对目录结构的字幕 | [工作区与输入格式](docs/usage/workspace.md) |
| 小说 / 长篇章节（`artc` 格式） | 读入标准章节目录项目，导出目标语或双语章节目录供下游排版构建 | [小说翻译指南](docs/usage/novel-quickstart.md) |
| 漫画 / 图像 | 裁图/包导入 → 布局检测 → 双 OCR → 导出 JSONL 复用文本三阶段 → LaMa/CPU 擦除与嵌字排版 → 质检与回读 | [漫画本地化流水线](docs/usage/manga.md) |
| 审图工作台（Web） | 多场景切换、四视图对比、QA 告警定位、字段级反馈保存与 `overrides.toml` 导出 | [审图工作台](docs/usage/review-workbench.md) |

## 环境要求（面向开发者）

- **推荐方式**：安装 [Pixi](https://pixi.sh/)，直接管理 Go、Python、CUDA 推理与前端依赖。
- **核心工具链**：Go `>= 1.26`、Python `>= 3.11`、Node.js + `pnpm`（前端审图构建）、7-Zip（可选，仅解压 `.rar`/`.cbr` 漫画包需要）、NVIDIA CUDA GPU（可选，仅漫画检测、OCR 与 LaMa 补画需要；CPU 环境可完成文本翻译、已有分析结果的漫画渲染与审图预览）。

## 5 分钟快速上手

### 1. Clone 并安装基础环境

```sh
git clone https://github.com/star-hengxing/localization-kit.git
cd localization-kit
pixi install
```

### 2. 零模型体验：启动审图工作台样例

仓库自带双页漫画渲染样例（位于 `src/web/public/`），无需 GPU 或任何模型权重即可启动审图工作台：

```sh
pixi install -e image -e web --locked
pixi run -e web web-install
pixi run -e web web-build
pixi run localization-kit image review-serve --dir src/web/public
```

浏览器打开终端输出的地址（默认 `http://127.0.0.1:4173`）即可体验对比视图、问题定位与反馈保存。

### 3. 跑通文本翻译流程

```sh
# 1. 先准备原文 JSONL（每行包含 id 与 src）
mkdir -p work/demo/source
# 将原文写入 work/demo/source/input.jsonl

# 2. 初始化作品并配置 work/demo/config.toml 中的 OpenAI 兼容模型
pixi run localization-kit init work/demo --adapter jsonl
pixi run localization-kit translate work/demo
pixi run localization-kit status work/demo --details
pixi run localization-kit export work/demo --stage translate --out work/demo/out.jsonl
```

详细配置与多阶段人工/Agent 审校见 [环境安装与作品配置](docs/usage/setup.md) 和 [命令行流程示例](docs/usage/cli-workflow-demo.md)。

## 项目结构

```text
localization-kit/
├── src/
│   ├── cmd/             # Go CLI 入口与工具命令
│   ├── internal/        # 文本翻译、工作区状态、术语/角色卡、质检与评测引擎
│   ├── image/           # Python 漫画流水线、QA 回读与审图服务后端
│   └── web/             # React + TypeScript 多场景审图工作台前端
├── data/                # 简繁字形表、内置开源字体及参考词表
├── schemas/             # 作品 config.toml JSON Schema
├── packaging/licenses/  # 发行打包附带的第三方开源组件声明
└── docs/
    ├── usage/           # 面向开发者的使用与开发指南
    └── adrs/            # 架构决策记录（ADR）
```

## 文档导航

- [使用文档总索引](docs/usage/README.md)
- [环境安装与作品配置](docs/usage/setup.md)
- [漫画本地化流水线（模型准备、单图与多页）](docs/usage/manga.md)
- [审图工作台（Web UI 与场景 API）](docs/usage/review-workbench.md)
- [源码构建、测试与提交检查](docs/usage/development.md)
- [架构决策记录（ADR）](docs/adrs/README.md)

## 许可证与致谢

本项目基于 [MIT License](LICENSE) 开源。

第三方组件与资源声明：

- 日语分词：[Kagome](https://github.com/ikawaha/kagome) 及 IPADIC 词典（见 `packaging/licenses/kagome/`）
- 漫画嵌字字体（按需下载，`pixi run -e image download-fonts`）：思源黑体（Source Han Sans CN）与 Fusion Pixel 像素字体（SIL OFL 1.1，见 `data/fonts/README.md`）
- 漫画视觉模型（按需外部下载）：Koharu RF-DETR、Ogkalu Comic Detector、LaMa、Hayai-OCR、Baberu-OCR、PP-OCRv5（具体来源与使用条款见 [漫画本地化流水线](docs/usage/manga.md)）
