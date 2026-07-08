-- +goose Up
ALTER TABLE installed_skills ADD COLUMN sigstore_bundle BLOB DEFAULT NULL;

-- +goose Down
ALTER TABLE installed_skills DROP COLUMN sigstore_bundle;
