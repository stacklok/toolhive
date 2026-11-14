-- +goose Up

ALTER TABLE installed_plugins ADD COLUMN managed INTEGER NOT NULL DEFAULT 0;

-- +goose Down

ALTER TABLE installed_plugins DROP COLUMN managed;
