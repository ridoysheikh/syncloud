-- +goose Up

-- Shared environment variables: every service in the environment inherits
-- them; a service's own variables win. JSON object of name -> value.
ALTER TABLE environments ADD COLUMN shared_env TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE environments DROP COLUMN shared_env;
