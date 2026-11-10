CREATE UNIQUE INDEX IF NOT EXISTS file_versions_node_version ON file_versions USING btree (node_id, version);
CREATE INDEX IF NOT EXISTS file_versions_node ON file_versions USING btree (node_id);
CREATE INDEX IF NOT EXISTS books_node ON books USING btree (node_id);
CREATE INDEX IF NOT EXISTS songs_node ON songs USING btree (node_id);
