-- The gallery map groups photos by a grid in the Web Mercator projection: at zoom z a cell is
-- 1/2^(z+1) of the world's width, 128 px on screen at that zoom (256 px tiles). The clusters
-- and the timeline's area filter both name cells through these functions, so they agree.
-- Latitudes beyond the projection's ±85.0511° fall into the edge rows.
--
-- Each body is a single expression with no FROM, so Postgres inlines it into the query
-- instead of calling the function per row (the clusters query runs it for every located
-- card on each map move). Hence the repeated subexpressions.

-- {x, y} of the cell.
CREATE OR REPLACE FUNCTION photo_map_cell(lat double precision, lon double precision, z int) RETURNS bigint[]
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT ARRAY[
        least(floor((lon + 180) / 360 * power(2, z + 1)), power(2, z + 1) - 1)::bigint,
        least(greatest(floor((1 - ln(tan(radians(greatest(least(lat, 85.0511287798), -85.0511287798)))
                                    + 1 / cos(radians(greatest(least(lat, 85.0511287798), -85.0511287798)))) / pi())
                             / 2 * power(2, z + 1)), 0), power(2, z + 1) - 1)::bigint
    ]
$$;

-- One number per cell, x·2^(z+1) + y: what the timeline's area filter compares.
CREATE OR REPLACE FUNCTION photo_map_key(lat double precision, lon double precision, z int) RETURNS bigint
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT least(floor((lon + 180) / 360 * power(2, z + 1)), power(2, z + 1) - 1)::bigint * (2::bigint << z)
         + least(greatest(floor((1 - ln(tan(radians(greatest(least(lat, 85.0511287798), -85.0511287798)))
                                      + 1 / cos(radians(greatest(least(lat, 85.0511287798), -85.0511287798)))) / pi())
                               / 2 * power(2, z + 1)), 0), power(2, z + 1) - 1)::bigint
$$;
