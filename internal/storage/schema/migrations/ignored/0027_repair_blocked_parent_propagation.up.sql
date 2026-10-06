-- Repair false blockers left by migrations 0047/0059 and their ignored twins.
-- Separate indexed target joins preserve parent-child filtering on Dolt 2.2.x
-- (gastownhall/beads#7037); shipped migration bytes stay unchanged.
-- Derived state preserves timestamps and is safe to recompute repeatedly.
UPDATE wisps SET is_blocked = 0, updated_at = updated_at;

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
          JOIN wisps t ON t.id = d.depends_on_wisp_id
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
                FROM wisp_dependencies cd
                JOIN wisps child ON child.id = cd.issue_id
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
                  FROM wisp_dependencies cd
                  JOIN wisps child ON child.id = cd.issue_id
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
    FROM wisps w
    WHERE w.status NOT IN ('closed', 'pinned')
      AND (
        EXISTS (
          SELECT 1
          FROM wisp_dependencies d
          JOIN issues t ON t.id = d.depends_on_issue_id
          WHERE d.issue_id = w.id
            AND d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR EXISTS (
          SELECT 1
          FROM wisp_dependencies d
          JOIN wisps t ON t.id = d.depends_on_wisp_id
          WHERE d.issue_id = w.id
            AND d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR EXISTS (
          SELECT 1
          FROM wisp_dependencies d
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
                FROM wisp_dependencies cd
                JOIN wisps child ON child.id = cd.issue_id
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
                  FROM wisp_dependencies cd
                  JOIN wisps child ON child.id = cd.issue_id
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
    JOIN wisp_dependencies d ON d.depends_on_issue_id = r.id
    JOIN wisps child ON child.id = d.issue_id
    WHERE r.kind = 'issue'
      AND d.type = 'parent-child'
      AND child.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'wisp', d.issue_id
    FROM reachable r
    JOIN wisp_dependencies d ON d.depends_on_wisp_id = r.id
    JOIN wisps child ON child.id = d.issue_id
    WHERE r.kind = 'wisp'
      AND d.type = 'parent-child'
      AND child.status NOT IN ('closed', 'pinned')
  )
UPDATE wisps
SET is_blocked = 1, updated_at = updated_at
WHERE id IN (SELECT id FROM reachable WHERE kind = 'wisp')
  AND status NOT IN ('closed', 'pinned');
