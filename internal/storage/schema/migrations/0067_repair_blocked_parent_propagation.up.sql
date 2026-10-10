-- Repair false blockers left by migrations 0047/0059 and their ignored twins.
-- Separate indexed target joins preserve parent-child filtering on Dolt 2.2.x
-- (gastownhall/beads#7037); shipped migration bytes stay unchanged.
-- Derived state preserves timestamps and is safe to recompute repeatedly.
DROP TABLE IF EXISTS __bd_0067_recompute_wisps;
CREATE TABLE __bd_0067_recompute_wisps (
    id VARCHAR(255) NOT NULL,
    status VARCHAR(32),
    PRIMARY KEY (id)
);
DROP TABLE IF EXISTS __bd_0067_recompute_wisp_deps;
CREATE TABLE __bd_0067_recompute_wisp_deps (
    issue_id VARCHAR(255),
    depends_on_issue_id VARCHAR(255),
    depends_on_wisp_id VARCHAR(255),
    type VARCHAR(32),
    metadata JSON
);

SET @has_wisps = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ('wisps', 'wisp_dependencies')
);
SET @has_split_wisp_deps = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies'
      AND COLUMN_NAME IN ('depends_on_issue_id', 'depends_on_wisp_id')
);

SET @sql = IF(@has_wisps > 1 AND @has_split_wisp_deps > 1,
    'INSERT INTO __bd_0067_recompute_wisps (id, status) SELECT id, status FROM wisps',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 1 AND @has_split_wisp_deps > 1,
    'INSERT INTO __bd_0067_recompute_wisp_deps (issue_id, depends_on_issue_id, depends_on_wisp_id, type, metadata) SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, type, metadata FROM wisp_dependencies',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- Self-assign updated_at: is_blocked is derived state and issues.updated_at
-- carries ON UPDATE CURRENT_TIMESTAMP; letting the recompute bump it plants
-- per-clone wall clock in a synced table (see blocked_state.go, bd-578h9.19).
UPDATE issues SET is_blocked = 0, updated_at = updated_at;

WITH RECURSIVE
  directly_blocked(kind, id) AS (
    SELECT DISTINCT 'issue', i.id
    FROM issues i
    WHERE i.status NOT IN ('closed', 'pinned')
      AND (
        EXISTS (
          SELECT 1
          FROM dependencies d
          JOIN issues t ON t.id = d.depends_on_issue_id
          WHERE d.issue_id = i.id
            AND d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR EXISTS (
          SELECT 1
          FROM dependencies d
          JOIN __bd_0067_recompute_wisps t ON t.id = d.depends_on_wisp_id
          WHERE d.issue_id = i.id
            AND d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR EXISTS (
          SELECT 1
          FROM dependencies d
          WHERE d.issue_id = i.id
            AND d.type = 'waits-for'
            AND (
              EXISTS (
                SELECT 1
                FROM dependencies cd
                JOIN issues child ON child.id = cd.issue_id
                WHERE cd.type = 'parent-child'
                  AND (
                    (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                    OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                  )
                  AND child.status NOT IN ('closed', 'pinned')
              )
              OR EXISTS (
                SELECT 1
                FROM __bd_0067_recompute_wisp_deps cd
                JOIN __bd_0067_recompute_wisps child ON child.id = cd.issue_id
                WHERE cd.type = 'parent-child'
                  AND (
                    (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                    OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                  )
                  AND child.status NOT IN ('closed', 'pinned')
              )
            )
            AND NOT (
              COALESCE(JSON_UNQUOTE(JSON_EXTRACT(d.metadata, '$.gate')), 'all-children') = 'any-children'
              AND (
                EXISTS (
                  SELECT 1
                  FROM dependencies cd
                  JOIN issues child ON child.id = cd.issue_id
                  WHERE cd.type = 'parent-child'
                    AND (
                      (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                      OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                    )
                    AND child.status = 'closed'
                )
                OR EXISTS (
                  SELECT 1
                  FROM __bd_0067_recompute_wisp_deps cd
                  JOIN __bd_0067_recompute_wisps child ON child.id = cd.issue_id
                  WHERE cd.type = 'parent-child'
                    AND (
                      (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                      OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                    )
                    AND child.status = 'closed'
                )
              )
            )
        )
      )
    UNION
    SELECT DISTINCT 'wisp', w.id
    FROM __bd_0067_recompute_wisps w
    WHERE w.status NOT IN ('closed', 'pinned')
      AND (
        EXISTS (
          SELECT 1
          FROM __bd_0067_recompute_wisp_deps d
          JOIN issues t ON t.id = d.depends_on_issue_id
          WHERE d.issue_id = w.id
            AND d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR EXISTS (
          SELECT 1
          FROM __bd_0067_recompute_wisp_deps d
          JOIN __bd_0067_recompute_wisps t ON t.id = d.depends_on_wisp_id
          WHERE d.issue_id = w.id
            AND d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR EXISTS (
          SELECT 1
          FROM __bd_0067_recompute_wisp_deps d
          WHERE d.issue_id = w.id
            AND d.type = 'waits-for'
            AND (
              EXISTS (
                SELECT 1
                FROM dependencies cd
                JOIN issues child ON child.id = cd.issue_id
                WHERE cd.type = 'parent-child'
                  AND (
                    (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                    OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                  )
                  AND child.status NOT IN ('closed', 'pinned')
              )
              OR EXISTS (
                SELECT 1
                FROM __bd_0067_recompute_wisp_deps cd
                JOIN __bd_0067_recompute_wisps child ON child.id = cd.issue_id
                WHERE cd.type = 'parent-child'
                  AND (
                    (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                    OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                  )
                  AND child.status NOT IN ('closed', 'pinned')
              )
            )
            AND NOT (
              COALESCE(JSON_UNQUOTE(JSON_EXTRACT(d.metadata, '$.gate')), 'all-children') = 'any-children'
              AND (
                EXISTS (
                  SELECT 1
                  FROM dependencies cd
                  JOIN issues child ON child.id = cd.issue_id
                  WHERE cd.type = 'parent-child'
                    AND (
                      (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                      OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                    )
                    AND child.status = 'closed'
                )
                OR EXISTS (
                  SELECT 1
                  FROM __bd_0067_recompute_wisp_deps cd
                  JOIN __bd_0067_recompute_wisps child ON child.id = cd.issue_id
                  WHERE cd.type = 'parent-child'
                    AND (
                      (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                      OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                    )
                    AND child.status = 'closed'
                )
              )
            )
        )
      )
  ),
  reachable(kind, id) AS (
    SELECT kind, id FROM directly_blocked
    UNION
    SELECT 'issue', d.issue_id
    FROM reachable r
    JOIN dependencies d ON d.depends_on_issue_id = r.id
    JOIN issues child ON child.id = d.issue_id
    WHERE r.kind = 'issue'
      AND d.type = 'parent-child'
      AND child.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'issue', d.issue_id
    FROM reachable r
    JOIN dependencies d ON d.depends_on_wisp_id = r.id
    JOIN issues child ON child.id = d.issue_id
    WHERE r.kind = 'wisp'
      AND d.type = 'parent-child'
      AND child.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'wisp', d.issue_id
    FROM reachable r
    JOIN __bd_0067_recompute_wisp_deps d ON d.depends_on_issue_id = r.id
    JOIN __bd_0067_recompute_wisps child ON child.id = d.issue_id
    WHERE r.kind = 'issue'
      AND d.type = 'parent-child'
      AND child.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'wisp', d.issue_id
    FROM reachable r
    JOIN __bd_0067_recompute_wisp_deps d ON d.depends_on_wisp_id = r.id
    JOIN __bd_0067_recompute_wisps child ON child.id = d.issue_id
    WHERE r.kind = 'wisp'
      AND d.type = 'parent-child'
      AND child.status NOT IN ('closed', 'pinned')
  )
UPDATE issues
SET is_blocked = 1, updated_at = updated_at
WHERE id IN (SELECT id FROM reachable WHERE kind = 'issue')
  AND status NOT IN ('closed', 'pinned');

DROP TABLE __bd_0067_recompute_wisps;
DROP TABLE __bd_0067_recompute_wisp_deps;
