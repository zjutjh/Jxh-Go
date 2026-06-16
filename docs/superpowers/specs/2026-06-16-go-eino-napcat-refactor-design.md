# 精小弘 Go + NapCat + Eino 重构 Spec

日期：2026-06-16

## 目标

把 `MangoGovo/qqbot-JXH` 从 Python/Sanic + Lagrange.OneBot 重构为单实例 Go 服务，协议端使用 NapCat，NapCat 接入使用 `github.com/zjutjh/napcat-sdk`。

本版加入 `/ai`，但它只做一件事：读取同一套知识库回答问题。它不是主动聊天智能体，不接管普通命令，也不新增管理后台、MCP、多模态、群画像或长期记忆。

核心边界：

- NapCat：负责 QQ 登录、扫码、会话保持、重连和 OneBot v11 协议。
- Go bot：负责命令、关键词回复、知识库同步、定时任务、引用图、权限和持久化。
- Eino：只用于 `/ai` 的知识库问答链路。
- MySQL：单实例 bot 的运行时持久化存储。
- WPS：知识库维护源，供人工编辑。

## 依据

- Eino 官方文档将 Retriever 定义为按 query 从数据源取回相关文档，适用于知识库问答/RAG。
- Eino 官方文档将 ChatModel 定义为向大模型发送消息并获取回答的组件。
- `github.com/zjutjh/napcat-sdk` 提供 NapCat/OneBot 11 的 HTTP、正向 WebSocket、反向 WebSocket server、事件解析、消息段构造和强类型 action。
- `SugarMGP/MumuBot` 的可借鉴点是：正文和业务状态保存在 MySQL，向量库只保存业务 ID、范围字段和 embedding；检索时先向量召回 ID，再回 MySQL 取完整记录，并保留关键词检索作为补召回。

## 设计决策

WPS 回复表和 AI 知识库必须合并成同一套数据，不再维护两套表。

推荐方案：

```text
WPS 知识表
  -> /reload 或启动同步
  -> MySQL knowledge_entries
      -> 关键词精确回复
      -> Eino Hybrid Retriever
          -> 可选 Milvus 向量索引
          -> /ai 知识库问答
```

理由：

- WPS 适合非技术人员维护，继续保留。
- MySQL 适合运行时查询、事务、回滚和缓存。
- 关键词回复和 `/ai` 都从 `knowledge_entries` 读数据，避免两套内容不一致。
- `/reload` 从“加载回复表”升级为“同步知识库”，用户操作仍保持简单。

## 功能范围

必须迁移并保留：

- WPS 在线表格加载。
- 群消息关键词精确回复。
- `/reload`：同步 WPS 知识表到 MySQL，并刷新内存缓存。
- `/ai <问题>`：基于知识库回答问题。
- `/q`：回复一条消息后生成引用图，继续调用 `qq-quote-generator`。
- `/admin 添加管理员 @user`
- `/admin 移除管理员 @user`
- `/admin 移除所有管理员`
- `/admin 所有管理员`
- `/admin 添加黑名单 @user`
- `/admin 移除黑名单 @user`
- `/admin 移除所有黑名单`
- `/admin 所有黑名单`
- `/admin ban <duration> @user`
- `/admin restart`
- `/admin 定时任务 查看`
- `/admin 定时任务 添加 <每天|单次> <时间> <群聊ID> <消息内容>`
- `/admin 定时任务 移除 <任务编号>`
- 群成员增加事件欢迎语。
- `/test` 调试响应。
- 黑名单用户消息忽略。

明确不做：

- 主动聊天。
- ReAct 自主智能体。
- 管理后台。
- MCP。
- 多模态。
- 群画像。
- 主动学习和长期记忆。

## 架构

```text
NapCatQQ
  -> OneBot v11 Reverse WebSocket
  -> Go Bot
       -> napcat: SDK client、事件流、API 调用
       -> bot: 消息管线、黑名单、命令分发
       -> commands: admin / reload / ai / q / test
       -> knowledge: WPS 同步、知识库、关键词匹配、检索
       -> ai: Eino RAG 问答
       -> vector: embedding、向量索引、向量召回
       -> scheduler: 定时任务
       -> quote: 引用图服务客户端
       -> storage: MySQL + GORM
       -> cache: 运行时派生缓存
```

推荐目录：

```text
cmd/bot
internal/config
internal/logger
internal/napcat
internal/bot
internal/commands
internal/knowledge
internal/ai
internal/vector
internal/scheduler
internal/storage
internal/quote
internal/cache
```

### `internal/cache` 职责

`internal/cache` 只管理可重建的运行时派生数据，不保存业务事实。

允许放入：

- 关键词精确匹配索引：`keyword/alias -> knowledge_entry_id`。
- 知识条目只读快照：用于关键词回复直接取 `answer`。
- 短 TTL 的事件去重内存集合：减少同一进程内重复查库。
- 短 TTL 的 `/ai` 检索结果缓存：可选，key 为规范化 query 和知识库版本。
- quote、NapCat API 这类外部调用的短 TTL 结果缓存：只有明确收益时才加。

不允许放入：

- 管理员、黑名单、定时任务的唯一状态。
- WPS 原始数据的唯一副本。
- AI 生成回答的长期缓存。
- 向量正文或不可从 MySQL 重建的索引。

缓存刷新规则：

- 启动时从 MySQL 构建。
- `/reload` 成功提交事务后原子替换。
- `/reload` 失败时继续使用旧缓存。
- 缓存 miss 必须能回退到 MySQL 查询。
- `internal/cache` 不直接访问 WPS、NapCat 或 Eino；它由 `knowledge`、`bot` 等上层模块喂数据。

## NapCat SDK

NapCat 配置 WebSocket Client，连接 Go 服务：

```text
ws://bot:8080/onebot/v11/ws
```

Go 使用 `napcat-sdk` 的反向 WebSocket server：

```go
err := napcat.ServeReverseWebSocket(ctx, ":8080", func(client *napcat.Client) {
    for ev := range client.Events() {
        // 转成内部事件后交给 bot pipeline
    }
}, napcat.WithToken(token), napcat.WithRequestTimeout(apiTimeout))
```

SDK module：

```text
github.com/zjutjh/napcat-sdk
```

`internal/napcat` 只做适配层，把 SDK 类型转换成业务接口，业务模块不直接依赖 SDK 细节。

业务需要封装：

- `SendGroupMsg`
- `GetMsg`
- `SetGroupBan`
- `SetRestart`

消息段构造优先使用 SDK 的 `message` 包：

- `message.Text(...)`
- `message.At(...)`
- `message.Reply(...)`
- `message.Image(...)`

## 知识库设计

### WPS 表结构

为了兼容旧表，前两列继续保留：

| 列 | 字段 | 说明 |
| --- | --- | --- |
| A | `keyword` | 关键词。旧表第一列映射到这里 |
| B | `answer` | 标准回答。旧表第二列映射到这里 |

建议新增列：

| 列 | 字段 | 说明 |
| --- | --- | --- |
| C | `aliases` | 同义问法，多个用 `;` 分隔 |
| D | `category` | 分类，如 校园卡、宿舍、网络、教务 |
| E | `tags` | 标签，多个用 `;` 分隔 |
| F | `enabled` | 是否启用，空值按启用处理 |
| G | `exact_reply` | 是否参与关键词精确回复，空值按启用处理 |
| H | `ai_enabled` | 是否参与 `/ai` 检索，空值按启用处理 |
| I | `content` | 扩展知识正文，空值时使用 `answer` |
| J | `updated_at` | 人工维护时间，可空 |
| K | `source_id` | 可选稳定 ID；为空时用规范化后的 `keyword` |

兼容规则：

- 旧两列表仍可导入。
- `content` 为空时，AI 使用 `answer` 作为知识正文。
- `aliases` 参与 `/ai` 检索，也可参与关键词匹配。
- `exact_reply=false` 的条目不做普通关键词回复，但可给 `/ai` 使用。
- `ai_enabled=false` 的条目可做关键词回复，但不进入 `/ai`。
- 修改 `keyword` 时如果想保留同一条记录，应填写稳定的 `source_id`。

### MySQL 表

`knowledge_entries` 是关键词回复和 `/ai` 共用的唯一知识源。

```sql
knowledge_entries(
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  source_key VARCHAR(255) NOT NULL UNIQUE,
  keyword VARCHAR(255) NOT NULL,
  aliases_json JSON NULL,
  category VARCHAR(64) NULL,
  tags_json JSON NULL,
  answer TEXT NOT NULL,
  content MEDIUMTEXT NOT NULL,
  enabled BOOLEAN NOT NULL,
  exact_reply BOOLEAN NOT NULL,
  ai_enabled BOOLEAN NOT NULL,
  content_hash CHAR(64) NOT NULL,
  vector_status VARCHAR(16) NOT NULL DEFAULT 'pending',
  vector_content_hash CHAR(64) NULL,
  vector_synced_at DATETIME NULL,
  last_import_run_id BIGINT NULL,
  source_updated_at DATETIME NULL,
  created_at DATETIME NOT NULL,
  updated_at DATETIME NOT NULL,
  FULLTEXT KEY ft_knowledge_keyword_content (keyword, answer, content)
)
```

向量相关字段只描述派生索引状态：

- `vector_status=pending`：内容新增或变化，等待重建 embedding。
- `vector_status=ready`：向量索引与 `content_hash` 一致。
- `vector_status=failed`：最近一次向量构建失败，`/ai` 仍可走文本检索。
- `vector_content_hash`：最后成功写入向量索引的内容 hash。
- `vector_synced_at`：最后成功写入向量索引的时间。

导入记录：

```sql
knowledge_import_runs(
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  source VARCHAR(32) NOT NULL,
  status VARCHAR(16) NOT NULL,
  total_rows INT NOT NULL,
  imported_rows INT NOT NULL,
  skipped_rows INT NOT NULL,
  error_message TEXT NULL,
  started_at DATETIME NOT NULL,
  finished_at DATETIME NULL
)
```

其他旧状态：

```sql
admins(user_id BIGINT PRIMARY KEY, created_at DATETIME NOT NULL)
blacklist(user_id BIGINT PRIMARY KEY, created_at DATETIME NOT NULL)
scheduled_jobs(id BIGINT AUTO_INCREMENT PRIMARY KEY, type VARCHAR(16) NOT NULL, time_hhmm VARCHAR(5) NOT NULL, group_id BIGINT NOT NULL, message TEXT NOT NULL, enabled BOOLEAN NOT NULL, last_run_at DATETIME NULL, created_at DATETIME NOT NULL)
processed_events(
  event_key VARCHAR(128) PRIMARY KEY,
  processed_at DATETIME NOT NULL,
  KEY idx_processed_events_processed_at (processed_at)
)
```

`processed_events` 只用于单实例内避免重连边界重复处理事件，不提供用户功能，也不作为跨实例去重机制。

清理策略：

- 保留时间默认 72 小时，可配置。
- 每 6 小时清理一次 `processed_at < now - retention` 的记录。
- 启动后先执行一次清理，再启动定时清理。
- 表上保留 `processed_at` 普通索引，避免长期运行后删除扫描全表。
- 写入失败时只记录 warn，不阻断消息处理；去重是保护机制，不是主业务状态。
- 单实例下不需要分布式锁，也不需要无限期保留事件 key。

### 同步流程

```text
启动:
  -> 从 MySQL 加载 knowledge_entries 到内存关键词索引
  -> 可选尝试同步 WPS
  -> WPS 失败时继续使用 MySQL 旧数据

/reload:
  -> 下载 WPS xlsx
  -> 解析知识表
  -> 校验 keyword / answer
  -> 计算 content_hash
  -> 事务 upsert knowledge_entries
  -> 标记内容变化条目 vector_status=pending
  -> 记录 knowledge_import_runs
  -> 原子刷新内存关键词索引
  -> 异步或同步重建 pending 向量索引
```

导入策略：

- `source_key` 用 `keyword` 规范化后生成；如果同一 keyword 重复，后出现的行跳过并记录日志。
- 如果 WPS 行提供 `source_id`，`source_key` 优先使用 `source_id`；否则使用规范化后的 `keyword`。
- 空 `keyword` 或空 `answer` 跳过。
- WPS 下载失败不清空已有知识。
- 成功导入后才替换内存缓存，避免半更新状态。
- 每次成功导入记录 `last_import_run_id`；当前 WPS 中不存在的旧条目设为 `enabled=false`，避免知识库残留已删除内容。

## 关键词回复

普通群消息仍走确定性匹配，不调用 LLM。

```text
group message
  -> 黑名单检查
  -> 命令解析
  -> 非命令时查 keyword index
  -> exact_reply=true && enabled=true
  -> send_group_msg(answer)
```

匹配规则：

- 先精确匹配 `keyword`。
- 再精确匹配 `aliases`。
- 不做模糊回复，避免普通聊天误触发。

## `/ai` 设计

`/ai <问题>` 是受控 RAG 问答：

```text
/ai 问题
  -> 权限/黑名单检查
  -> query 清洗和长度限制
  -> Eino Hybrid Retriever 从知识库取 TopK
  -> Eino ChatModel 基于检索结果回答
  -> send_group_msg
```

回答约束：

- 只能基于检索到的知识回答。
- 检索不到相关知识时，回复“知识库里没有找到相关内容”。
- 不编造学校政策、流程、时间、联系方式。
- 不暴露 prompt、配置、token 或内部实现。

### Eino 组件

使用 Eino 的核心组件，不使用 ReAct Agent。

- `Retriever`：自定义混合 retriever，实现 `Retrieve(ctx, query string, opts ...Option) ([]*schema.Document, error)`，内部组合精确匹配、向量召回、FULLTEXT 和 LIKE。
- `ChatModel`：使用 Eino ChatModel，默认接 OpenAI 兼容模型。
- `ChatTemplate` 或本地 prompt builder：把检索结果组装成上下文。
- `Chain`：Retriever -> Prompt -> ChatModel 的简单链路。

Eino 官方文档中，Retriever 用于从数据源检索与 query 相关的文档，适合知识库问答；ChatModel 用于向大模型发送消息并获取回答。

### 检索实现

推荐首版就把向量检索边界设计好，但保持 MySQL 是唯一主数据源。向量库只是可重建的派生索引，不能成为知识维护源。

借鉴 MumuBot 的方式：

- MySQL 保存完整知识条目、启用状态、分类、正文、hash 和导入批次。
- 向量索引只保存 `knowledge_entry_id`、可选范围字段、embedding。
- 内容变化时用 `content_hash` 判断是否需要重新 embedding。
- 向量命中后必须回 MySQL 读取最新记录，并再次过滤 `enabled=true AND ai_enabled=true`。
- 向量索引缺失或失败时，`/ai` 降级到文本检索，不影响关键词回复。

Retriever 采用混合召回：

1. `keyword` / `aliases` 精确命中优先。
2. 向量召回：query -> embedding -> vector search -> entry IDs -> MySQL 取正文。
3. `FULLTEXT(keyword, answer, content)` 文本检索。
4. 必要时 fallback 到 `LIKE`。
5. 去重后按 `exact > vector_score > fulltext_score > updated_at` 排序。
6. 只返回 `enabled=true AND ai_enabled=true` 的条目。

MySQL 中文全文检索效果取决于部署环境和分词配置。首版不能依赖全文检索作为唯一召回手段；精确匹配、aliases 和 `LIKE` fallback 必须可用。

### 向量索引

推荐新增 `internal/vector`，提供和 MumuBot 类似的窄接口：

```go
type Embedder interface {
    Embed(ctx context.Context, text string) ([]float64, error)
}

type Store interface {
    Upsert(ctx context.Context, entryID int64, embedding []float64, meta VectorMeta) error
    Search(ctx context.Context, embedding []float64, topK int, threshold float64) ([]SearchResult, error)
    Delete(ctx context.Context, entryIDs []int64) error
    Close() error
}
```

首选实现：

- Embedder：`github.com/cloudwego/eino-ext/components/embedding/openai`。
- Vector Store：Milvus，HNSW 索引，默认 `COSINE`。
- Collection：`jxh_knowledge_vectors`。
- Vector fields：`entry_id`、`category`、`embedding`。

Milvus 不是业务数据库；它可以删除后通过 MySQL `knowledge_entries` 全量重建。部署不想增加 Milvus 时，配置 `vector.enabled=false`，`/ai` 自动退回文本混合检索。

向量重建策略：

- `/reload` 事务提交后，找出 `vector_status=pending` 的条目。
- 对 `content` 生成 embedding。
- 成功写入向量索引后，把 `vector_status` 更新为 `ready`，并写入 `vector_content_hash=content_hash`。
- 失败时标记 `failed` 并记录日志；不回滚知识库导入。
- `enabled=false` 或 `ai_enabled=false` 的条目应从向量索引删除或在搜索后过滤掉。
- 提供维护命令或启动参数用于全量重建向量索引，但首版不需要暴露给 QQ 群用户。

返回文档格式：

```text
Document.ID = knowledge_entries.id
Document.Content = content
Document.MetaData = {
  "keyword": keyword,
  "category": category,
  "tags": tags,
  "answer": answer
}
```

如果向量召回和文本召回返回同一条知识，保留最高分和命中来源列表，方便日志排查。

## 配置

```yaml
app:
  debug: false
  log_level: "info"
  timezone: "Asia/Shanghai"

server:
  addr: ":8080"
  onebot_path: "/onebot/v11/ws"

onebot:
  access_token: ""
  api_timeout_sec: 30

wps:
  share_url: ""
  sid: ""
  sheet: "release"
  cache_file: "./data/cache/knowledge.xlsx"
  sync_on_start: true

database:
  host: "mysql"
  port: 3306
  user: "jxh"
  password: ""
  name: "jxh_bot"
  charset: "utf8mb4"
  parse_time: true
  loc: "Local"

ai:
  enabled: true
  base_url: ""
  api_key: ""
  model: ""
  timeout_sec: 30
  max_question_chars: 500
  top_k: 5
  score_threshold: 0.1

embedding:
  enabled: true
  base_url: ""
  api_key: ""
  model: ""
  dimensions: 1024

vector:
  enabled: true
  address: "milvus:19530"
  db_name: "default"
  collection_name: "jxh_knowledge_vectors"
  metric_type: "COSINE"
  top_k: 8
  score_threshold: 0.7

event_dedupe:
  retention_hours: 72
  cleanup_interval_hours: 6

cache:
  ai_retrieval_ttl_sec: 300

quote:
  base_url: "http://quote:5000"
  timeout_sec: 10

scheduler:
  timezone: "Asia/Shanghai"

debug:
  enable_test_command: true
```

环境变量覆盖：

- `JXH_ONEBOT_TOKEN`
- `JXH_WPS_SID`
- `JXH_MYSQL_PASSWORD`
- `JXH_MYSQL_DSN`
- `JXH_AI_BASE_URL`
- `JXH_AI_API_KEY`
- `JXH_EMBEDDING_BASE_URL`
- `JXH_EMBEDDING_API_KEY`

## 技术栈

| 用途 | 选型 |
| --- | --- |
| Go 版本 | Go 1.25+ |
| NapCat SDK | `github.com/zjutjh/napcat-sdk` |
| Eino | `github.com/cloudwego/eino` |
| OpenAI 兼容模型 | `github.com/cloudwego/eino-ext/components/model/openai` |
| OpenAI 兼容 embedding | `github.com/cloudwego/eino-ext/components/embedding/openai` |
| 向量索引 | Milvus，可配置关闭 |
| 日志 | `go.uber.org/zap` |
| 配置 | `gopkg.in/yaml.v3` |
| ORM | `gorm.io/gorm` |
| MySQL driver | `gorm.io/driver/mysql` |
| 存储 | MySQL |
| Excel | `github.com/xuri/excelize/v2` |
| 定时任务 | `github.com/robfig/cron/v3` |
| 缓存 | `github.com/jellydator/ttlcache/v3` |

## 迁移阶段

### Phase 1：骨架与 NapCat SDK

- 初始化 Go module。
- 建立配置、日志和 `napcat-sdk` 反向 WebSocket server。
- 实现 `internal/napcat` 适配层。
- 封装 `SendGroupMsg`、`GetMsg`、`SetGroupBan`、`SetRestart`。

### Phase 2：MySQL + GORM

- 接入 MySQL 和 GORM。
- 建立 admins、blacklist、scheduled_jobs、knowledge_entries、knowledge_import_runs 表。
- 建立 processed_events 表和基于 `processed_at` 的清理索引。
- 实现 repository。

### Phase 3：知识库同步和关键词回复

- 实现 WPS 下载和 Excel 解析。
- 实现 `/reload` 同步知识库。
- 实现内存关键词索引。
- 实现关键词和 aliases 精确回复。
- 内容变化时标记 `vector_status=pending`。

### Phase 4：`/ai` 知识库问答

- 实现 MySQL 文本 Retriever。
- 实现 embedding client 和 `internal/vector` 接口。
- 实现 Milvus vector store；配置关闭时自动降级。
- 实现混合 Retriever：精确匹配、向量、FULLTEXT、LIKE。
- 接入 Eino ChatModel。
- 实现 `/ai <问题>`。
- 添加 prompt 约束和检索为空时的固定回复。

### Phase 5：管理命令、引用图、定时任务

- 实现 `/admin` 全部旧命令。
- 实现 `/q` 和 quote client。
- 实现 cron 定时任务。

### Phase 6：部署和迁移

- Dockerfile。
- Compose：NapCat、bot、quote、mysql；启用向量检索时增加 milvus。
- JSON 旧数据迁移到 MySQL。
- WPS 旧两列表兼容导入。
- 本地运行说明。

## 测试

必须覆盖：

- `internal/napcat` 事件适配。
- `internal/napcat` API 调用封装。
- WPS 两列表兼容导入。
- 新 WPS 知识表导入。
- `knowledge_entries` upsert 和事务回滚。
- 关键词和 aliases 精确匹配。
- MySQL 文本 Retriever 排序和过滤。
- 向量索引 upsert、delete、search 的 fake store 测试。
- 混合 Retriever 在向量不可用时降级到文本检索。
- WPS 内容变化时标记 `vector_status=pending`。
- 向量写入成功后更新 `vector_content_hash`。
- `/ai` 检索为空时的固定回复。
- `/ai` 有检索结果时 prompt 组装。
- `processed_events` 72 小时保留清理。
- `internal/cache` `/reload` 成功后原子替换、失败时保留旧索引。
- 命令解析。
- 管理员权限。
- 黑名单过滤。
- `/q` quote 请求体。
- 定时任务每天/单次语义。

CI 使用 fake NapCat adapter、fake quote server、fake ChatModel、测试 MySQL，不依赖真实 QQ、NapCat、WPS 或真实模型。

## 验收

- NapCat 能通过反向 WebSocket 连接 Go 服务。
- WPS 两列表能导入为知识库。
- 新 WPS 知识表能导入为知识库。
- 关键词回复读 `knowledge_entries`。
- `/reload` 能同步知识库并刷新关键词缓存。
- `/ai <问题>` 能基于知识库回答，向量索引启用时优先参与召回。
- 向量索引不可用时，`/ai` 能降级到精确、FULLTEXT、LIKE 检索。
- `/ai` 检索不到内容时不编造。
- `/q` 可用。
- `/admin` 旧命令可用。
- 群成员加入欢迎语可用。
- 定时任务不依赖 WebSocket 收包循环。
- 管理员、黑名单、知识库、定时任务重启后不丢。
- Docker Compose 可启动 NapCat、bot、quote、mysql；启用向量检索时可启动 milvus。
- 部署文档只描述单个 Go bot 实例。
