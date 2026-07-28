-- +goose Up

ALTER TABLE installed_skills ADD COLUMN managed INTEGER NOT NULL DEFAULT 0;

-- +goose Down

ALTER TABLE installed_skills DROP COLUMN managed;
