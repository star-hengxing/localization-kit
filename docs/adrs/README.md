| 文件 | 摘要 |
| --- | --- |
| `0001-text-core-architecture.md` | Go 核心聚焦格式适配、术语规则、模型调用、状态管理与导出，与外部抓取/出书/GPU 调度解耦 |
| `0002-model-session-ownership.md` | 工具通过 OpenAI 兼容 API 直连模型服务，模型进程与会话生命周期由外部调用方或部署层管理 |
| `0003-game-jsonl-handoff.md` | 通用 JSONL 协议仅交付原文与译文，游戏控制码保护、映射保管与写回由调用方负责 |
| `0004-job-state-and-recovery.md` | 以作品锁 + 追加式 `results.jsonl` 持久化历史，阶段视图可重建，支持崩溃前滚与断点续跑 |
| `0005-repeated-source-reuse.md` | 跨版本复用按条目、分组原文、术语、模型与上下文签名判定，不按短文本相等盲目扇出 |
| `0006-glossary-and-character-cards.md` | 术语表显式加载并按 `src` 覆盖；拟声词采用独立词段匹配以防助词误命中；按组保守失效 |
| `0007-translation-review-and-polish.md` | 区分初翻、审查、润色与校对阶段，API 与 Agent 共享领取/底稿校验契约并保护人工修订 |
