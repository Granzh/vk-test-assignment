-- +goose Up
-- Protocol of the final draw: who held it, how big the drum was, how many
-- winners and reserves were asked for and how many prize tickets each winner gets.
-- final_winners.tickets_count is the number of drum tickets (chances) the user held.
ALTER TABLE final_draw
    ADD COLUMN held_by_telegram_id BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN drum_tickets        INT    NOT NULL DEFAULT 0 CHECK (drum_tickets >= 0),
    ADD COLUMN drum_users          INT    NOT NULL DEFAULT 0 CHECK (drum_users >= 0),
    ADD COLUMN winners_count       INT    NOT NULL DEFAULT 0 CHECK (winners_count >= 0),
    ADD COLUMN reserves_count      INT    NOT NULL DEFAULT 0 CHECK (reserves_count >= 0),
    ADD COLUMN prize_tickets       INT    NOT NULL DEFAULT 1 CHECK (prize_tickets > 0);

-- The final draw is held once: the table can never hold a second row.
CREATE UNIQUE INDEX final_draw_single_idx ON final_draw ((true));

-- Reserves form a queue: position 1 is called first.
ALTER TABLE final_winners
    ADD COLUMN reserve_position INT CHECK (reserve_position > 0),
    ADD CONSTRAINT final_winners_reserve_position_chk CHECK (is_reserve = (reserve_position IS NOT NULL));

CREATE UNIQUE INDEX final_winners_reserve_position_idx ON final_winners (final_draw_id, reserve_position);

-- +goose Down
DROP INDEX final_winners_reserve_position_idx;
ALTER TABLE final_winners
    DROP CONSTRAINT final_winners_reserve_position_chk,
    DROP COLUMN reserve_position;
DROP INDEX final_draw_single_idx;
ALTER TABLE final_draw
    DROP COLUMN prize_tickets,
    DROP COLUMN reserves_count,
    DROP COLUMN winners_count,
    DROP COLUMN drum_users,
    DROP COLUMN drum_tickets,
    DROP COLUMN held_by_telegram_id;
