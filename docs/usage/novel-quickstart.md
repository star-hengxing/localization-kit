# 小说翻译

小说经过初翻、润色和校对后，可从所选阶段构建成品。单篇或系列使用一个作品目录。

## 原文和初始化

准备包含 `book.toml`、`toc.toml`、UTF-8 `.txt` 章节及可选图片的章节目录（`artc` 适配器格式），并将其放在作品目录的 `source/`。

```text
work/novel/
├── source/
│   ├── book.toml          书籍元数据（title、lang 等）
│   ├── toc.toml           目录结构与章节相对路径
│   ├── chapters/          UTF-8 .txt 章节正文
│   └── images/            封面和插图（如有）
├── tmp/<worker>/          领取与提交的临时文件
└── build/
    ├── zh/                目标语章节目录
    └── bilingual/         双语对照章节目录
```

```sh
localization-kit init work/novel --adapter artc
```

在作品 `config.toml` 设置模型名与材料路径。[模型配置](setup.md#模型配置) [术语与角色卡](polish.md#术语与角色卡)

## 初翻与处理

```sh
localization-kit translate work/novel --model hy-mt2
localization-kit status work/novel --details
```

初翻调用已配置的模型 API，完成结果逐条保存。重跑沿用现有状态；失败条目使用 `--retry-failed`。请求中断后先确认模型端计算已结束，再续跑。

主 agent 或人工安排执行者按块润色和校对。每位执行者使用独立的 `tmp/<worker>/`，验收后清理本次临时文件。[subagent 用法](agent.md) [阶段文件](workspace.md#作品目录和状态)

## 导出

在作品目录执行。示例选用校对稿，也可换成 `translate` 或 `polish`。两种版本按需执行：

```sh
localization-kit export --stage proofread --out build/zh/project
localization-kit export --stage proofread --out build/bilingual/project --bilingual
```

导出核对阶段完整性、依赖、字形、额外英文和双语原文，并输出完整章节目录与复制图片资源；如需进一步打包为 EPUB/PDF，可交给外部排版或电子书构建工具处理。输出路径须为新路径，执行期间暂停修改原文、状态和材料。

原文变化后建立新作品目录，通过 `translate <新作品> --previous <原作品>` 复用可用历史。
