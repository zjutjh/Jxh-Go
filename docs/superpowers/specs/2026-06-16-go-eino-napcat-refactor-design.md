# 精小弘 Go + NapCat + Eino 重构 Spec

日期：2026-06-16

## 1. 摘要

把 `MangoGovo/qqbot-JXH` 从 Python/Sanic + Lagrange.OneBot 重构为单实例 Go 服务。协议端改用 NapCat，Go 侧通过 `github.com/zjutjh/napcat-sdk` 接入 OneBot v11 反向 WebSocket。

本版保留现有 bot 功能，并新增 `/ai`。`/ai` 只基于同一套知识库回答问题，不做主动聊天、不做 ReAct Agent，也不新增管理后台、MCP、多模态、群画像或长期记忆。

核心方向：

- WPS 是人工维护源。
- MySQL `knowledge_entries` 是运行时唯一知识主表。
- 关键词回复和 `/ai` 共用 `knowledge_entries`。
- Milvus 只作为可选、可重建的向量索引。
- 单实例部署，不设计分布式锁或跨实例一致性。

## 2. 依据

- Eino Retriever 适合按 query 从数据源取回相关文档，可用于知识库问答/RAG。
- Eino ChatModel 负责向大模型发送消息并获取回答。
- `github.com/zjutjh/napcat-sdk` 提供 NapCat/OneBot 11 的 HTTP、正向 WebSocket、反向 WebSocket server、事件解析、消息段构造和强类型 action。
- `SugarMGP/MumuBot` 的 RAG 可借鉴点是：正文和业务状态保存在 MySQL，向量库只保存业务 ID、范围字段和 embedding；检索时先向量召回 ID，再回 MySQL 取完整记录，并保留关键词检索作为补召回。

## 3. 范围

### 3.1 必须保留

- WPS 在线表格加载。
- 群消息关键词精确回复。
- `/reload`：同步 WPS 知识表到 MySQL，并刷新运行时缓存。
- `/ai <问题>`：基于知识库回答问题。
- `/q`：回复一条消息后生成引用图，继续调用 `qq-quote-generator`。
- `/test` 调试响应。
- 群成员增加事件欢迎语。
- 黑名单用户消息忽略。
- 管理员、黑名单和定时任务持久化。

管理命令保持现有能力：

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

### 3.2 明确不做

- 主动聊天。
- ReAct 自主智能体。
- 管理后台。
- MCP。
- 多模态。
- 群画像。
- 主动学习和长期记忆。
- 多实例部署。

## 4. 关键设计决策

### 4.1 WPS 和 AI 知识库合并

WPS 回复表和 AI 知识库必须合并成同一套数据，不维护两套表。

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

- WPS 适合非技术人员维护。
- MySQL 适合运行时查询、事务、回滚和缓存。
- 关键词回复和 `/ai` 读同一张知识表，避免内容漂移。
- `/reload` 从“加载回复表”升级为“同步知识库”，用户操作仍然简单。

### 4.2 向量索引不是主数据源

Milvus 只是派生索引。删除 Milvus collection 后，系统必须能用 MySQL `knowledge_entries` 全量重建。

向量索引不可用时：

- 关键词回复不受影响。
- `/ai` 降级到精确匹配、FULLTEXT、LIKE 检索。
- 不因为 embedding 或 Milvus 失败回滚 WPS 导入。

### 4.3 缓存只保存可重建派生数据

`internal/cache` 不保存业务事实。管理员、黑名单、定时任务、知识库正文都以 MySQL 为准。

### 4.4 事件去重只做短期保护

`processed_events` 只用于单实例内处理重连边界的重复事件。它不是用户功能，也不是跨实例去重机制，因此必须有保留时间和清理任务。

## 5. 系统架构

```text
NapCatQQ
  -> OneBot v11 Reverse WebSocket
  -> Go Bot
       -> napcat: SDK client、事件流、API 调用
       -> bot: 消息管线、黑名单、命令分发
       -> commands: admin / reload / ai / q / test
       -> knowledge: WPS 同步、知识库、关键词匹配、文本检索
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

模块职责：

| 模块 | 职责 |
| --- | --- |
| `internal/napcat` | 适配 `napcat-sdk`，把 SDK 类型转换为内部事件和业务接口 |
| `internal/bot` | 消息管线、黑名单检查、命令分发、非命令关键词匹配 |
| `internal/commands` | `/admin`、`/reload`、`/ai`、`/q`、`/test` |
| `internal/knowledge` | WPS 下载解析、知识 upsert、关键词索引构建、文本检索 |
| `internal/ai` | Eino Retriever、prompt、ChatModel、回答约束 |
| `internal/vector` | embedding client、Milvus store、向量重建 |
| `internal/storage` | GORM models、repositories、事务边界 |
| `internal/scheduler` | cron 定时任务加载、执行和持久化 |
| `internal/quote` | `qq-quote-generator` HTTP client |
| `internal/cache` | 只保存可重建的运行时派生缓存 |

## 6. NapCat 接入

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

业务模块不直接依赖 SDK 细节。`internal/napcat` 需要封装：

- `SendGroupMsg`
- `GetMsg`
- `SetGroupBan`
- `SetRestart`

消息段构造优先使用 SDK 的 `message` 包：

- `message.Text(...)`
- `message.At(...)`
- `message.Reply(...)`
- `message.Image(...)`

## 7. 数据模型

### 7.1 WPS 知识表

WPS 继续以现有两列为主。导入器负责把两列回复表解析成知识库记录，并自动补充 RAG 所需的 `content`、分类、菜单路径和检索别名。

基础列：

| 列 | 字段 | 说明 |
| --- | --- | --- |
| A | `keyword` | 关键词。旧表第一列映射到这里 |
| B | `answer` | 标准回答。旧表第二列映射到这里 |

第三列规则：

- C 列不导入数据库。
- C 列不参与关键词回复。
- C 列不参与 `/ai` 检索、embedding 或 prompt。
- 如果 `dev` sheet 中存在 C 列，只视为人工维护备注或修订意见。

可选新增列：

| 列 | 字段 | 说明 |
| --- | --- | --- |
| D | `aliases` | 可选同义问法，多个用 `;` 分隔 |
| E | `category` | 可选分类，如 交通、选课、宿舍、报到 |
| F | `usage` | 可选用途：`both` / `exact` / `ai`；空值由导入器判断 |
| G | `status` | 可选状态：`enabled` / `disabled` / `draft`；空值按 `enabled` |
| H | `source_id` | 可选稳定 ID；为空时用规范化后的 `keyword` |

兼容规则：

- 旧两列表仍可导入。
- `answer` 是精确关键词命中后直接发给群的内容。
- `content` 不要求人工维护，由导入器根据 `keyword`、菜单路径、`aliases`、`category` 和 `answer` 自动生成。
- `aliases` 参与 `/ai` 检索，也可参与关键词匹配。
- `status=disabled` 或 `draft` 的条目不参与关键词回复和 `/ai`。
- `usage=exact` 的条目只做关键词回复，不进入 `/ai`。
- `usage=ai` 的条目只进入 `/ai`，不做关键词精确回复。
- `usage=both` 的条目同时参与关键词回复和 `/ai`。
- 修改 `keyword` 时如果想保留同一条记录，应填写稳定的 `source_id`。

### 7.2 导入器自动增强

导入器必须理解现有 `data.xlsx` 这类结构，不能只把第二列原样丢进向量库。

行类型：

| 类型 | 判断 | 处理 |
| --- | --- | --- |
| `menu_node` | `keyword` 是 `%数字`，且 `answer` 中包含 `%子编号 标题` | 保留精确回复；作为菜单树节点和上下文来源 |
| `knowledge` | 自然关键词，或没有子节点的 `%数字` 叶子节点 | 进入关键词回复和 `/ai` |
| `chitchat` | 短关键词 + 短趣味回复，如 晚安、饿了、美女 | 默认只做关键词回复，不进 `/ai` |
| `maintenance_note` | `dev` sheet 第三列 | 忽略，不入库 |

菜单树解析：

- 从 `answer` 中提取 `%数字 标题`，建立父子关系。
- 对叶子节点生成标题路径，例如 `朝晖校区交通 / 火车站 / 杭州东站`。
- 叶子节点的 `content` 使用 `标题路径 + aliases + answer`。
- 菜单中间节点默认可做关键词回复，但不优先进入 `/ai`；如果内容本身有事实说明，可按 `knowledge` 处理。

自动生成字段：

| 字段 | 生成规则 |
| --- | --- |
| `source_key` | 优先 `source_id`，否则规范化 `keyword` |
| `content` | `标题路径 + keyword + aliases + answer`；自然关键词没有路径时用 `keyword + aliases + answer` |
| `category` | 优先 WPS `category`；否则从顶层菜单或关键词规则推断 |
| `aliases` | 合并 WPS aliases、菜单标题、标题路径中的末级标题、常见同义问法 |
| `exact_reply` | `status=enabled` 且 `usage` 为 `both` 或 `exact` |
| `ai_enabled` | `status=enabled` 且 `usage` 为 `both` 或 `ai`，但 `chitchat` 默认 false |

### 7.3 MySQL 表

`knowledge_entries` 是关键词回复和 `/ai` 共用的唯一知识源。

```sql
knowledge_entries(
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  source_key VARCHAR(255) NOT NULL UNIQUE,
  keyword VARCHAR(255) NOT NULL,
  entry_type VARCHAR(32) NOT NULL,
  path VARCHAR(512) NULL,
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
  FULLTEXT KEY ft_knowledge_keyword_content (keyword, path, answer, content)
)
```

解析字段：

- `entry_type`：`menu_node`、`knowledge` 或 `chitchat`。
- `path`：菜单树标题路径；自然关键词可为空。

向量字段只描述派生索引状态：

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

其他业务状态：

```sql
admins(user_id BIGINT PRIMARY KEY, created_at DATETIME NOT NULL)
blacklist(user_id BIGINT PRIMARY KEY, created_at DATETIME NOT NULL)
scheduled_jobs(id BIGINT AUTO_INCREMENT PRIMARY KEY, type VARCHAR(16) NOT NULL, time_hhmm VARCHAR(5) NOT NULL, group_id BIGINT NOT NULL, message TEXT NOT NULL, enabled BOOLEAN NOT NULL, last_run_at DATETIME NULL, created_at DATETIME NOT NULL)
```

事件去重：

```sql
processed_events(
  event_key VARCHAR(128) PRIMARY KEY,
  processed_at DATETIME NOT NULL,
  KEY idx_processed_events_processed_at (processed_at)
)
```

## 8. 知识同步

启动流程：

```text
启动
  -> 从 MySQL 加载 knowledge_entries 到内存关键词索引
  -> 启动 processed_events 清理任务
  -> 可选尝试同步 WPS
  -> WPS 失败时继续使用 MySQL 旧数据
```

`/reload` 流程：

```text
/reload
  -> 下载 WPS xlsx
  -> 解析 release sheet 基础两列
  -> 忽略 dev sheet 第三列维护备注
  -> 识别菜单树、知识条目和闲聊条目
  -> 自动生成 content、aliases、category、usage
  -> 校验 keyword / answer
  -> 计算 content_hash
  -> 事务 upsert knowledge_entries
  -> 内容变化条目标记 vector_status=pending
  -> 当前 WPS 不存在的旧条目标记 enabled=false
  -> 记录 knowledge_import_runs
  -> 原子刷新内存关键词索引
  -> 异步或同步重建 pending 向量索引
```

导入策略：

- `source_key` 优先使用 WPS `source_id`。
- `source_id` 为空时，`source_key` 使用规范化后的 `keyword`。
- 空 `keyword` 或空 `answer` 跳过。
- 同一 `source_key` 且 `answer` 相同的重复行只导入一次。
- 同一 `source_key` 但 `answer` 不同的重复行记录冲突；默认保留第一条，冲突行不进入 `/ai`。
- 短趣味回复默认标记为 `usage=exact`，避免污染 RAG。
- `%数字` 菜单叶子节点默认可进入 `/ai`，但中间菜单节点只有在包含事实说明时进入 `/ai`。
- 第三列内容不入库、不参与检索，只在导入日志中统计有多少维护备注被忽略。
- WPS 下载失败不清空已有知识。
- 成功导入后才替换内存缓存，避免半更新状态。
- 每次成功导入记录 `last_import_run_id`。

## 9. 运行时行为

### 9.1 消息管线

```text
group message
  -> 事件去重
  -> 黑名单检查
  -> 命令解析
  -> 命令处理
  -> 非命令关键词匹配
```

### 9.2 关键词回复

普通群消息走确定性匹配，不调用 LLM。

```text
非命令消息
  -> 查 keyword index
  -> exact_reply=true && enabled=true
  -> send_group_msg(answer)
```

匹配规则：

- 先精确匹配 `keyword`。
- 再精确匹配 `aliases`。
- 不做模糊回复，避免普通聊天误触发。

### 9.3 `/ai`

`/ai <问题>` 是受控 RAG 问答。

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

### 9.4 `/q`

`/q` 回复一条消息时，Go bot 调用 `GetMsg` 获取原消息，再请求 `qq-quote-generator` 生成引用图，最后通过 NapCat 发送图片消息。

### 9.5 定时任务

定时任务从 MySQL 加载，由 `internal/scheduler` 独立于 WebSocket 收包循环运行。

每天任务按配置时区重复执行。单次任务执行成功后禁用或删除，具体行为在实现时保持原项目语义。

## 10. RAG 和向量检索

### 10.1 Eino 组件

使用 Eino 的核心组件，不使用 ReAct Agent。

- `Retriever`：自定义混合 retriever，实现 `Retrieve(ctx, query string, opts ...Option) ([]*schema.Document, error)`。
- `ChatModel`：使用 Eino ChatModel，默认接 OpenAI 兼容模型。
- `ChatTemplate` 或本地 prompt builder：把检索结果组装成上下文。
- `Chain`：Retriever -> Prompt -> ChatModel 的简单链路。

### 10.2 混合召回

Retriever 内部组合四类召回：

1. `keyword` / `aliases` 精确命中。
2. 向量召回：query -> embedding -> vector search -> entry IDs -> MySQL 取正文。
3. `FULLTEXT(keyword, answer, content)` 文本检索。
4. `LIKE` fallback。

排序规则：

```text
exact > vector_score > fulltext_score > updated_at
```

只返回 `enabled=true AND ai_enabled=true` 的条目。向量召回命中后必须回 MySQL 读取最新记录，并再次过滤状态。

MySQL 中文全文检索效果取决于部署环境和分词配置。因此精确匹配、aliases 和 `LIKE` fallback 必须可用，不能只依赖 FULLTEXT。

RAG 检索使用导入器生成的 `content`。对于菜单树叶子节点，`content` 必须包含标题路径，例如 `朝晖校区交通 / 火车站 / 杭州东站`，否则用户用自然语言提问时很难召回 `%0011` 这类编号知识。

`answer` 仍保留原始回复文本。关键词命中直接发送 `answer`；`/ai` 检索和 embedding 使用 `content`，并在 prompt metadata 中携带 `answer` 作为短答参考。

### 10.3 向量索引

`internal/vector` 提供窄接口：

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

向量重建策略：

- `/reload` 事务提交后，找出 `vector_status=pending` 的条目。
- 对 `content` 生成 embedding。
- 成功写入向量索引后，把 `vector_status` 更新为 `ready`，并写入 `vector_content_hash=content_hash`。
- 失败时标记 `failed` 并记录日志；不回滚知识库导入。
- `enabled=false` 或 `ai_enabled=false` 的条目应从向量索引删除，或在搜索后过滤掉。
- 提供维护命令或启动参数用于全量重建向量索引，但首版不暴露给 QQ 群用户。

返回给 Eino 的文档格式：

```text
Document.ID = knowledge_entries.id
Document.Content = content
Document.MetaData = {
  "keyword": keyword,
  "category": category,
  "tags": tags,
  "answer": answer,
  "path": path,
  "sources": ["exact" | "vector" | "fulltext" | "like"]
}
```

如果向量召回和文本召回返回同一条知识，保留最高分和命中来源列表，方便日志排查。

## 11. 缓存和事件清理

### 11.1 `internal/cache`

`internal/cache` 只管理可重建的运行时派生数据，不保存业务事实。

允许缓存：

- 关键词精确匹配索引：`keyword/alias -> knowledge_entry_id`。
- 知识条目只读快照：用于关键词回复直接取 `answer`。
- 短 TTL 的事件去重内存集合：减少同一进程内重复查库。
- 短 TTL 的 `/ai` 检索结果缓存：key 为规范化 query 和知识库版本。
- quote、NapCat API 这类外部调用的短 TTL 结果缓存：只有明确收益时才加。

禁止缓存为唯一状态：

- 管理员。
- 黑名单。
- 定时任务。
- WPS 原始数据。
- AI 生成回答的长期结果。
- 向量正文或不可从 MySQL 重建的索引。

缓存刷新规则：

- 启动时从 MySQL 构建。
- `/reload` 成功提交事务后原子替换。
- `/reload` 失败时继续使用旧缓存。
- 缓存 miss 必须能回退到 MySQL 查询。
- `internal/cache` 不直接访问 WPS、NapCat 或 Eino。

### 11.2 `processed_events`

`processed_events` 只用于单实例内避免重连边界重复处理事件。

清理策略：

- 保留时间默认 72 小时，可配置。
- 每 6 小时清理一次 `processed_at < now - retention` 的记录。
- 启动后先执行一次清理，再启动定时清理。
- `processed_at` 必须有普通索引，避免长期运行后删除扫描全表。
- 写入失败只记录 warn，不阻断消息处理。
- 单实例下不需要分布式锁，也不需要无限期保留事件 key。

## 12. 配置

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

## 13. 技术栈

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

## 14. 迁移阶段

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

### Phase 3：知识同步和关键词回复

- 实现 WPS 下载和 Excel 解析。
- 实现 release 两列解析，忽略 dev 第三列维护备注。
- 实现 `%数字` 菜单树解析、标题路径生成和闲聊条目识别。
- 实现 `content`、`aliases`、`category`、`usage` 自动生成。
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

## 15. 测试要求

必须覆盖：

- `internal/napcat` 事件适配。
- `internal/napcat` API 调用封装。
- WPS 两列表兼容导入。
- WPS 可选列覆盖规则导入。
- dev sheet 第三列维护备注不入库。
- `%数字` 菜单树解析和标题路径生成。
- 短趣味回复自动标记为 `usage=exact`。
- 重复 key 同 answer 去重、不同 answer 记录冲突。
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

CI 使用 fake NapCat adapter、fake quote server、fake ChatModel、fake vector store、测试 MySQL，不依赖真实 QQ、NapCat、WPS、Milvus 或真实模型。

## 16. 验收标准

- NapCat 能通过反向 WebSocket 连接 Go 服务。
- WPS 两列表能导入为知识库。
- WPS 第三列维护备注不会进入数据库、向量索引或 `/ai` prompt。
- `%数字` 菜单树能解析为带标题路径的知识条目。
- 短趣味回复不会污染 `/ai` RAG。
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
- `processed_events` 长期运行不会无限堆积。
- Docker Compose 可启动 NapCat、bot、quote、mysql；启用向量检索时可启动 milvus。
- 部署文档只描述单个 Go bot 实例。
