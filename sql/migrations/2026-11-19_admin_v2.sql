-- admin v2 移植 Phase 3 新表 (方案 .plans/2026-11-18_admin移植方案_样式对齐旧版与api-v2.md §4.3)
-- auto / blockip / unblockip 与 PHP 同构 (共库灰度可互写互读);
-- rss_request_log 为 PHP SQLite 表的 MySQL 化 (列名一致)。

CREATE TABLE IF NOT EXISTS `auto` (
  `id` bigint(20) NOT NULL AUTO_INCREMENT,
  `type` int(11) NOT NULL,
  `value` longtext NOT NULL,
  `sign` longtext NOT NULL,
  `datetime` bigint(20) NOT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `blockip` (
  `id` bigint(20) NOT NULL AUTO_INCREMENT,
  `nodeid` int(11) NOT NULL,
  `ip` text NOT NULL,
  `datetime` bigint(20) NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_blockip_node` (`nodeid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `unblockip` (
  `id` bigint(20) NOT NULL AUTO_INCREMENT,
  `ip` text NOT NULL,
  `datetime` bigint(20) NOT NULL,
  `userid` bigint(20) NOT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `rss_request_log` (
  `id` bigint(20) NOT NULL AUTO_INCREMENT,
  `user_id` bigint(20) NOT NULL DEFAULT 0,
  `email` varchar(255) NOT NULL DEFAULT '',
  `token` varchar(64) NOT NULL DEFAULT '',
  `app` varchar(64) NOT NULL DEFAULT '',
  `ip` varchar(64) NOT NULL DEFAULT '',
  `method` varchar(8) NOT NULL DEFAULT 'GET',
  `uri` varchar(512) NOT NULL DEFAULT '',
  `query` text,
  `user_agent` varchar(512) NOT NULL DEFAULT '',
  `headers` text,
  `created_at` datetime NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_rrl_user` (`user_id`),
  KEY `idx_rrl_created` (`created_at`),
  KEY `idx_rrl_app` (`app`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
