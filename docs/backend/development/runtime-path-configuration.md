# 运行路径与配置契约

## 1. 目的

后端过去把 `../storage` 和 `../ai/src` 解释为“相对于进程当前工作目录”的路径。
同一程序从项目根目录和 `backend` 目录启动时会得到不同结果，因此形成了必须从特定目录启动的隐含条件。

P5.1 引入统一的 `APP_ROOT`，把运行位置与资源位置分开：

```text
APP_ROOT
├── storage                 ← STORAGE_ROOT=storage
├── ai/src                  ← PYTHON_SOURCE_ROOT=ai/src
└── backend
```

## 2. 配置规则

1. 显式设置 `APP_ROOT` 时，它必须是已经存在的绝对目录；
2. 开发环境省略 `APP_ROOT` 时，程序从当前工作目录逐级向上查找项目根；
3. 开发项目根必须同时包含 `backend/go.mod` 和 `ai/src/rag_ai`；
4. `STORAGE_ROOT`、`PYTHON_SOURCE_ROOT` 是绝对路径时直接使用；
5. 这两个变量是相对路径时，固定相对于 `APP_ROOT` 解析；
6. 部署产物没有开发源码标志时，不进行模糊猜测，必须显式设置 `APP_ROOT`；
7. 数据库迁移 SQL 通过 `go:embed` 编译进程序，不需要额外的运行时迁移目录。

## 3. 分层位置

- `internal/config`：发现或校验 `APP_ROOT`，把资源路径转换为绝对路径；
- `cmd/server/main.go`：组合根，先加载路径配置，再把结果传给文件存储和 Python 处理器配置；
- `internal/infrastructure`：接收已经解析好的绝对路径并校验、创建或使用实际资源；
- Domain 与 Application：不感知操作系统路径，也不参与本次改动。

这条边界意味着：配置层决定“资源在哪里”，基础设施层决定“如何使用资源”，业务层只关心用例。

## 4. 开发与部署示例

本地开发可以省略：

```dotenv
APP_ROOT=
STORAGE_ROOT=storage
PYTHON_SOURCE_ROOT=ai/src
```

部署环境使用绝对路径：

```dotenv
APP_ROOT=E:/services/rag-platform
STORAGE_ROOT=storage
PYTHON_SOURCE_ROOT=ai/src
```

也可以为单项资源提供绝对路径；绝对路径不会再次拼接到 `APP_ROOT`。

Compose 部署固定使用容器内路径：

```dotenv
APP_ROOT=/app
STORAGE_ROOT=storage
PYTHON_SOURCE_ROOT=ai/src
PYTHON_EXECUTABLE=python3
```

因此上传目录解析为 `/app/storage`，Python 源码基准解析为 `/app/ai/src`。宿主机的 `./storage`
绑定到 `/app/storage`，容器重建后上传文件仍然保留。

## 5. 验收标准

- 路径配置单元测试通过；
- 后端全量 Go 测试与 `go vet ./...` 通过；
- 同一个已构建服务分别以项目根目录和 `backend` 为工作目录启动，`GET /health` 均返回 `200`；
- 两种启动方式最终解析到相同的存储目录和 Python 源码目录；
- 默认验收关闭远程 Embedding 与问答能力，不产生远程调用费用。
