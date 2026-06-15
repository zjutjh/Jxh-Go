# 精小弘 Go + NapCat 重构设计 Spec

日期：2026-06-16

## 结论

将 `MangoGovo/qqbot-JXH` 从 Python/Sanic + Lagrange.OneBot 重构为 Go 服务，并使用 NapCat 作为 QQ 协议端。功能范围只保持 `qqbot-JXH` 当前代码已经实现的能力；不因为参考 `SugarMGP/MumuBot` 而新增群友智能体、长期记忆、MCP、管理后台、多模态、群画像、主动发言或 RAG。

MumuBot 只作为实现参考：

- 借鉴它的 Go 工程分层。
- 借鉴它的 OneBot API `echo` 响应匹配。
- 借鉴它的消息段解析方式。
- 借鉴它的 YAML 配置、环境变量覆盖、结构化日志和缓存实践。
- 不借鉴它的产品功能边界。

如果未来确实要实现旧 README 中写过但当前代码没有实现的 `/ai`，应另开 spec。当前重构不实现 `/ai`，也不把 Eino 接入运行路径。若项目后续仍坚持保留 Go + Eino 技术方向，可以先预留 `internal/ai` 目录和接口，但不注册命令、不暴露用户功能、不进入本 spec 的完成定义。

## 依据

已核对的上下文：

- 上一轮分析线程：`codex://threads/019ecbc8-514a-7991-8b24-0acd16e2c2d7`
- 旧项目本地代码：`/Users/phlin/Documents/New project/qqbot-JXH`
- 旧项目当前 commit：`31e2af4`
- 旧项目远端：`https://github.com/MangoGovo/qqbot-JXH.git`
- 参考项目：`https://github.com/SugarMGP/MumuBot.git`
- NapCat 文档：支持 OneBot 11、HTTP、WebSocket、反向 WebSocket、WebUI 配置。

旧项目实际实现集中在 `ws/server.py`。README 中的 `/ai <message>` 没有对应实现，所以本 spec 不把它列为迁移功能。

## 功能范围

只迁移 `qqbot-JXH` 当前代码实际具备的行为。

### 必须保留

- 从 WPS 在线 Excel 下载回复表。
- 读取 `release` sheet，第一列为关键词，第二列为回复。
- 群消息精确关键词自动回复。
- `/reload`：重新加载 WPS 回复表，保持旧代码行为，不新增管理员权限限制。
- `/q`：用户回复一条消息后发送 `/q`，获取被回复消息并调用 `qq-quote-generator` 生成引用图。
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
- `/test`：保留旧代码中的调试响应行为，后续是否删除另开需求。
- 黑名单用户消息被忽略。
- 机器人关闭 flag 的内部结构可以保留，但旧项目没有暴露完整开关命令，不新增开关命令。

### 不在本次实现

- `/ai <message>`。
- 自主聊天、主动插话、ReAct Agent。
- 长期记忆、向量检索、群文化学习。
- MCP 工具扩展。
- 管理后台。
- 群级启用/禁用配置。
- 多模态图片/视频理解。
- 表情包自动收集。
- 群友画像。
- HTTP 管理 API。
- 新的风控、审核、权限模型。

## 重构目标

- QQ 登录、扫码、会话、协议兼容交给 NapCat。
- Go 服务只做 `qqbot-JXH` 的既有业务逻辑。
- 保留 OneBot v11 边界，未来可以替换协议端。
- 修复旧实现中连接、调度和状态耦合的问题。
- OneBot API 调用必须用 `echo` 匹配响应，不能再直接读取下一帧。
- 定时任务独立运行，不依赖 WebSocket 收包循环。
- 管理员、黑名单、定时任务和回复规则持久化，容器重建不丢数据。
- 保持部署简单：NapCat + Go bot + quote 服务。

## 非目标

- 不在 Go 中处理 QQ 登录。
- 不写 NapCat 私有插件。
- 不改变用户可见命令集。
- 不新增 AI 功能。
- 不引入 MySQL、Milvus、MCP 或前端后台作为首版依赖。
- 不重写 `qq-quote-generator`。

## 总体架构

```text
NapCatQQ
  -> OneBot v11 WebSocket
  -> Go Bot Service
       -> onebot 适配层
       -> event 事件分发
       -> command 命令路由
       -> reply 关键词回复
       -> admin 管理员/黑名单
       -> scheduler 定时任务
       -> quote 引用图客户端
       -> storage 持久化
```

推荐目录：

```text
cmd/bot
  启动配置、日志、数据库、OneBot 服务、调度器和命令注册

internal/config
  YAML 配置、环境变量覆盖、配置校验

internal/logger
  zap 日志初始化

internal/onebot
  OneBot 事件结构、消息段、WebSocket transport、echo API 客户端、响应匹配

internal/bot
  群消息处理管线、黑名单过滤、命令入口、关键词 fallback

internal/commands
  admin、reload、q、test 等旧命令实现

internal/reply
  WPS 下载、Excel 解析、回复规则缓存、热重载

internal/scheduler
  定时任务持久化、cron 注册、单次任务清理

internal/storage
  SQLite 初始化、repository、迁移

internal/quote
  qq-quote-generator HTTP 客户端

internal/cache
  TTL 缓存封装，可用于消息详情和群成员信息缓存
```

`internal/ai` 不进入首版。如果为了后续 Go + Eino 方向预留目录，只能放空接口和文档，不注册 `/ai`。

## OneBot 连接模式

默认使用 NapCat 反向 WebSocket，最接近旧项目的 Lagrange 反向 WebSocket 部署：

```text
NapCat WebUI 新建 WebSocket Client
URL: ws://bot:8080/onebot/v11/ws
Token: 与 Go 配置中的 onebot.access_token 一致
```

为了借鉴 MumuBot 的 OneBot 客户端设计，`internal/onebot` 应抽象 transport：

```go
type Transport interface {
    Start(ctx context.Context) error
    Send(ctx context.Context, payload []byte) error
    Events() <-chan []byte
    Close() error
}
```

首版只必须实现：

- `ReverseWSTransport`：NapCat 主动连接 Go。

可保留接口但不实现用户功能：

- `ForwardWSTransport`：未来如果要改成 MumuBot 风格的 Go 主动连接 NapCat，再实现。

## OneBot API 设计

必须借鉴 MumuBot 的 `echo` 匹配模式，修复旧项目 `get_msg()` 直接等待下一帧的问题。

```text
ActionClient.Call(ctx, action, params)
  -> 生成 echo
  -> pending[echo] = response channel
  -> WebSocket 写入 action frame
  -> reader loop 收到 echo response
  -> 按 echo 投递到 pending
  -> 超时后清理 pending
```

首版需要封装的 OneBot API 只包括旧功能用到的接口：

- `send_group_msg`
- `get_msg`
- `set_group_ban`
- `set_restart`

可选但不改变功能：

- `get_login_info`：用于识别 self id。
- `get_group_member_info`：用于内部调试或更好的日志。

消息段解析覆盖旧功能所需类型：

- `text`
- `at`
- `reply`
- `image`

兼容解析但不新增功能：

- `face`
- `mface`
- `record`
- `video`
- `file`
- `json`
- `forward`

这些类型可以规范化为内部 `GroupMessage` 字段或摘要文本，但业务层只消费旧功能需要的 `text`、`at`、`reply`、`image`。

## 技术栈

参考 MumuBot 的工程技术，但保持首版轻量。

| 领域 | 推荐 | 说明 |
| --- | --- | --- |
| Go 版本 | Go 1.24+ | 不依赖 MumuBot 的 Go 1.26 环境 |
| HTTP 路由 | `github.com/go-chi/chi/v5` | 提供 OneBot endpoint 和基础健康检查 |
| WebSocket | `github.com/gorilla/websocket` | 与 MumuBot 保持一致，生态成熟 |
| JSON | `github.com/bytedance/sonic` | 与 MumuBot 保持一致 |
| 日志 | `go.uber.org/zap` | 结构化日志 |
| 配置 | `gopkg.in/yaml.v3` | YAML + 环境变量覆盖 |
| 数据库 | SQLite | 单实例 bot 部署简单 |
| ORM | 可选 GORM | 若使用 GORM，后续切 MySQL 更容易；也可以直接 `database/sql` |
| 缓存 | `github.com/jellydator/ttlcache/v3` | 仅用于内部缓存，不形成新功能 |
| Excel | `github.com/xuri/excelize/v2` | 解析 WPS 下载的 xlsx |
| 定时任务 | `github.com/robfig/cron/v3` | 独立调度，支持时区 |
| 测试 | Go 标准 `testing` + fake client | 不依赖真实 NapCat |

不引入：

- Eino 运行链路。
- MySQL。
- Milvus。
- MCP。
- 前端构建工具。
- templ 管理后台。

## 配置设计

配置只覆盖旧功能所需项。

```yaml
app:
  debug: false
  log_level: "info"
  timezone: "Asia/Shanghai"

server:
  addr: ":8080"
  onebot_path: "/onebot/v11/ws"

onebot:
  mode: "reverse"
  access_token: ""
  api_timeout_sec: 30

wps_excel:
  share_url: ""
  sid: ""
  sheet: "release"
  cache_file: "./data/cache/replies.xlsx"

database:
  driver: "sqlite"
  dsn: "file:./data/jxh.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"

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
- `JXH_DB_DSN`

## 存储设计

首版只持久化旧项目已有状态。

```sql
admins(
  user_id INTEGER PRIMARY KEY,
  created_at TEXT NOT NULL
)

blacklist(
  user_id INTEGER PRIMARY KEY,
  created_at TEXT NOT NULL
)

reply_rules(
  keyword TEXT PRIMARY KEY,
  reply TEXT NOT NULL,
  updated_at TEXT NOT NULL
)

scheduled_jobs(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  type TEXT NOT NULL,
  time_hhmm TEXT NOT NULL,
  group_id INTEGER NOT NULL,
  message TEXT NOT NULL,
  enabled INTEGER NOT NULL,
  last_run_at TEXT,
  created_at TEXT NOT NULL
)
```

可选内部表：

```sql
processed_events(
  event_key TEXT PRIMARY KEY,
  processed_at TEXT NOT NULL
)
```

`processed_events` 只用于避免重连边界重复处理事件，不提供用户可见功能。不要加入消息日志、长期记忆或群画像表。

## 业务流程

### 群消息处理

```text
OneBot 原始事件
  -> onebot.ParseGroupMessage
  -> 黑名单检查
  -> 命令解析
  -> 命令执行
  -> 若不是命令，执行关键词精确匹配
  -> send_group_msg
```

保持旧行为：

- 命令优先于关键词。
- 黑名单用户直接忽略。
- `is_me` 拥有管理员命令权限。
- 存储的管理员拥有管理员命令权限。
- `/reload` 不新增管理员限制。
- `/test` 按旧实现保留调试响应。

### WPS 回复表加载

```text
启动
  -> 从 SQLite 加载 reply_rules 到内存
  -> 尝试刷新 WPS
  -> 成功后替换 SQLite 和内存缓存
  -> 失败则继续使用旧缓存

/reload
  -> 拉取 WPS download_url
  -> 下载 xlsx
  -> excelize 解析 release sheet
  -> 校验空 key、重复 key
  -> 替换 reply_rules
  -> 原子替换内存缓存
```

旧项目启动时依赖 WPS 成功加载。Go 版允许 WPS 失败时使用本地缓存，这是稳定性修复，不改变用户功能。

### 引用图 `/q`

```text
/q reply 消息
  -> 从消息段读取 reply id
  -> OneBot get_msg
  -> 生成 quote 请求体
  -> 如被引用消息本身引用了另一条消息，再拉一层
  -> 调用 quote 服务 /base64/
  -> 发送 image 消息段
```

错误反馈保持简单：

- 未回复消息：提示需要回复一条消息。
- `get_msg` 失败：提示获取原消息失败。
- quote 服务失败：提示引用图生成失败。

### 管理命令

管理命令保持旧语义：

- 非管理员执行 `/admin ...` 返回无权限提示。
- 添加管理员和黑名单依赖 `@user`。
- 管理员不能被加入黑名单。
- `ban <duration> @user` 调用 `set_group_ban`。
- `restart` 调用 `set_restart`。
- 查看列表时没有内容则返回旧项目风格的空提示。

### 定时任务

```text
启动
  -> 从 SQLite 读取 enabled jobs
  -> 注册到 cron

/admin 定时任务 添加 ...
  -> 解析 <每天|单次> <时间> <群聊ID> <消息内容>
  -> 持久化
  -> 注册 cron

任务触发
  -> send_group_msg
  -> 单次任务成功后删除或禁用
```

`单次` 语义保持为下一次到达该 `HH:mm` 时执行一次。如果添加时当天时间已过，则次日执行。旧项目这部分语义不够明确，Go 版需要写清楚并测试。

## 可迭代接口

接口用于降低耦合，不代表新增功能。

```go
type ActionClient interface {
    SendGroupMessage(ctx context.Context, groupID int64, message Message) (int64, error)
    GetMessage(ctx context.Context, messageID int64) (*MessageDetail, error)
    SetGroupBan(ctx context.Context, groupID, userID int64, durationSec int) error
    Restart(ctx context.Context, delayMS int) error
}

type Command interface {
    Name() string
    Match(msg *GroupMessage) bool
    Execute(ctx context.Context, env CommandEnv, msg *GroupMessage) (*CommandResult, error)
}

type ReplyRuleStore interface {
    List(ctx context.Context) ([]ReplyRule, error)
    ReplaceAll(ctx context.Context, rules []ReplyRule) error
}

type AdminStore interface {
    ListAdmins(ctx context.Context) ([]int64, error)
    AddAdmin(ctx context.Context, userID int64) error
    RemoveAdmin(ctx context.Context, userID int64) error
    ClearAdmins(ctx context.Context) error
}

type BlacklistStore interface {
    ListBlacklist(ctx context.Context) ([]int64, error)
    AddBlacklist(ctx context.Context, userID int64) error
    RemoveBlacklist(ctx context.Context, userID int64) error
    ClearBlacklist(ctx context.Context) error
}
```

约束：

- `commands` 不能直接依赖 WebSocket 连接，只依赖 `ActionClient` 和 store。
- `onebot` 不依赖业务模块。
- `storage` 不依赖 OneBot。
- 所有外部调用必须接收 `context.Context` 和超时。

## Docker Compose

```yaml
services:
  napcat:
    image: napcat/napcat:latest
    restart: unless-stopped
    volumes:
      - ./napcat:/app/.config/QQ
    ports:
      - "6099:6099"
    depends_on:
      - bot

  bot:
    build: .
    restart: unless-stopped
    volumes:
      - ./config/config.yaml:/app/config/config.yaml:ro
      - ./data:/app/data
    environment:
      JXH_ONEBOT_TOKEN: "${JXH_ONEBOT_TOKEN}"
      JXH_WPS_SID: "${JXH_WPS_SID}"
    ports:
      - "8080:8080"
    depends_on:
      - quote

  quote:
    image: zhullyb/qq-quote-generator
    restart: unless-stopped
    ports:
      - "5004:5000"
```

## 迁移阶段

### Phase 1：项目骨架与 OneBot 适配

- 初始化 Go module。
- 建立 `cmd/bot`、`internal/config`、`internal/logger`、`internal/onebot`。
- 接入 `chi`、`gorilla/websocket`、`zap`、`sonic`、`yaml.v3`。
- 实现反向 WebSocket endpoint。
- 实现 OneBot frame 分类：event、meta、api response。
- 实现 echo pending map 和 action timeout。
- 写 fake transport 测试。

验收：

- NapCat 能连接 Go 服务。
- Go 服务能识别生命周期事件和群消息事件。
- 测试能证明 `get_msg` 不会误读普通事件。

### Phase 2：存储和基础状态

- 接入 SQLite。
- 建立 admins、blacklist、reply_rules、scheduled_jobs。
- 实现配置文件和环境变量覆盖。
- 实现管理员判断和黑名单过滤。
- 实现 `bot` 在线检测和 `/test` 调试响应。

验收：

- 重启后管理员和黑名单不丢。
- 群消息能进入统一事件管线。

### Phase 3：关键词回复和 `/reload`

- 实现 WPS download_url 获取。
- 用 excelize 解析 `release` sheet。
- 实现 reply_rules 替换。
- 实现 `/reload`。
- 实现关键词精确匹配。

验收：

- WPS 成功时可刷新回复。
- WPS 失败时保留旧回复。
- 重复关键词有日志告警。

### Phase 4：管理命令和引用图

- 实现 `/admin` 命令解析和执行。
- 实现管理员增删查清空。
- 实现黑名单增删查清空。
- 实现 `ban` 和 `restart`。
- 实现 `/q` 和 quote client。
- 对命令 parser 做表驱动测试。

验收：

- 旧项目主要管理命令行为等价。
- `/q` 能生成图片，失败时有明确反馈。

### Phase 5：独立定时任务

- 接入 `robfig/cron/v3`。
- 启动时恢复任务。
- 添加、查看、移除定时任务。
- 明确定义并测试 `单次` 的下一次执行语义。

验收：

- 没有群消息输入时，定时任务仍按时执行。
- 单次任务执行后不会重复发送。

### Phase 6：部署和迁移

- Dockerfile 多阶段构建。
- Docker Compose 集成 NapCat、bot、quote。
- JSON 数据迁移到 SQLite。
- 统一日志字段。
- 编写本地运行说明。

验收：

- `docker compose up` 后可完成扫码登录、连接、关键词回复、管理命令、引用图和定时任务。
- 数据挂载在 `./data`，容器重建不丢状态。

## 测试策略

单元测试：

- OneBot echo 匹配。
- OneBot 消息段解析。
- 命令解析。
- 管理员权限判断。
- 黑名单过滤。
- WPS Excel 解析。
- scheduler 单次/每天语义。

集成测试：

- fake OneBot transport 输入群消息事件，断言输出 `send_group_msg`。
- fake quote HTTP server 验证 `/q` 请求体。
- in-memory SQLite 验证 repository。

手工 smoke test：

- NapCat WebUI 配置反向 WS。
- QQ 扫码登录。
- 发送 `bot`。
- 发送关键词。
- 执行 `/reload`。
- 管理员添加和黑名单。
- `/q`。
- 添加一个单次定时任务。

CI 不依赖真实 QQ、NapCat、WPS 或外部模型。

## 风险与处理

- NapCat 配置差异：path 和 token 配置化。
- WebSocket 断线：transport 层统一连接状态，业务层只看到 `ActionClient` 错误。
- API 响应串线：强制 echo 匹配。
- WPS 凭证失效：本地缓存优先，reload 失败不清空数据。
- 旧定时任务语义不清：在 Go 版明确 `单次` 是下一次到达 `HH:mm` 执行一次。
- MumuBot 技术栈偏重：只吸收工程实现，不吸收产品功能。
- SQLite 单实例限制：当前 bot 是单实例部署，足够支撑迁移目标。

## 完成定义

- NapCat 能稳定连接 Go 服务。
- 旧项目已实现的命令和事件行为完成迁移。
- `/ai`、MCP、长期记忆、管理后台等新功能没有进入实现范围。
- OneBot API 统一 echo 匹配。
- 管理员、黑名单、回复规则、定时任务可持久化。
- 定时任务不依赖 WebSocket 收包循环。
- Docker Compose 可启动 NapCat、bot、quote。
- 关键模块有单元测试或 fake integration test。
