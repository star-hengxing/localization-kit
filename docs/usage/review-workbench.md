# 审图工作台（Review Workbench）

审图工作台提供漫画渲染前后的四视图对比、QA 告警定位、多场景动态切换、字段级反馈保存与 `overrides.toml` 建议导出。

## 启动方式

先构建前端静态资源并确保已安装 `image`（或 `image-gpu`）环境：

```sh
pixi install -e image -e web --locked
pixi run -e web web-install
pixi run -e web web-build
```

- **零模型体验随仓样例**：
  ```sh
  pixi run localization-kit image review-serve --dir src/web/public
  ```
- **打开单个渲染目录**：
  ```sh
  pixi run localization-kit image review-serve --dir work/review
  ```
- **扫描根目录下的多场景**（扫描最多 5 层子目录，按 `review-manifest.json` 修改时间打开最近场景）：
  ```sh
  pixi run localization-kit image review-serve --root work
  ```
- **启动空工作台（随后通过 API 推送场景）**：
  ```sh
  pixi run localization-kit image review-serve
  ```

服务默认监听 `127.0.0.1:4173`，`--port 0` 由系统分配端口，`--web-dist` 可指定自定义网页构建目录。支持在浏览器 URL 中使用 `?scene=<id>&page=<页号>` 直达指定场景与页码。

## 场景管理 HTTP / SSE 接口

服务端提供本地场景注册与热切换接口，已打开的浏览器界面会通过 SSE 自动同步更新并保留当前缩放、视图和草稿。写请求需使用回环主机名并携带请求头 `X-Localization-Kit-Request: 1`。

| 接口 | 用途 |
| --- | --- |
| `GET /api/scenes` | 获取已注册场景列表与当前 `active_id` |
| `POST /api/scenes` | 用 `dir` 注册或刷新场景，可选 `label` 与 `activate: false` |
| `POST /api/scenes/active` | 用 `id` 切换当前激活场景 |
| `POST /api/scenes/scan` | 用 `root` 重新扫描场景目录 |
| `DELETE /api/scenes/<id>` | 从工作台列表中移除场景（不删除磁盘成品） |
| `GET /api/events` | 订阅 SSE 事件（`scenes_updated`、`active_changed`） |

使用 `curl` 推送新渲染场景示例：

```sh
curl -sS -X POST http://127.0.0.1:4173/api/scenes \
  -H "Content-Type: application/json" \
  -H "X-Localization-Kit-Request: 1" \
  -d "{\"dir\":\"$(pwd)/work/review\",\"label\":\"第 1 话\"}"
```

## 反馈与覆写导出

- **保存反馈到目录**：在对应场景目录写入 `feedback.json` 与 `feedback.md`；前后端均基于字段级时间戳（`field_updated_at`）合并并发修改，重渲染后旧指纹反馈自动标为待复核。
- **导出 `overrides.toml`**：一键导出当前页的译文修订、跳过或字体/擦除调整建议，可直接回填到单页目录后重渲染。
