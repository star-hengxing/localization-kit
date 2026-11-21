# 安装与作品配置

在仓库根目录安装依赖后，可直接通过 `pixi run localization-kit ...` 调用 CLI（也可先执行 `pixi run build` 后将 `build/` 加入 PATH）。用 `pixi run localization-kit --help` 查看命令清单，用 `pixi run localization-kit terms init --help` 查看子命令参数。构建与测试说明见[源码构建与测试](development.md)。

## 准备原文并初始化

为作品建一个目录，将原文放入 `source/`。

| 原文格式 | `source/` 内容 |
| --- | --- |
| `artc` | `book.toml`、`toc.toml`、UTF-8 `.txt` 章节及可选图片（书籍章节目录格式）。 |
| `vtt` | 一个或多个 VTT 文件，可保留各轨的子目录。 |
| `jsonl` | 默认使用 `source/input.jsonl`。 |

```sh
localization-kit init work/novel --adapter artc
```

`--adapter` 选择 `artc`、`vtt` 或 `jsonl`。可用 `--input` 指定 `source/` 内的文件或目录，例如 `--input source/episode.vtt`。初始化和翻译直接读取章节目录格式，导出后输出目标语或双语章节目录。[输入与输出格式](workspace.md#输入与输出)

初始化生成 `config.toml`、空术语表和日语词频统计。已有术语表会保留。需要重算统计或准备角色卡时，见[术语与角色卡](polish.md#术语与角色卡)。

## 指定作品位置

显式目录紧跟完整命令名，也可以用 `--project-config` 指定作品的 `config.toml`。省略作品位置时读取当前目录的配置，`init` 则初始化当前目录。

```sh
localization-kit status pixiv-12345
localization-kit status --project-config pixiv-12345/config.toml
cd pixiv-12345
localization-kit status
```

配置文件内的路径及 `init --input` 相对作品目录，命令行传入的其他文件路径相对执行目录。

## 模型配置

在作品的 `config.toml` 中编辑模型和材料，沿用初始化生成的 `adapter` 和 `input`。模型地址、模型 ID 和初翻 profile 由部署方或 OpenAI 兼容服务端提供，按部署方要求启动模型并维护会话。

```toml
adapter = "artc"
input = "source"
model = "translate"
glossaries = ["materials/glossary.json"]
# cards = "materials/characters.json"

[models.translate]
url = "<模型 API 地址>"
model = "<模型 ID>"
profile = "hy-mt2"
```

- 工作区的 `model` 设置默认模型。只有一个模型时可以省略。配置多个 `[models.名称]` 后，可用 `--model 名称` 选择。初翻使用 `hy-mt2` 或 `opensakura` profile，质量模型可省略 `profile`。状态查询和导出无需模型配置。
- `glossaries` 可以省略，此时使用 `materials/glossary.json`。填写后按指定列表使用。默认角色卡 `materials/characters.json` 填写完成后用于润色与校对，空模板会跳过。可通过 `cards` 指定其他角色卡。材料或配置变化后用 `status` 核对阶段状态。
- 旧作品可删除已经退役的模型 `revision` 字段。部署不同权重时使用可区分的实际模型 ID。

便携包中的 `schemas/config.schema.json` 提供完整字段。使用 Tombi 时，可在 `config.toml` 首行填写 `#:schema <相对路径>/schemas/config.schema.json`，路径从配置文件所在目录算起。[作品配置 schema](../../schemas/config.schema.json)

初始化后可按 [JSONL 流程示例](cli-workflow-demo.md) 或[小说翻译指南](novel-quickstart.md) 执行。阶段文件、日志和恢复方式见[作品目录和状态](workspace.md#作品目录和状态)。
