# 术语、角色卡、审查与润色

这些命令提供术语、角色卡、审查和润色操作。`review` 生成或校验审查报告；`polish` 生成润色候选，并在明确执行 `polish apply` 后写入作业。目标流程中的校对包含审查与修复，subagent 可以按块领取并处理校对任务。

`run` 命令通过配置的 LLM API 执行单项工作；外部 subagent 可读取材料并填写结果文件，也可通过 [任务领取与提交](agent.md) 处理润色和校对。主 agent 负责派发，见[阶段与执行方式](../adrs/0007-translation-review-and-polish.md)。

## 术语与角色卡

作品 `init` 自动建立空术语表和日语词频统计。程序使用 Kagome 和 IPADIC 分词，统计出现至少两次的名词和连续名词组合；排除代名词、非自立名词、数字和接尾词。结果按次数排序，不跨原文条目，也不自动写入术语表。分词可能切错人名或将普通名词列为候选，human 或 agent 应结合原文选择，将需要统一的词写入 `materials/glossary.json`：

```json
{"entries":[{"src":"アリス","dst":"爱丽丝","kind":"name","constraint":"hard"}]}
```

独立重新初始化或调整统计：

```sh
localization-kit terms init --project-config work/config.toml
localization-kit terms init --project-config work/config.toml --refresh
localization-kit terms validate work
```

`--refresh` 只更新统计，不覆盖术语表；旧版子串统计需使用该选项重新生成。默认表自动用于三个阶段；如果配置了 `glossaries`，则只使用配置指定的表。也可选用 `terms run work --model quality` 调用质量模型生成 `materials/terms-candidates.json`；候选是词条列表，由 human 或 agent 选择后写入作品表。冲突候选会保留，校验术语表时会要求先消除同一写法的冲突。模型运行记录用于续跑，输入或模型配置发生变化时应使用新的运行目录。

角色卡由 agent 填写，可不使用：

```sh
localization-kit characters init --project-config work/config.toml
localization-kit characters validate work
```

模板在 `materials/characters.json`。填写人物原名、译名、性别、代词和说话口吻。自称、称呼、笑声和口头禅等事实可在对应数组中记录 `src`、`dst` 和原文短引句 `evidence`。填写后默认提供给润色及校对 agent，空模板会略过。校验核对事实引句，语义仍由执行者对照原文判断。人物原名与已启用术语表的原词或变体完全相同时，译名须一致。材料文件保存术语和角色事实，原文批次另行传递。

也可通过 API 读取全文和术语表，生成同一格式的角色卡：

```sh
localization-kit characters run work --model quality --out work/materials/characters-api.json
localization-kit characters validate work --input work/materials/characters-api.json
```

`--out` 必须是新文件，生成结果须通过与 `characters validate` 相同的校验。写入前会复查原文、术语和模型配置。确认内容后，在 `config.toml` 的 `cards` 中指定该文件。无角色时可输出空数组。此命令在一次请求中处理全文，超出模型上下文时需要更大上下文的模型。每条引文最多 160 个 Unicode 字符的限制适用于新卡校验。

```json
{"characters":[{"name_src":"アリス","name_dst":"爱丽丝","aliases_dst":[],"gender":"unknown","pronoun":"unknown","register":"语气温和","self_ref":[],"address":[],"laugh":[],"catchphrases":[]}]}
```

称呼事实另填 `to`；当对象是听者且没有明确称呼原词时可填 `"to":"listener","src":""`。引句须逐字存在于原文，且包含非空的 `src`。

有初翻作业后，可用角色卡执行保守的人称提示：

```sh
localization-kit characters warnings work --narration narration.json
```

`narration.json` 是组名到 `first`、`third` 或 `unknown` 的对象，例如 `{"chapter-1":"third"}`；该参数可省略。提示输出到 stdout，不修改译文；无警告不代表人称一定正确。

## 审查初翻

`fidelity` 检查漏译、增译和意义，`subject` 检查主语、施受及人物关系。目标必须已有译文，原样保留条目和待复核条目不作为默认目标；邻近上下文仍会附在材料中。可用重复的 `--id` 或 `--group` 选择范围，并用 `--batch-size` 调整目标批次大小。

```sh
localization-kit review prepare work --kind fidelity --out work/materials/pre.json
```

审查使用作品配置中启用的术语表和角色卡。外部检查后另存报告；每个目标必须有一条 `pass`、`issues` 或 `uncertain`，有问题时提供逐字原文依据。以下哈希使用 `pre.json` 的 `hash`，ID 使用材料中的实际目标 ID：

```json
{"material_hash":"材料中的 hash","items":[{"id":"条目 ID","verdict":"pass"}]}
```

```sh
localization-kit review validate work --input work/materials/pre.json --report work/materials/pre-report.json
```

校验检查报告覆盖、依据和底稿版本，输出 `valid`、各判定数量及 `all_pass`；`valid` 只表示结构有效。报告不直接改写译文。

也可使用配置中的质量模型逐批执行检查：

```sh
localization-kit review run work --input work/materials/pre.json --model quality --run-dir work/materials/pre-review-run --out work/materials/pre-report.json
```

`quality` 是示例模型名，需先在模型配置中填写能执行结构化审查的模型；初翻效果不能证明模型适合审查。程序对每批只发一次请求，保存请求开始记录、原始回复、错误和通过校验的批次。相同材料及模型续跑会复用通过的批次；有失败或未确认的请求时，先确认上游推理已结束，再加 `--retry-failed`。材料或模型配置变化需要新的运行目录。输出截断时，可提高输出 token 上限，或缩小 `review prepare --batch-size` 后重新准备材料；两种情况均需新的运行目录。运行目录内含原文与译文，应自行妥善保存。

术语、检查和润色的 `run` 可用 `--max-output-tokens` 设置每批输出上限，默认 2048。调整上限后使用新的运行目录；模型配置中的 `profile` 只供初翻使用，质量模型可以省略。

运行期间会重新核对材料文件、作业、上下文及证据；语义正确性仍需人工或外部执行者核对。报告写入失败时可指定新输出路径并复用已完成批次。若首次写入清单前进程中断，目录内可能留下 `.review-run-*.json` 暂存文件；确认没有进程使用该运行目录后检查并清理。

## 润色、候选审查与应用

从初翻审查材料准备润色，`--report` 可传入已经校验的初翻审查意见。`fix` 侧重纠错，`rewrite` 可较大幅度调整表达；候选可由外部执行者填写或由指定模型生成。`fix` 是润色模式，完整校对还需审查并修复问题。

```sh
localization-kit polish prepare work --input work/materials/pre.json --report work/materials/pre-report.json --mode fix --out work/materials/polish.json
```

如另有同一批目标的主语等检查报告，可附加 `--additional-input subject.json --additional-report subject-report.json`；这两个选项可成对重复。程序校验底稿、术语表和角色卡一致后，将报告一并交给润色执行者判断。

按 `polish.json` 的 `instruction` 生成候选文件 `candidate.json`。`material_hash` 是润色材料的 `hash`，每项的 `expected_result_hash` 来自材料目标；`actor` 填实际润色者或模型。改动需要填写 `reason`。

也可调用指定质量模型逐批生成候选：

```sh
localization-kit polish run work --input work/materials/polish.json --model quality --run-dir work/materials/polish-run --out work/materials/candidate.json
```

模型只生成候选译文及理由，程序根据材料填入版本和模型署名，并检查条目、术语与基础规则；原始回复、错误和完成的批次保存在运行目录。该命令不会修改作业，仍须执行下面的候选忠实度审查和 `polish apply`。续跑、未知请求和截断的处理与 `review run` 相同。

```json
{"material_hash":"润色材料的 hash","actor":"执行者","items":[{"id":"条目 ID","expected_result_hash":"底稿哈希","dst":"候选译文","reason":"改动原因"}]}
```

```sh
localization-kit polish validate work --input work/materials/polish.json --file work/materials/candidate.json
localization-kit review prepare work --kind fidelity --candidate work/materials/candidate.json --out work/materials/post.json
```

候选审查须使用与润色材料相同的术语表和角色卡。对照 `post.json` 生成报告，除 `material_hash` 外，还须原样填写材料的 `candidate_hash`。所有目标的忠实度审查均为 `pass` 后才可应用：

```sh
localization-kit review validate work --input work/materials/post.json --report work/materials/post-report.json
localization-kit polish apply work --input work/materials/polish.json --file work/materials/candidate.json --review-input work/materials/post.json --review-report work/materials/post-report.json
```

应用会在持锁时再次检查底稿、上下文、术语和候选，使用现有修订机制追加记录；已修改记录标记为 `polish` 并保存执行者和原因，未修改项保留在候选和报告中。人工或润色历史不会被覆盖，导出署名包含仍在当前结果历史中的润色者。

候选流程的 `polish apply` 只应用经过审查的修改，不记录阶段完成；使用 `localization-kit run --stage polish|proofread --model <名称>` 才会通过共享任务流程记录完成状态。

同批若同时修改前文和依赖旧前文的后文，程序会拒绝应用。使用 `review prepare --id 条目 ID` 缩小目标，先处理前文，再重新准备和复核后文。材料和报告均需保留；任何底稿或关联上下文改变后，应重新准备对应材料。

真实作品的增强流程尚未完成生产质量验收。专项检查和人称提示不能替代人工判断。
