# 命令行流程示例

以下用一条 JSONL 演示从初始化到导出。`work` 是作品目录。安装程序和准备模型服务见[安装与作品配置](setup.md)。

先将原文保存到 `work/source/input.jsonl`：

```jsonl
{"id":"1","group":"scene","src":"アリスは「ありがとう」と言った。"}
```

初始化后可根据 `work/materials/term-statistics.json` 和原文挑词，填入 `work/materials/glossary.json`。

```sh
localization-kit init work --adapter jsonl
localization-kit terms validate work
```

需要角色卡时，生成模板，填写后校验。

```sh
localization-kit characters init --project-config work/config.toml
localization-kit characters validate work
```

## 初翻

填写 `work/config.toml`，确认模型可调用后运行初翻。[模型配置](setup.md#模型配置)

```sh
localization-kit translate work
localization-kit status work --details
```

中断后用原命令续跑，失败条目加 `--retry-failed`。模型配置或术语变化后，程序重新判断受影响的结果，保留人工修订并按需标记复核。原文变化时建立新作品，使用 `translate <新作品> --previous <原作品>` 复用可用结果。[增量和检查](workspace.md#增量和检查)

## 润色和校对

可以由人工或主 agent 指派执行者分别领取润色和校对。执行者使用实际模型 ID，逐条确认整批后保存提交文件。润色完成后，另开会话领取 `proofread`。[派发及提交示例](agent.md#人工派发)

```sh
localization-kit next work --stage polish --worker editor --model <实际模型ID> --format text
localization-kit submit work --worker editor --task <任务ID> --file <结果.json>
```

也可以通过模型 API 执行。命令示例中的 `quality` 是 `config.toml` 内登记的模型名。

```sh
localization-kit run work --stage polish --model quality
localization-kit run work --stage proofread --model quality
```

API 与人工派发共用领取与提交。请求结果未知时，先确认模型已停止，再释放任务或重试。候选生成使用 `polish run`。[API 任务说明](agent.md#api-执行)

## 导出

选择已完成的阶段，指定一个新的输出路径。示例导出校对稿，也可指定 `translate` 或 `polish`。指定阶段未完成或依赖待复核时，程序拒绝导出。

```sh
localization-kit export work --stage proofread --out output.jsonl
```

JSONL 输出到文件。VTT 和 `artc` 输出到目录。小说可加 `--bilingual` 导出双语章节目录，供外层电子书构建工具使用。[小说导出说明](novel-quickstart.md#导出)

需要重开条目或汇总润色改动时，见[指定条目重做](agent.md#指定条目重做)和[润色审计](agent.md#润色审计)。
