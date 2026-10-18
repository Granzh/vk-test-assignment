-- +goose Up
-- Contact collection from final winners (spec 6.3) as a state machine in the
-- database: dialog_step is NULL (no dialog), 'name' (waiting for the full
-- name) or 'consent' (name received, waiting for the explicit consent).
-- A reserve taken off the queue keeps is_reserve and reserve_position and gets
-- promoted_at; a forfeited prize records why.
ALTER TABLE final_winners
    ADD COLUMN dialog_step        TEXT CHECK (dialog_step IN ('name', 'consent')),
    ADD COLUMN forfeit_reason     TEXT CHECK (forfeit_reason IN ('expired', 'declined', 'undeliverable')),
    ADD COLUMN promoted_at        TIMESTAMPTZ,
    ADD COLUMN contact_purged_at  TIMESTAMPTZ;

UPDATE final_winners SET forfeit_reason = 'expired' WHERE status = 'forfeited';

-- The name is now asked before the consent, so the old rule "a name needs a
-- consent" is replaced by rules on the final states.
ALTER TABLE final_winners DROP CONSTRAINT final_winners_check;

ALTER TABLE final_winners
    ADD CONSTRAINT final_winners_received_chk  CHECK (status <> 'contact_received' OR consent_given_at IS NOT NULL),
    ADD CONSTRAINT final_winners_forfeit_chk   CHECK ((status = 'forfeited') = (forfeit_reason IS NOT NULL)),
    ADD CONSTRAINT final_winners_dialog_chk    CHECK (dialog_step IS NULL OR status = 'notified'),
    ADD CONSTRAINT final_winners_no_data_chk   CHECK (status <> 'forfeited' OR (contact_name IS NULL AND consent_given_at IS NULL)),
    ADD CONSTRAINT final_winners_consent_step_chk CHECK (dialog_step IS DISTINCT FROM 'consent' OR contact_name IS NOT NULL);

-- +goose Down
ALTER TABLE final_winners
    DROP CONSTRAINT final_winners_consent_step_chk,
    DROP CONSTRAINT final_winners_no_data_chk,
    DROP CONSTRAINT final_winners_dialog_chk,
    DROP CONSTRAINT final_winners_forfeit_chk,
    DROP CONSTRAINT final_winners_received_chk;
ALTER TABLE final_winners
    ADD CHECK (contact_name IS NULL OR consent_given_at IS NOT NULL);
ALTER TABLE final_winners
    DROP COLUMN contact_purged_at,
    DROP COLUMN promoted_at,
    DROP COLUMN forfeit_reason,
    DROP COLUMN dialog_step;
