<p align="center">
  <h1 align="center">精小弘 Jxh-Go</h1>
  <p align="center">一个基于 Go、NapCat 和 Eino 重构的精弘 QQ 群助手</p>
</p>

<p align="center">
  <a href="https://github.com/cloudwego/eino"><img alt="Eino" src="https://img.shields.io/badge/Eino-Agent-blue?style=flat-square"></a>
  <a href="https://github.com/NapNeko/NapCatQQ"><img alt="NapCat" src="https://img.shields.io/badge/NapCat-OneBot11-green?style=flat-square"></a>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat-square&logo=go">
  <img alt="MySQL" src="https://img.shields.io/badge/MySQL-8.4+-4479A1?style=flat-square&logo=mysql&logoColor=white">
</p>



## 什么是精小弘

精小弘是面向浙江工业大学相关 QQ 群场景的问答机器人。它保留原 `MangoGovo/qqbot-JXH` 的核心能力：WPS 回复表、关键词回复、菜单式问答、管理员命令、黑名单、定时任务、入群欢迎和引用图。

这个仓库是 Go 重构版本。实现上使用 NapCat 接入 OneBot 11，用 MySQL 保存知识库和运行状态，并用 Eino 接入 `/ai`，让同一份 WPS 知识库既能做精确回复，也能做检索问答。

## 特性

本项目目前聚焦原功能迁移和可迭代架构：

- **NapCat 接入** — 默认使用 OneBot 11 正向 WebSocket，bot 主动连接 NapCat。
- **WPS 知识库** — 兼容旧两列回复表，第三列作为维护备注，不入库、不参与 RAG。
- **菜单树解析** — 自动解析 `%编号` 菜单结构，生成知识路径和 `/ai` 检索正文。
- **关键词回复** — 支持 keyword 和 aliases 精确匹配。
- **知识库重载** — `/reload` 从 WPS 同步数据，并刷新关键词缓存和 `/ai` retriever。
- **AI 问答** — `/ai <问题>` 基于同一知识库检索回答，模型未配置时使用抽取式 fallback。
- **群管理能力** — 管理员、黑名单、禁言、NapCat 重启、定时任务。
- **引用图** — `/q` 调用外部 quote 服务生成引用图。
- **事件去重** — `processed_events` 记录 NapCat 重连边界的已处理事件，并定期清理。
- **显式 Schema** — MySQL 表结构由 SQL 文件初始化，不在应用启动时自动迁移。

## 快速开始

### 环境要求

| 依赖 | 说明 |
| --- | --- |
| Go 1.25+ | 编译和运行 bot |
| Docker Compose | 启动 MySQL、NapCat 等外部依赖 |
| MySQL 8.4+ | 保存知识库、管理员、黑名单、定时任务和事件去重 |
| NapCatQQ | QQ 登录和 OneBot 11 协议适配 |
| OpenAI 兼容模型 API | 可选，用于 `/ai` |

### 配置与启动

```bash
# 1. 复制配置
cp config.example.yaml config.yaml

# 2. 启动外部依赖
NAPCAT_UID=$(id -u) NAPCAT_GID=$(id -g) docker compose up -d mysql napcat

# 3. 启动 bot
go run ./cmd/bot -config config.yaml
```

`docker-compose.yaml` 只启动外部依赖，不运行 Go bot 服务本身。bot 保持单独运行，方便本地调试、日志观察和后续部署到 systemd、supervisor 或其他进程管理器。

## NapCat 配置

NapCat 由 compose 作为外部依赖启动。启动后打开 WebUI：

```text
http://127.0.0.1:6099/webui
```

WebUI 登录 token 可通过容器日志查看：

```bash
docker logs napcat
```

在 NapCat WebUI 中登录 QQ，并开启 OneBot 11 正向 WebSocket：

- 监听地址使用 `0.0.0.0`，不要填 `127.0.0.1`。NapCat 在容器内运行，填 `127.0.0.1` 会只监听容器内部 loopback，宿主机上的 bot 会连接失败。
- 监听端口使用 `3001`。
- token 设置为一个自定义密钥，例如 `change-me`。
- `config.yaml` 里的 `onebot.access_token` 必须和 NapCat 端 token 完全一致。

对应 bot 配置：

```yaml
onebot:
  ws_url: "ws://127.0.0.1:3001"
  access_token: "change-me"
```

如果修改了 NapCat 端口，需要同步修改 `onebot.ws_url`。如果修改了 NapCat token，需要同步修改 `onebot.access_token`。

NapCat 数据通过 Docker volume 持久化：

| volume | 容器路径 | 用途 |
| --- | --- | --- |
| `napcat_qq` | `/app/.config/QQ` | QQ 登录态 |
| `napcat_config` | `/app/napcat/config` | NapCat 配置 |
| `napcat_plugins` | `/app/napcat/plugins` | NapCat 插件 |

## Docker Compose

compose 默认启动以下外部依赖：

| 服务 | 作用 | 暴露端口 |
| --- | --- | --- |
| `mysql` | GORM/MySQL 数据库 | `3306` |
| `napcat` | QQ 登录和 OneBot 11 协议适配 | `3000`, `3001`, `6099` |

引用图服务默认不启动，需要时启用 `quote` profile：

```bash
docker compose --profile quote up -d
```

向量数据库默认不启动。Milvus 由 `milvus-etcd`、`milvus-minio`、`milvus-standalone` 三个服务组成，需要向量检索时启用 `vector` profile：

```bash
docker compose --profile vector up -d
```

Milvus 会暴露：

| 服务 | 用途 | 暴露端口 |
| --- | --- | --- |
| `milvus-standalone` | Milvus gRPC/API | `19530`, `9091` |
| `milvus-minio` | Milvus 对象存储依赖 | `9000`, `9001` |
| `milvus-etcd` | Milvus 元数据依赖 | 不暴露到宿主机 |

当前 `/ai` 可先走 MySQL 知识库的文本检索；`vector.enabled` 打开后，`config.yaml` 里的 `vector.address` 应保持为 `127.0.0.1:19530`，因为 Go bot 是在宿主机单独运行。

## 数据库 Schema

应用运行时不使用 `AutoMigrate`。表结构以 `deploy/mysql/init/001_schema.sql` 为准。

`docker-compose.yaml` 会把该 SQL 文件挂载到 MySQL 容器的 `/docker-entrypoint-initdb.d/001_schema.sql`。MySQL 官方 entrypoint 会在 `mysql_data` volume 首次初始化时自动执行它，因此新环境只需要启动 MySQL：

```bash
docker compose up -d mysql
```

如果 `mysql_data` volume 已经存在，MySQL 不会重复执行 `/docker-entrypoint-initdb.d` 里的 SQL。需要重建空库时可以删除 volume 后重新启动：

```bash
docker compose down
docker volume rm jxh-go_mysql_data
docker compose up -d mysql
```

如果不想删除数据，也可以手动执行增量 SQL，或直接执行当前 schema：

```bash
docker compose exec -T mysql mysql -ujxh -pjxh_password jxh_bot < deploy/mysql/init/001_schema.sql
```

GORM 连接数据库的逻辑在 `cmd/bot/main.go`：如果 `database.dsn` 非空，直接使用完整 DSN；否则按 `database.user/password/host/port/name/charset/parse_time/loc` 拼出 MySQL DSN，再通过 `gorm.Open(mysql.Open(dsn), &gorm.Config{})` 建立连接。

需要重新生成 GORM query/model 代码时，先确保 MySQL 已启动，然后运行：

```bash
go run ./cmd/gormgen -config config.yaml -schema deploy/mysql/init/001_schema.sql
```

`cmd/gormgen` 会先执行 schema SQL，再通过 `gorm.io/gen` 读取当前 MySQL 表结构，输出到 `internal/storage/query`。

## WPS 知识表

`wps.share_url` 应填写网页端“右键文件 -> 导出文档链接”得到的链接，或可直接下载的 xlsx 地址。普通 `365.kdocs.cn/l/...` 分享页只用于浏览器打开文档，程序请求时通常会返回登录/跳转 HTML，不能直接导入；受保护文档还需要配置 `wps.sid` 或 `JXH_WPS_SID`。

基础列兼容旧回复表：

| 列 | 字段 | 说明 |
| --- | --- | --- |
| A | `keyword` | 关键词 |
| B | `answer` | 标准回答 |
| C | 维护备注 | 不入库，不参与关键词回复或 `/ai` |

可选列：

| 列 | 字段 | 说明 |
| --- | --- | --- |
| D | `aliases` | 同义问法，多个用分隔符隔开 |
| E | `category` | 分类 |
| F | `usage` | 用途控制 |
| G | `status` | 启用状态 |
| H | `source_id` | 稳定 ID，修改 keyword 时用于保留同一条记录 |

导入器会解析 `%编号` 菜单树，生成 `path` 和 RAG `content`。`content` 会包含菜单路径、关键词、别名和回答，便于 `/ai` 检索。

## 常用命令

| 命令 | 说明 |
| --- | --- |
| `/test` | 连通性测试 |
| `/reload` | 从 WPS 同步知识库并刷新缓存 |
| `/ai <问题>` | 基于知识库检索回答 |
| `/q` | 回复消息后生成引用图 |
| `/admin ...` | 管理员、黑名单、定时任务等管理命令 |

## 配置文件

主配置文件是 `config.yaml`，可以从 `config.example.yaml` 复制得到。示例配置里已经写明每个字段的用途。

常用敏感配置可通过环境变量覆盖：

| 环境变量 | 作用 |
| --- | --- |
| `JXH_ONEBOT_TOKEN` | OneBot WebSocket token |
| `JXH_ONEBOT_WS_URL` | NapCat 正向 WebSocket 地址 |
| `JXH_WPS_SID` | WPS 登录态 sid |
| `JXH_MYSQL_PASSWORD` | MySQL 密码 |
| `JXH_MYSQL_DSN` | 完整 MySQL DSN |
| `JXH_AI_PROVIDER` | ChatModel 提供方，支持 `openai`、`ark` |
| `JXH_AI_BASE_URL` | ChatModel base_url；Ark 可留空使用默认地址 |
| `JXH_AI_API_KEY` | ChatModel API Key |
| `JXH_EMBEDDING_PROVIDER` | Embedding 提供方，支持 `openai`、`ark` |
| `JXH_EMBEDDING_BASE_URL` | Embedding base_url；Ark 可留空使用默认地址 |
| `JXH_EMBEDDING_API_KEY` | Embedding API Key |

## 目录结构

| 路径 | 说明 |
| --- | --- |
| `cmd/bot` | bot 启动入口 |
| `cmd/gormgen` | 根据 MySQL schema 生成 GORM query/model 的工具 |
| `internal/napcat` | NapCat SDK 适配层 |
| `internal/bot` | 群消息处理管线 |
| `internal/knowledge` | WPS 解析、关键词索引、文本检索 |
| `internal/storage` | GORM repository 和存储模型 |
| `internal/ai` | `/ai` RAG 服务和 Eino ChatModel 适配 |
| `deploy/mysql/init` | MySQL 初始化 SQL |
