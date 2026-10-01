-- Doctor results carry the code of their fact line (agent.proto DoctorResult.detail_code), so the admin UI can write
-- the line in the viewer's language. Additive: one column with an empty default, old rows keep showing their detail.

-- +goose Up
ALTER TABLE doctor_result ADD COLUMN detail_code TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE doctor_result DROP COLUMN detail_code;
