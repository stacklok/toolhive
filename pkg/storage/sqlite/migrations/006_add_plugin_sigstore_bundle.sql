-- +goose Up

ALTER TABLE installed_plugins ADD COLUMN sigstore_bundle BLOB DEFAULT NULL;

-- +goose Down

ALTER TABLE installed_plugins DROP COLUMN sigstore_bundle;
