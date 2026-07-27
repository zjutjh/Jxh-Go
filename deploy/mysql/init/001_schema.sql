-- 精小弘 Go bot MySQL schema
-- 运行时不使用 AutoMigrate；MySQL 8.4 首次初始化时执行本文件。

CREATE TABLE IF NOT EXISTS `knowledge_trigger_logs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `source_key` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT 'WPS 词条稳定键',
  `trigger_type` varchar(32) NOT NULL COMMENT 'keyword_reply 或 ai_retrieval',
  `group_id` bigint NOT NULL COMMENT '触发所在 QQ 群',
  `triggered_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_trigger_stats` (`triggered_at`, `source_key`, `trigger_type`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS `scheduled_jobs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `type` varchar(16) NOT NULL COMMENT '任务类型：每天/单次',
  `time_hhmm` varchar(5) NOT NULL COMMENT '触发时间，格式 HH:MM',
  `run_date` date DEFAULT NULL COMMENT '单次任务执行日期，格式 YYYY-MM-DD；每天任务此字段为 NULL',
  `group_id` bigint NOT NULL COMMENT 'QQ群号',
  `message` text NOT NULL COMMENT '定时发送内容',
  `enabled` boolean NOT NULL COMMENT '是否启用',
  `last_run_at` datetime(3) DEFAULT NULL COMMENT '最近执行时间；用于防止同一天重复触发',
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS `group_join_requests` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `flag` varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT 'NapCat 群通知标识；实时事件取 flag，系统消息取 request_id 字符串',
  `group_id` bigint DEFAULT NULL COMMENT 'QQ群号',
  `user_id` bigint DEFAULT NULL COMMENT '申请人 QQ',
  `student_id` varchar(64) DEFAULT NULL COMMENT '申请信息中显式填写的学号',
  `student_name` varchar(64) DEFAULT NULL COMMENT '申请信息中显式填写的姓名',
  `major` varchar(128) DEFAULT NULL COMMENT 'AI 从验证信息中提取的专业',
  `sub_type` varchar(32) DEFAULT NULL COMMENT '申请类型：add/invite 等',
  `comment` text DEFAULT NULL COMMENT '申请验证信息',
  `status` varchar(32) NOT NULL COMMENT '处理状态：pending/processed',
  `source` varchar(32) NOT NULL COMMENT '来源：event/system',
  `raw_json` mediumtext DEFAULT NULL COMMENT 'NapCat 原始事件或系统消息 JSON',
  `system_raw_json` mediumtext DEFAULT NULL COMMENT 'NapCat 最近一次系统消息 JSON',
  `ai_parse_status` varchar(32) NOT NULL DEFAULT 'pending' COMMENT 'AI 解析状态：pending/completed/failed/skipped',
  `ai_parse_attempts` int unsigned NOT NULL DEFAULT 0 COMMENT 'AI 解析尝试次数',
  `requested_at` datetime(3) DEFAULT NULL COMMENT '申请时间',
  `processed_at` datetime(3) DEFAULT NULL COMMENT '首次观察到已处理状态的时间',
  `first_seen_at` datetime(3) DEFAULT NULL COMMENT '首次登记时间',
  `last_seen_at` datetime(3) DEFAULT NULL COMMENT '最近出现时间',
  `ai_parsed_at` datetime(3) DEFAULT NULL COMMENT 'AI 解析完成时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_group_join_requests_flag` (`flag`),
  KEY `idx_group_join_requests_group_id` (`group_id`),
  KEY `idx_group_join_requests_user_id` (`user_id`),
  KEY `idx_group_join_requests_status` (`status`),
  KEY `idx_group_join_requests_ai_parse_status` (`ai_parse_status`),
  KEY `idx_group_join_requests_last_seen_at` (`last_seen_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
