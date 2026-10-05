-- A group's colour in the admin (its chip on the users list and in the group editor): the name of a tone of the
-- admin's palette, "" = none picked (the admin then derives one from the id). The palette order is the one the admin
-- offers its swatches in; sky and mint come last because they are also the colours of the Link and Keys chips.
--
-- The groups that exist now get distinct tones in a stable order (oldest first, then by id), so the first six of
-- them never share a colour; the next ones wrap round.

-- +goose Up
ALTER TABLE user_group ADD COLUMN color TEXT NOT NULL DEFAULT '';

UPDATE user_group SET color = (
    SELECT CASE n.k
               WHEN 0 THEN 'lavender'
               WHEN 1 THEN 'sand'
               WHEN 2 THEN 'sage'
               WHEN 3 THEN 'rose'
               WHEN 4 THEN 'sky'
               ELSE 'mint'
           END
    FROM (SELECT id, (ROW_NUMBER() OVER (ORDER BY created_at, id) - 1) % 6 AS k FROM user_group) n
    WHERE n.id = user_group.id
);

-- +goose Down
ALTER TABLE user_group DROP COLUMN color;