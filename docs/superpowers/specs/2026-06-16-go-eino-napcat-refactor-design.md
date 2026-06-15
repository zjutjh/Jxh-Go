# 精小弘 Go + NapCat 重构 Spec

日期：2026-06-16

## 目标

把 `MangoGovo/qqbot-JXH` 从 Python/Sanic + Lagrange.OneBot 重构为 Go + NapCat。

本次只迁移旧项目当前代码已经实现的功能，不实现 `/ai`，不新增管理后台、长期记忆、MCP、多模态、主动聊天等能力。`SugarMGP/MumuBot` 只作为 Go 工程实现参考：OneBot 客户端、`echo` 响应匹配、消息段解析、YAML 配置、结构化日志和缓存实践。

核心边界：

- NapCat 负责 QQ 登录、扫码、会话保持、重连和 OneBot v11 协议。
- Go 服务负责旧 bot 的业务逻辑。
- Go 服务继续通过 OneBot v11 与协议端通信，默认使用反向 WebSocket。

## 保留功能

必须迁移：

- WPS 在线 Excel 回复表加载：`release` sheet，第一列关键词，第二列回复。
- 群消息精确关键词回复。
- `/reload`：重新加载 WPS 回复表，保持旧代码行为。
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

- `/ai`。
- 新命令。
- 主动聊天或智能体行为。
- 管理后台、MCP、长期记忆、向量检索、多模态、群画像。

## 架构

```text
NapCatQQ
  -> OneBot v11 Reverse WebSocket
  -> Go Bot
       -> onebot: 连接、事件解析、API 调用
       -> bot: 消息管线、黑名单、命令分发、关键词 fallback
       -> commands: admin / reload / q / test
       -> reply: WPS Excel 加载和回复规则缓存
       -> scheduler: 定时任务
       -> quote: 引用图服务客户端
       -> storage: MySQL + GORM 持久化
```

推荐目录：

```text
cmd/bot
internal/config
internal/logger
internal/onebot
internal/bot
internal/commands
internal/reply
internal/scheduler
internal/storage
internal/quote
internal/cache
```

## OneBot 设计

NapCat 配置 WebSocket Client，连接 Go 服务：

```text
ws://bot:8080/onebot/v11/ws
```

Go 侧实现反向 WebSocket endpoint。所有 OneBot API 调用必须使用 `echo` 匹配响应：

```text
Call(action, params)
  -> 生成 echo
  -> 写入 pending map
  -> 发送 action frame
  -> 收到 response frame
  -> 按 echo 找到等待方
  -> 超时清理 pending
```

首版封装的 OneBot API：

- `send_group_msg`
- `get_msg`
- `set_group_ban`
- `set_restart`

消息段至少解析：

- `text`
- `at`
- `reply`
- `image`

其他消息段可以保留为原始数据或摘要文本，不驱动新功能。

## 技术栈

| 用途 | 选型 |
| --- | --- |
| Go 版本 | Go 1.24+ |
| HTTP 路由 | `github.com/go-chi/chi/v5` |
| WebSocket | `github.com/gorilla/websocket` |
| JSON | `github.com/bytedance/sonic` |
| 日志 | `go.uber.org/zap` |
| 配置 | `gopkg.in/yaml.v3` |
| ORM | `gorm.io/gorm` |
| MySQL driver | `gorm.io/driver/mysql` |
| 存储 | MySQL |
| Excel | `github.com/xuri/excelize/v2` |
| 定时任务 | `github.com/robfig/cron/v3` |
| 缓存 | `github.com/jellydator/ttlcache/v3` |

数据库使用 MySQL，访问层使用 GORM。这样可以直接借鉴 MumuBot 的持久化技术栈，也方便后续把数据迁移到管理后台或其他服务中。首版仍保持单实例 bot，不做分布式锁或多实例调度。

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

wps_excel:
  share_url: ""
  sid: ""
  sheet: "release"
  cache_file: "./data/cache/replies.xlsx"

database:
  host: "mysql"
  port: 3306
  user: "jxh"
  password: ""
  name: "jxh_bot"
  charset: "utf8mb4"
  parse_time: true
  loc: "Local"

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

如果设置了 `JXH_MYSQL_DSN`，优先使用完整 DSN；否则由配置项拼出：

```text
jxh:<password>@tcp(mysql:3306)/jxh_bot?charset=utf8mb4&parseTime=True&loc=Local
```

## 存储

只持久化旧项目已有状态：

```sql
admins(user_id BIGINT PRIMARY KEY, created_at DATETIME NOT NULL)
blacklist(user_id BIGINT PRIMARY KEY, created_at DATETIME NOT NULL)
reply_rules(keyword VARCHAR(255) PRIMARY KEY, reply TEXT NOT NULL, updated_at DATETIME NOT NULL)
scheduled_jobs(id BIGINT AUTO_INCREMENT PRIMARY KEY, type VARCHAR(16) NOT NULL, time_hhmm VARCHAR(5) NOT NULL, group_id BIGINT NOT NULL, message TEXT NOT NULL, enabled BOOLEAN NOT NULL, last_run_at DATETIME NULL, created_at DATETIME NOT NULL)
```

可以增加内部表：

```sql
processed_events(event_key VARCHAR(128) PRIMARY KEY, processed_at DATETIME NOT NULL)
```

`processed_events` 只用于避免重连边界重复处理事件，不提供用户功能。

GORM 模型需要显式设置表名和字段类型，避免自动命名造成迁移不清晰。启动时可以使用 `AutoMigrate` 创建/补齐表结构，但删除字段、修改字段语义必须通过显式迁移脚本处理。

## 核心流程

### 群消息

```text
OneBot event
  -> parse group message
  -> 黑名单检查
  -> 命令解析
  -> 命令执行
  -> 关键词精确匹配
  -> send_group_msg
```

### WPS 回复表

```text
启动:
  -> 从 MySQL 加载 reply_rules
  -> 尝试刷新 WPS
  -> 成功则替换 MySQL 和内存缓存
  -> 失败则继续使用旧缓存

/reload:
  -> 拉取 WPS download_url
  -> 下载 xlsx
  -> 解析 release sheet
  -> 替换 reply_rules
  -> 替换内存缓存
```

### `/q`

```text
/q reply
  -> 读取 reply id
  -> get_msg
  -> 生成 quote 请求体
  -> 调用 quote /base64/
  -> 发送 image 消息段
```

### 定时任务

```text
启动:
  -> 读取 scheduled_jobs
  -> 注册 cron

添加:
  -> 解析命令
  -> 写入 MySQL
  -> 注册 cron

触发:
  -> send_group_msg
  -> 单次任务执行后删除或禁用
```

`单次` 定义为下一次到达该 `HH:mm` 时执行一次。如果当天时间已过，则次日执行。

## 部署

Compose 需要包含 MySQL：

```yaml
services:
  mysql:
    image: mysql:8.4
    restart: unless-stopped
    environment:
      MYSQL_DATABASE: jxh_bot
      MYSQL_USER: jxh
      MYSQL_PASSWORD: ${JXH_MYSQL_PASSWORD}
      MYSQL_ROOT_PASSWORD: ${JXH_MYSQL_ROOT_PASSWORD}
      TZ: Asia/Shanghai
    volumes:
      - ./data/mysql:/var/lib/mysql
    command:
      - --character-set-server=utf8mb4
      - --collation-server=utf8mb4_unicode_ci

  bot:
    build: .
    restart: unless-stopped
    depends_on:
      - mysql
      - quote
    volumes:
      - ./config/config.yaml:/app/config/config.yaml:ro
      - ./data/cache:/app/data/cache
    environment:
      JXH_ONEBOT_TOKEN: ${JXH_ONEBOT_TOKEN}
      JXH_WPS_SID: ${JXH_WPS_SID}
      JXH_MYSQL_PASSWORD: ${JXH_MYSQL_PASSWORD}
    ports:
      - "8080:8080"

  napcat:
    image: napcat/napcat:latest
    restart: unless-stopped
    depends_on:
      - bot
    volumes:
      - ./napcat:/app/.config/QQ
    ports:
      - "6099:6099"

  quote:
    image: zhullyb/qq-quote-generator
    restart: unless-stopped
    ports:
      - "5004:5000"
```

## 迁移阶段

### Phase 1：骨架与 OneBot

- 初始化 Go module。
- 建立配置、日志、HTTP server、反向 WebSocket endpoint。
- 实现 OneBot 事件解析和 `echo` API 调用。
- 测试 `get_msg` 不会误读普通事件。

### Phase 2：存储与基础消息

- 接入 MySQL 和 GORM。
- 建立 admins、blacklist、reply_rules、scheduled_jobs 表。
- 实现管理员、黑名单、回复规则、定时任务 repository。
- 实现群消息管线、黑名单过滤、管理员判断。
- 实现 `/test` 和基础发送。

### Phase 3：关键词与 `/reload`

- 实现 WPS 下载和 Excel 解析。
- 实现回复规则热更新。
- 实现关键词精确匹配。
- 实现 `/reload`。

### Phase 4：管理命令与 `/q`

- 实现 `/admin` 全部旧命令。
- 实现禁言和 restart。
- 实现 `/q` 和 quote client。

### Phase 5：定时任务

- 接入 cron。
- 启动恢复任务。
- 实现查看、添加、移除。
- 测试每天/单次语义。

### Phase 6：部署

- Dockerfile。
- Compose：NapCat、bot、quote、mysql。
- JSON 旧数据迁移到 MySQL。
- 本地运行说明。

## 测试

必须覆盖：

- OneBot `echo` 响应匹配。
- OneBot 消息段解析。
- 命令解析。
- 管理员权限。
- 黑名单过滤。
- WPS Excel 解析。
- `/q` quote 请求体。
- 定时任务每天/单次语义。
- repository 持久化。

CI 使用 fake OneBot transport、fake quote server、测试 MySQL，不依赖真实 QQ、NapCat、WPS。

## 验收

- NapCat 能通过反向 WebSocket 连接 Go 服务。
- 关键词回复可用。
- `/reload` 可用。
- `/q` 可用。
- `/admin` 旧命令可用。
- 群成员加入欢迎语可用。
- 定时任务不依赖 WebSocket 收包循环。
- 管理员、黑名单、回复规则、定时任务重启后不丢。
- Docker Compose 可启动 NapCat、bot、quote、mysql。
- 没有实现 `/ai` 或其他新增功能。
