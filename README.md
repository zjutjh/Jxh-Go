# Jxh-Go

精小弘 Go + NapCat + Eino 重构实现。

## 当前能力

- NapCat OneBot v11 WebSocket 接入，默认采用 MumuBot 类似的正向 WebSocket：bot 主动连接 NapCat。
- WPS `release` sheet 两列回复表导入。
- 第三列维护备注不入库、不参与 RAG。
- `%编号` 菜单树解析，生成 `path` 和 RAG `content`。
- 关键词与 aliases 精确回复。
- `/reload` 同步知识库并刷新关键词缓存和 `/ai` retriever。
- `/ai <问题>` 基于知识库检索回答；没有配置模型时使用抽取式 fallback。
- 管理员、黑名单、定时任务、processed events、知识库表的 GORM 模型。
- `processed_events` 支持按时间清理。

## 本地运行

```bash
cp config.example.yaml config.yaml
go test ./...
go run ./cmd/bot -config config.yaml
```

NapCat 需要独立运行。参考 MumuBot 的部署方式，本项目默认不把 NapCat 放进 Docker Compose，而是在配置里填写 NapCat 的 OneBot WebSocket 地址：

```text
onebot.ws_url: ws://127.0.0.1:3001
```

在 NapCat 中开启 OneBot 11 WebSocket 服务，监听地址和端口与 `onebot.ws_url` 保持一致。如果 bot 跑在 Docker 里、NapCat 跑在宿主机，compose 默认使用：

```text
JXH_ONEBOT_WS_URL=ws://host.docker.internal:3001
```

## Docker Compose

```bash
cp config.example.yaml config.yaml
docker compose up --build
```

默认只启动 `mysql` 和 `bot`。NapCat 按 MumuBot 风格独立运行，引用图服务可按实际镜像调整后启用 `quote` profile。

## WPS 表规则

基础列：

| 列 | 字段 |
| --- | --- |
| A | keyword |
| B | answer |

C 列如果存在，只作为维护备注，不进入数据库、向量索引或 `/ai` prompt。

可选列：

| 列 | 字段 |
| --- | --- |
| D | aliases |
| E | category |
| F | usage |
| G | status |
| H | source_id |
