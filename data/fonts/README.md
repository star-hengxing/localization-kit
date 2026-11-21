# 字体

嵌字用的思源黑体（Source Han Sans CN，简体中文子集 OTF）与缝合像素字体（Fusion Pixel Font，10px / 12px 等宽简体中文），均遵循 SIL Open Font License 1.1。

运行以下命令下载全部字体到本目录：

```sh
pixi run -e image download-fonts
# 或：python scripts/download_fonts.py
```

| 文件 | 用途 |
|---|---|
| `SourceHanSansCN-Medium.otf` | 常规对白起手字重（`typeset.FONTS["medium"]`） |
| `SourceHanSansCN-Bold.otf` | 粗体对白/强调字重（`typeset.FONTS["bold"]`） |
| `SourceHanSansCN-Regular.otf` | 备用细体字重（`typeset.FONTS["regular"]`） |
| `SourceHanSansCN-Heavy.otf` | 喊叫等特粗字重（`typeset.FONTS["heavy"]`） |
| `fusion-pixel-10px-monospaced-zh_hans.otf`、`fusion-pixel-12px-monospaced-zh_hans.otf` | 原文为点阵字体的标牌（`typeset.PIXEL_FONTS`） |

上游来源：
- Source Han Sans CN：`https://github.com/adobe-fonts/source-han-sans/raw/release/SubsetOTF/CN/SourceHanSansCN-<字重>.otf`
- Fusion Pixel Font v2026.09.25（`OFL-fusion-pixel-font.txt`）：`https://github.com/TakWolf/fusion-pixel-font/releases/download/2026.09.25/fusion-pixel-font-<10|12>px-monospaced-otf-v2026.09.25.zip`
