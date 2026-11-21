# subagent 润色与校对

主 agent 负责派发任务和阶段顺序；执行者通过命令领取、处理并提交。作业先完成初翻；术语表变化时先用当前术语表续跑翻译，更新作业状态。相同阶段的执行者应使用相同模型和材料。

## 人工派发

在支持本地命令和文件的 agent 工具中选择模型并启动执行者，或者交给主 agent 派发。派发时提供 [worker 指令](subagent-worker.md) 和下面的信息。

```text
程序：<localization-kit 可执行文件绝对路径>
作品：<作品目录绝对路径>
阶段：polish
worker：editor-1
实际模型 ID：<在 agent 工具中选择的模型>
提交文件：<本执行者独立的文件路径>
上下文范围：group
块大小：20
前文：5 条
```

润色完成后启动独立校对会话，将阶段改为 `proofread`，worker 改为 `proofreader-1`，填写该会话使用的实际模型。

## 查看进度

```sh
localization-kit status work
localization-kit status work --details
localization-kit status work --id line-1
```

默认输出初翻、润色、校对的分阶段计数；`--details` 附全部 ID 的状态，`--id` 附单条状态、原文和当前译文。输出为 JSON。`done` 为完成，`pending` 为待处理，`review` 为待复核，`failed` 为初翻失败，`blocked` 表示前置数据未就绪，`unknown` 表示无法核验，`passthrough` / `not_applicable` 表示原样保留和无需处理。`in_progress` 是各阶段有效领取的条目数，与待处理或待复核计数重叠；条目上的 `claim` 列出执行者及截止时间。

润色和校对按各阶段最近一次通过校验并保存的 `next` 配置检查模型、块大小、上下文和材料。完成记录的依赖变化后会显示待复核。阶段配置缺失或材料不可用时显示 `unknown`。

`status` 以只读方式核对原文并查询状态。遇到待恢复的批次或不完整的历史末尾时会报错，请用原翻译命令恢复。翻译持锁期间，查询可读取已发布的进度快照。快照尚未生成时，稍后重试。

`status` 和 `next` 用已保存的润色稿核验润色依赖。校对修订后，仍有效的润色记录继续显示完成。执行 `redo` 后，指定条目按当前保留的译文重新领取。

## 领取与提交

```sh
localization-kit next work --stage polish --worker editor-1 --model model-name
```

`--stage` 为 `polish` 或 `proofread`。`--model` 填执行该任务的实际模型。`next` 读取作品 `materials/` 中的默认术语表和角色卡，`config.toml` 可指定其他材料文件。领取有效期默认 30 分钟，可用 `--ttl 1h` 调整。每块默认最多 20 条，可用 `--max-lines` 调整。默认提供同组前 5 条，可用 `--context` 调整，并附块后 3 条原文。默认输出 JSON，`--format text` 可逐行阅读，包含条目 ID、前后文和已提供的材料。

领取记录保存后，即使刷新阶段快照失败，`next` 也会返回可提交的任务。作品日志会记录任务 ID 和关闭作业时的错误。

润色时可成对传入 `--review-input <初翻审查材料>` 和 `--review-report <报告>`；这两个参数均可重复，分别按顺序配对。报告须与作业原文、术语和所用角色卡一致。领取结果逐条包含审查结论、原文证据及当时的译文；审查底稿或上下文中的译文变化后，该意见标记 `stale: true`，执行者须结合当前材料重新判断。同阶段执行者应使用相同报告，报告变化会使已领取任务失效，并重新判断阶段完成状态；报告不可读取时状态为 `unknown`。

`--context-scope group` 是默认范围。主 agent 可以另行提供全文原文；程序根据同组前文及当前块的译文核验结果，后续块提交译文时可以直接继续领取。确认执行者**只使用领取结果中的材料**时，可用 `--context-scope task`，将译文依赖限制在领取结果所附的前文和当前块。此模式每块必须使用独立执行者会话，不携带之前任务的上下文，也不得额外提供或读取作品上下文；材料不足时释放并报告。程序无法验证执行者是否遵守该约定。两种模式都保留全组原文、关联术语以及初翻所用历史的依赖；原文、术语、初翻历史或附带审查意见的有效性变化仍可能使相关条目待复核。切换范围会重新判断完成状态。

返回 `status: ready` 时，输出含任务 ID、`lines` 中的各条 ID、原文及当前译文，还有同组上下文和处理规则。`cards` 提供已填写的角色卡，`glossary` 提供 `lines` 原文命中的术语。两项为空时各自省略。执行者处理整块，将结果按领取顺序保存为 JSON 数组，例如 `submission.json`：

```json
[{"id":"line-1","dst":"修改后的译文","reason":"调整语气"},{"id":"line-2","dst":"原样确认的译文"}]
```

原样确认时 `dst` 填原译文，`reason` 可以省略。校对有修改时，还需在 `issues` 中记录问题以及当前条目原文中的逐字依据：

```json
[{"id":"line-1","dst":"修正后的译文","reason":"修正漏译","issues":[{"code":"omission","detail":"补回被省略的内容","quote":"原文中的依据"}]}]
```

```sh
localization-kit submit work --worker editor-1 --task 领取结果中的任务ID --file submission.json
localization-kit next work --stage polish --worker editor-1 --model model-name --format text
```

逐条确认整批后，也可以用 `--changes` 提交。改动文件是 UTF-8 JSON 数组，只包含实际修改的条目，仍按正式 ID 填写 `dst`、`reason` 和校对所需的 `issues`。省略的条目按领取底稿原样确认。整批均原样确认时写 `[]`。重试时用同一选项提交同样的内容。

```sh
localization-kit submit work --worker editor-1 --task 领取结果中的任务ID --changes changes.json
```

校对使用相同命令，将 `--stage` 改为 `proofread`，并填写执行校对的实际模型。校对包括对照原文审查、修复和核对修改。无法确认或修复的问题不提交为完成，应释放任务并报告主 agent：

```sh
localization-kit release work --worker editor-1
```

`next` 的 `busy` 表示待处理组正在被占用；`blocked` 表示缺少初翻或术语状态不一致；`done` 仅表示所选阶段在当前模型和材料下没有待处理条目。主 agent 按初翻、润色、校对的顺序组织工作。重复 `next` 会续领本执行者的有效任务；领取过期后可由其他执行者领取。须整块提交，相同提交可安全重试。批次结果提交时若进程中断，下一次作品命令打开状态会完成已记录的整块提交。若中断发生在批次记录保存之前，重新领取并处理待复核条目。该机制针对进程中断；系统断电及底层文件系统故障尚未验收。

作业记录原样确认和修改，原文、模型、块大小、前文范围、关联术语、角色卡、字形规则或相关上下文变化可能使阶段重新进入待处理。同组一次只允许一个未完成的领取。`next` 与 `submit` 遇到作品锁竞争时会短暂重试，持续占用则返回锁错误。

## 指定条目重做

```sh
localization-kit redo work --stage polish --id line-1 --id line-2
localization-kit redo work --stage translate --id line-3
```

`--stage` 可选 `translate`、`polish` 或 `proofread`，`--id` 可重复。润色重做保留当前译文并清除所选条目的润色和校对完成状态。校对重做只清除校对完成状态。初翻重做将所选条目退回待翻，后续依赖其上文的机器结果退回待翻，人工或已润色结果保留并标为待复核。所有原有记录仍保存在作业历史中。重做初翻后重新运行原翻译命令，其他阶段由执行者重新领取。调整 subagent 提示词后如需复查已有条目，也用 `redo` 指定条目。目标组存在活动领取时先等待或释放。

## 润色审计

```sh
localization-kit audit work
```

输出润色阶段状态、每位执行者的改动数量和改动率、每块的条目与原样提交情况，以及当前记录保存的繁体和英文警告。执行者统计按当前条目的润色任务归属计算，不是历史工作量累计；改动率比较润色提交与该次领取前的译文。记录缺少可追溯底稿时计入 `unknown`，不计入改动率。`all_unchanged` 只在能确认整块所有条目仍属于该任务且当前阶段有效时为 true；旧记录若无法确认块大小不会标记。原样提交是合法操作，需结合原文和审查意见判断，必要时用 `redo <作品目录> --stage polish --id ...` 重做。

`audit` 校验参数后打开作品，按需恢复结果历史和刷新阶段 JSON。审计不调用模型。

## API 执行

通过 `run --stage polish|proofread --model <名称>` 调用质量模型时，CLI 使用同一任务领取与提交流程。润色阶段的 `run` 和 `next` 均可成对重复传入 `--review-input 材料 --review-report 报告`，供执行者参考。`polish run` 用于生成供审查的候选。

使用 API 时，程序还会核对模型配置、内置提示词和输出预算。模型请求结果未知时领取会暂时保留。先确认模型已停止，再用错误信息中的 worker 执行 `release`。领取到期后重新运行可能再次请求模型。
