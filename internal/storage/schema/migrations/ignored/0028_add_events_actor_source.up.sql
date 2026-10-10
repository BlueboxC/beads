-- Clone-local twin of 0068; idempotent on init and cloned workspaces.
SET @needs_actor_source = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bd_events_journal') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'bd_events_journal'
          AND COLUMN_NAME = 'actor_source') = 0,
    1, 0
);
SET @sql = IF(@needs_actor_source = 1,
    'ALTER TABLE bd_events_journal ADD COLUMN actor_source VARCHAR(16) NOT NULL DEFAULT ''''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
