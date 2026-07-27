# AGENTS.md

本文件适用于整个仓库。修改代码前先阅读本文件、`README.md`、相关入口和完整调用链。

## 项目现状

- 项目是 Go 1.25+ 编写的精弘 QQ 群助手，通过 NapCat SDK v1.0.2 接入 OneBot 11。入口是 `cmd/bot/main.go`。
- WPS XLSX 是知识唯一真源。启动和 `/reload` 下载并解析表格，验证成功后写入 `data/cache/knowledge.xlsx`，再原子替换进程内 `knowledge.IndexRef`。MySQL 不保存知识正文。
- 普通关键词回复使用 keyword/alias 精确匹配（大小写和首尾空格已归一化）；`/ai` 使用 Eino ReAct Agent 调用内存 `search_knowledge` 工具，支持 AND、OR 和 Go 正则搜索。
- MySQL 只保存 `knowledge_trigger_logs`、`scheduled_jobs` 和 `group_join_requests`。表结构以 `deploy/mysql/init/001_schema.sql` 为准，运行时禁止 `AutoMigrate`。schema 变更后需在 `deploy/mysql/migrations/` 追加迁移脚本供已有部署使用。
- 数据访问统一位于 `internal/storage`，使用直接 GORM 调用与手写模型（`internal/storage/models.go`）。修改字段时先改 `deploy/mysql/init/001_schema.sql`，再同步本地模型；运行时仍禁止 `AutoMigrate`。
- Compose 包含 MySQL、NapCat、quote 和 bot。quote 服务从 `zjutjh/qq-quote-generator` 构建，客户端先请求 `/gif/base64/`，失败后回退 `/png/base64/`。
- bot 只主动连接 NapCat 正向 WebSocket，不提供反向 WebSocket 服务。容器默认在 `:8080/healthz` 暴露端点供 Docker healthcheck 使用，可通过 `HEALTH_PORT` 调整；容器通过 entrypoint 支持 `PUID/PGID` 环境变量以匹配 NAS（群晖/威联通）宿主用户权限，避免 `./data/` 挂载点写入失败。
- 仓库当前不保留 `docs/` 内容，也不提交 `*_test.go`。不要自行恢复历史设计文档或测试文件，除非任务明确要求。

## 关键业务约束

- 除 `/admin` 外，所有群聊 `/` 命令都可以直接触发，不要求 @机器人；`/admin` 及其子命令必须明确 @机器人。关键词回复同样不要求 @。命令名必须完整匹配，不能用宽泛前缀把 `/air` 当成 `/ai`。
- `/reload`、`/admin` 只允许当前群 owner/admin。权限每次通过 NapCat 实时查询，不缓存、不持久化。
- `/ai` 同时最多处理 2 个请求，必须受配置超时和问题长度限制；回答只能依据工具搜索结果，不得回退到模型自身知识编造校务信息。
- `/q N` 包含被回复消息及其之前的 `N-1` 条消息，按时间从旧到新生成引用图，不能包含后续消息。
- 引用图中的 `at` 片段优先显示当前群名片，其次显示 QQ 昵称；成员查询失败时回退 QQ 号，不能因此阻断引用图生成。
- WPS 导入为空或解析失败时不得替换现有索引或有效缓存。菜单 `%编号` 路径构建必须能终止循环父子关系。
- WPS 基础列为 keyword、answer、维护备注；可选列为 aliases、category、usage、status、source_id。空 keyword/answer 跳过，冲突 source_key 不得静默覆盖不同答案。
- 关键词 answer 中只执行 CQ image 和 CQ file，其他 CQ 标签保留为普通文本。两者的远程来源仅允许合法 HTTP(S)，本地来源仅允许 `/` 分隔的安全相对路径，并映射到 NapCat 的 `/app/jxh-media/`；拒绝绝对路径、反斜杠、`.`、`..`、查询参数、`file://` 和 `base64://` 输入。远程文件由 bot 限制跳转、地址、超时、并发和暂存总量后写入共享的 `/app/data/flash/`，单文件不超过 100 MiB，再通过 NapCat 闪传发送；同一来源在保留期内复用暂存文件，失败提示不得回显可能带签名参数的源 URL，禁止改用群文件上传。
- 定时任务只接受 `每天` 或 `单次`，格式：每天 `HH:MM`；单次 `YYYY-MM-DD HH:MM`（存 `run_date` 列），群号必须为正。只有消息实际发送成功后才能更新 `last_run_at` 或禁用单次任务；NapCat 未连接和发送错误必须保留任务等待重试。新建每天任务时若当前时刻已过 `HH:MM`，需将 `LastRunAt` 设为当前时间避免当天立即触发。
- WPS 冲突 source_key 不同答案时保留首条并禁用其 AI 检索（`AIEnabled=false`），不得静默覆盖。
- 群申请只有一个业务标识：NapCat 内部的 `GroupNotify.seq`。实时 OneBot 群申请事件将它以字符串字段 `flag` 返回，`get_group_system_msg` 历史查询则将同一个值以数值字段 `request_id` 返回；两者不是两个不同 ID，只是 NapCat 两套接口的字段名和 JSON 类型不一致。
- 群申请统一以数据库 `flag` 列唯一去重：实时事件必须原样保存 `flag`，以便后续 `set_group_add_request` 精确处理；补同步必须把 `request_id` 不经 `float64` 地解析为无损十进制字符串后写入同一个 `flag` 字段。禁止新增 `request_id`、`system_request_id` 等平行身份列或唯一索引，也禁止用群号、用户、验证信息或时间窗口模糊关联两条记录。`flag` 不得截断、哈希或伪造，空值和超过 512 字符的值必须报错。导出文件按来源群拆分，只写入本地，不默认上传 QQ。
- 词条统计失败不能阻断关键词回复或 `/ai` 回答，只记录成功发送的回复；导出按 source_key 合并关键词和 AI 次数。统计使用应用时区的自然日边界；导出时间戳必须显式 `t.In(location).Format(...)`，不能直接使用 `time.Format`（否则会显示 UTC 时刻）。
- 词条日志按 `database.trigger_log_retention_days`（默认 180 天）定期清理；无限增长会拖慢查询和体积。
- 链接净化只处理受支持的 Bilibili/小红书域名。短链跳转必须限制协议、端口、目标域名、跳转次数和超时，不能放宽成通用 URL 抓取器。

## 实现规范

- 优先复用现有实现、Go 标准库和已安装依赖。不要添加单实现接口、无调用 wrapper、兼容层、未来配置或重复 helper。
- 修 bug 前搜索所有调用者，尽量在共享根因位置修一次。保持改动文件和 diff 最少，不做任务外重构或格式化。
- 错误必须保留上下文；附加能力失败可以记录并降级，但消息发送、缓存替换、数据库写入等关键操作不得伪装成功。
- 外部输入边界必须验证：OneBot/NapCat 动态 JSON、WPS 内容、管理员命令、URL、文件路径、配置和数据库字段。管理员命令要兼容全角空格（`　`）。
- 并发共享状态沿用现有策略：知识索引用 `atomic.Pointer`，Pipeline sender 用互斥锁，AI 来源收集和并发槽必须保持线程安全。
- NapCat 事件循环必须异步分发：慢路径（`/reload`、`/q`、`/ai`、链接净化）会阻塞背压 SDK 缓冲区，因此 `napcat.Server.consume` 每个事件用 goroutine 处理。
- 可选服务优先使用具体指针类型，避免把 typed nil 放入接口后被误判为已初始化。
- 不在日志、错误、提交内容或示例中泄露 access token、WPS sid、AI key、数据库密码和原始敏感申请信息。
- 不手工编辑 `go.sum`；依赖变化使用 `go mod tidy`。
- 当前生产镜像使用 `CGO_ENABLED=0`，新增依赖不得破坏纯 Go 构建，除非任务明确批准部署变更。
- 进程必须响应 `SIGINT` 与 `SIGTERM`（`docker stop` 发送 SIGTERM）；不允许仅捕获 `os.Interrupt`。

## 验证要求

- 当前仓库没有测试文件，因此 `go test ./...` 只能证明所有包可编译，不能证明业务行为。不要把它描述为完整回归测试。
- 每次代码改动至少运行：`gofmt`（仅改动的 Go 文件）、`go test ./...`、`go build ./...`、`go vet ./...`、`go mod tidy -diff` 和 `git diff --check`。
- 可用时补跑 `staticcheck ./...`、`deadcode ./...`、`golangci-lint run` 和 `docker-compose config --quiet`。
- 涉及 NapCat、WPS、MySQL、quote、文件挂载或真实 QQ 行为时，明确区分静态/编译验证与真实服务联调；没有实际联调就不能声称生产行为已验证。

## Git 与交付

- 工作区可能已有用户改动；不要重置、覆盖或顺手清理无关文件。禁止未经明确授权执行 `git reset --hard`、强推、提交或推送。
- 提交前查看现有提交风格。当前提交消息以简短中文 Conventional Commit 为主，例如 `fix: ...`、`feat: ...`、`refactor: ...`。
- 最终说明必须列出实际改动、验证命令及未验证的外部环境，不得用推测替代证据。
