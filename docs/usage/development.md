# 源码构建、测试与提交检查

在仓库根目录执行以下命令。依赖与任务由 [Pixi](https://pixi.sh/) 统一管理（若不使用 Pixi，可分别使用 Go `>= 1.26`、Python `>= 3.11` 与 `pnpm` 执行对应命令）。

## 常用开发任务

| 命令 | 用途 |
| --- | --- |
| `pixi run build` | 构建 Go CLI 可执行文件到 `build/` |
| `pixi run -e image download-fonts` | 下载漫画嵌字所需的开源字体到 `data/fonts/` |
| `pixi run package` | 构建包含程序、运行数据、配置 schema 与第三方声明的归档 |
| `pixi run lint` | 格式化并执行 Go (`go vet`)、Python (`ruff`) 与 TOML (`tombi`) 静态分析 |
| `pixi run test` | 运行 Go 单元与集成测试 |
| `pixi run test-cli` | 构建 CLI 并运行跨进程集成测试 |
| `pixi run -e image test-py` | 运行 Python 单元测试（漫画组装、OCR、桥接、渲染、质检与预览服务） |
| `pixi run -e web web-install` | 按锁文件安装网页前端依赖 |
| `pixi run -e web web-typecheck` | 检查网页 TypeScript 类型 |
| `pixi run -e web web-test` | 运行网页单元测试 |
| `pixi run -e web web-build` | 构建审图网页到 `src/web/dist/` |
| `pixi run -e web web-dev` | 启动含样例清单的网页开发服务器 |

## 代码结构约定

- **Go CLI 与文本引擎**（`src/cmd/`、`src/internal/`）：命令路由与参数解析集中在 [command.go](../../src/internal/cli/command.go)，文本翻译、状态机、术语、角色卡和评测逻辑全部在此层实现。
- **Python 图像流水线**（`src/image/`）：默认 `-e image` 环境保持纯 CPU（零顶层 `torch`/`transformers`/`onnxruntime` 导入），所有 GPU 模型会话集中在 [vision.py](../../src/image/vision.py)。
- **Web 审图工作台**（`src/web/`）：Vite + React + TypeScript 单页应用，内置样例清单位于 `src/web/public/`。

## 提交前检查

提交代码前请运行：

```sh
pixi run lint
pixi run test
pixi run -e image test-py
pixi run -e web web-typecheck
pixi run -e web web-test
```

相关用户流程文档见 [使用文档总索引](README.md)。
