# 纯文本翻译工作流

作品目录是命令的操作对象。`source/` 保存原文，`config.toml` 记录格式和已启用材料，`state/` 保存初翻、润色、校对结果与运行状态，`build/` 保存小说成品。[命令行流程示例](cli-workflow-demo.md) [Pixiv 小说作品目录](novel-quickstart.md#原文和初始化)

## 流程

1. 放入 JSONL、VTT 或 artc 原文，执行 `localization-kit init <作品> --adapter <格式>`。
2. 可选准备、确认并启用术语表和角色卡；初翻只使用术语表。
3. `localization-kit translate <作品>` 通过配置的模型 API 翻译，逐条保存结果；失败条目加 `--retry-failed`，中断后可续跑。
4. 人工或主 agent 安排执行者按块完成 `polish` 和 `proofread`；校对包含对照原文审查、修复和核对。详见 [subagent 用法](agent.md)。
5. `localization-kit export <作品> --stage <阶段> --out <路径>` 导出指定阶段。小说导出为 `artc` 章节目录结构，供下游排版或构建工具使用。

作品命令将日志追加到根目录的 `localization-kit.log`，同时输出 stderr。机器可读结果使用 stdout。日志文件打不开时，命令在改动作品状态前失败。进程被强制终止时，日志可能缺少结束信息，恢复前需要确认模型端请求是否结束。

## 作品目录和状态

```text
作品/
├── config.toml
├── source/
├── materials/
├── state/
│   ├── manifest.json
│   ├── results.jsonl
│   ├── status.json
│   ├── translate/translation.json
│   ├── polish/translation.json
│   └── proofread/translation.json
├── build/
└── localization-kit.log
```

初始化只准备原文配置和材料，翻译开始后建立 `state/`。各阶段首次产生结果后才生成 JSON，包含全部条目，未完成条目标为 `pending`。`results.jsonl` 是追加式结果历史，用于恢复和重建阶段 JSON。`status.json` 保存阶段配置和任务领取，翻译期间还可提供带时间的进度快照。由 CLI 管理状态、结果和恢复文件。

如果状态查询报告有待恢复的批次或不完整的日志末尾，可先备份作品，再用原翻译命令恢复。已完整写入且有效的末行会保留。完整日志记录包含非法 UTF-8 时，命令会报告损坏记录的字节偏移。

已有作品清单时，缺少 `state/results.jsonl` 会中止命令。请恢复原历史文件后重试。

对照同一作品的不同阶段时，用 `manifest.json` 和各阶段 `translation.json` 中的条目 ID 对齐原文和译文。跨作品比较见[作业比较](ab-eval.md#作业比较)。

默认使用 `materials/glossary.json`，填写后的 `materials/characters.json` 自动用于润色和校对；空角色卡模板会跳过。`config.toml` 可以指定其他材料，路径相对作品根目录。作品格式和输入路径由配置登记，原文须位于 `source/`。状态记录的作品内材料路径相对作品根目录，移动整个作品后仍可解析。

## 输入与输出

### JSONL

默认输入为 `source/input.jsonl`，每行一个 JSON 对象，`id`、`src` 为字符串；可选 `group` 和对象 `meta`。未指定 group 时每条为独立组。

```jsonl
{"id":"1","group":"scene-1","src":"こんにちは"}
{"id":"2","group":"scene-1","src":"ありがとう"}
```

导出为 JSONL 文件，包含 ID、原文、译文、状态及各条结果的来源。`state/credits.json` 中的独立译制署名供 VTT 和 artc 导出使用。当前 JSONL 导出省略输入的 `meta`，下游如需元数据须另行保管。

### VTT

`source/` 可以包含一条或多条字幕轨道。程序递归读取 VTT，以相对路径区分轨道，保留时间轴；输出每条先原文、再追加不同于原文的译文。原 `NOTE` 块不保留，输出包含译制署名。导出后需核对轨道、时间轴和实际播放效果。

### artc 小说

`source/` 包含原始 artc 项目的 `book.toml`、`toc.toml` 和目录表引用的 UTF-8 `.txt` 章节。章节路径使用项目内的相对路径。`lang` 以 `zh` 开头或已有 `original` 配置时会报错。

书名、章节标题及正文行转换成条目，正文按章节分组，ID 形如 `0001:p042:l2`。`chapters/info.txt` 作为信息章保留，译制署名写入信息章或新增的署名章节。导出保留段落的缩进和空行，送译前去除 ruby 注音的读音。

状态查询和初翻读取作品文本。默认导出中文目录结构，`--bilingual` 将原文章节另存到 `original/`，同时复制封面等资源，可交给下游电子书构建工具处理。[小说导出说明](novel-quickstart.md#导出)

## 增量和检查

输入不变时可续跑。原文或分组变化须建立新的作品工作区，`translate <新作品> --previous <原作品>` 复用符合条件的结果；历史条目一并保存，目标作品的润色和校对配置需重新核验。保留原作品资源，以便需要时重建旧版本。

复用及阶段有效性检查考虑原文、分组、关联术语、模型 URL、ID、profile、规则、字形表和上文。条件变化时重新判断机器结果，保留人工、润色及校对结果并按需标记复核。切换模型 ID 或 profile 时使用新作品目录。部署侧可用不同模型 ID 区分不同权重。

选阶段导出之前核对条目完整性和依赖，不以其他阶段静默填充缺项。原样确认是有效的阶段结果；无需翻译的条目可原样通过。自动检查无法证明语义正确，仍须对照原文验收。

如果命令报告输出已生成但作品配置或原文已变化，先核对该路径下的成品。需重新导出时使用新路径。

## 参考资料

- [小说翻译](novel-quickstart.md)
- [术语、角色卡、审查与润色](polish.md)
- [subagent 润色与校对](agent.md)
- [翻译质量比较与评测](ab-eval.md)
