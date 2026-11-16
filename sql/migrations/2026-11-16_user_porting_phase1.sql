-- 用户端移植 Phase 1 (方案 .plans/2026-11-16_user端移植方案_静态前端与api-v1.md §4.0)
-- 登录态机制: JWT + HttpOnly Cookie + token_version 即时吊销
-- 共库安全: PHP 侧不读写该列, 双系统可并行运行
-- 注意: MySQL 5.7 不支持 ADD COLUMN IF NOT EXISTS, 重复执行报 Duplicate column 可忽略

ALTER TABLE `user`
    ADD COLUMN `token_version` INT NOT NULL DEFAULT 0 COMMENT '登录态版本号: +1 即吊销全部旧 JWT (Go §4.0)';
