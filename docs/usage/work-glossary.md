# 作品术语表

术语表用于统一人名、称谓、专有名词和固定译法。程序可读取已有表辅助翻译，也可准备作品材料、调用指定模型提取候选、校验证据和确认术语。具体命令见[术语与角色卡](polish.md#术语与角色卡)，设计依据见[术语表与角色卡](../adrs/0006-glossary-and-character-cards.md)。

## 组织和格式

跨作品稳定的词可放通用表；作品专用词和覆盖项放作品表。作品 `config.toml` 按通用表、作品表的顺序列出路径，后一个表按 `src` 覆盖前一表中的同名条目。

```json
{
  "entries": [
    {
      "src": "先生",
      "dst": "老师",
      "kind": "term",
      "constraint": "hard",
      "variants": ["先生方"],
      "note": "称呼"
    }
  ]
}
```

`src` 和 `dst` 必须非空；`kind` 为 `term`、`name` 或 `onomatopoeia`，默认 `term`；`constraint` 为 `hard` 或 `soft`，默认 `soft`。`variants` 和 `note` 可省略。动词活用、别名等须按实际原文补入 `variants`；`dst` 写一个确定译法，不写斜杠分隔的候选。

`term`、`name` 按子串匹配，拟声词按独立词段或叠词匹配。原文中命中的 hard 与 soft 条目都会送入模型；hard 译法缺失时产生警告，当前不会单独阻断提交。作品中的歧义和人称仍须人工核对。

## 翻译、修订和导出

```toml
glossaries = ["materials/common.json", "materials/work.json"]
```

路径相对于作品目录解析。程序不会自动加载通用表，应自行保存使用过的版本；翻译、修订和导出读取同一组已启用术语：

```sh
localization-kit translate 作品目录
localization-kit edit 作品目录 --file edits.jsonl
localization-kit export 作品目录 --stage proofread --out exports/review.jsonl
```

术语变化后重新运行翻译，关联组的机器结果会按新签名重新判断，人工结果保留并按需复核；原文变化时使用新工作区及 `--previous`。VTT / artc 的导出后处理使用当前配置的术语表。修改表前后应比较影响范围和输出，尤其注意人名覆盖和拟声词连写漏匹配。

## 作品表制作建议

准备作品表时可先收集全文、出现频次、前后文和现有通用词，再确认角色名、称谓和作品专属译法。候选词可能含噪声，应对照原文确认；出现多种解释时先由人决定，再写入唯一的 `dst`。角色事实和称谓规则需要原文依据，随剧情变化的称呼要保留上下文。

程序可以准备原文材料（`terms init`），调用指定模型提取候选（`terms run`），并校验术语与角色卡的结构、状态和证据（`terms validate`），人工确认后再写入作品术语表。模型候选默认待确认，未决或冲突内容须先处理。
