CREATE OR REPLACE VIEW photo_album_visible_items AS
SELECT i.album_id, i.asset_id, i.added_by
FROM photo_album_items i
JOIN photo_albums al ON al.id = i.album_id
JOIN photo_assets c ON c.id = i.asset_id
JOIN nodes cn ON cn.id = c.node_id
WHERE cn.user_id = c.user_id
  AND (c.user_id = al.owner_id OR c.user_id = i.added_by
       OR EXISTS (SELECT 1 FROM photo_partners p
                  JOIN photo_settings s ON s.user_id = p.owner_id AND s.enabled
                  WHERE p.owner_id = c.user_id AND p.viewer_id = i.added_by));
