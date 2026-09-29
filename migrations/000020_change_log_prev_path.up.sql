-- A move or rename records where the node was before it (the node's disk_path before
-- the change). The sync feed scoped to one folder filters on the node's current path,
-- so without this a node moved out of the folder simply stopped appearing and the
-- daemon kept its local copy; with it the scoped feed reports such a move as a delete.
-- NULL for every other change, and for moves recorded before this column existed.
ALTER TABLE change_log ADD COLUMN prev_path text;
