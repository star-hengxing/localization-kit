# 图片翻译审阅前端

从仓库根目录启动审图预览或运行网页开发任务。[审图工作台](../../docs/usage/review-workbench.md) · [构建与测试](../../docs/usage/development.md)

`pixi run -e web web-dev` 默认加载双页样例，包含旧成品对照和 QA 告警。

静态审阅时，将实际项目的 `review-manifest.json` 和图片放在 `dist/`，保留清单中图片相对于清单的路径，再用本机 HTTP 静态服务打开。静态预览可断网使用。页面自动在当前浏览器保存草稿，导出区提供 Markdown 复制、下载和 TOML 草稿。

| 快捷键 | 操作 |
| --- | --- |
| `S` / `D` / `F` | 切换卷帘、左右并排和按住闪烁 |
| 空格 | 在闪烁视图中临时显示原图或旧成品 |
| `0` / `1` | 缩放适配窗口或按实际像素显示 |
| `[` / `]` | 上一页或下一页 |
| 左右方向键 | 分割线获得焦点时调整位置 |

## 本地服务接口

`GET /api/feedback` 返回版本化反馈文档，可用 `{"feedback": ...}` 包装。无存档时返回 `{"version":1,"entries":[]}`。前端按 `page_id`、`unit_id` 与 `page_fingerprint` 识别记录，并以 `field_updated_at` 合并服务端记录和浏览器草稿。

`POST /api/feedback` 接收 `{"feedback": <文档>}`，也兼容带 `markdown` 的请求。本地服务按字段时间戳合并反馈，从同一份记录写出 `feedback.json` 和 `feedback.md`，并在 `{"ok":true,"feedback": <已保存文档>}` 中返回合并结果。前端也接受仅含 `{"ok":true}` 的响应或 HTTP 204。

反馈记录包含 `status`（`approved`、`problem` 或 `pending`）、`tags`、`note`、ISO 8601 `updated_at` 和可选 `overrides`。整页的 `unit_id` 为 `__page__`。旧记录的指纹缺失时按未知指纹读取。

## 覆盖草稿

「应跳过」生成 `action = "skip"`。当前页的建议可导出为 `overrides.toml`。手动分列须能拼回译文全文。标签和备注保存在 TOML 注释中。已有同名 `[units."ID"]` 表时，将建议字段合并到原表。
