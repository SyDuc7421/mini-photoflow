-- author_name is copied from auth when the post is created; there is no foreign key to another service.
CREATE TABLE IF NOT EXISTS posts (
    id BIGSERIAL PRIMARY KEY,
    author_id BIGINT NOT NULL,
    author_name TEXT NOT NULL,
    caption TEXT NOT NULL DEFAULT '',
    image_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS posts_author_idx ON posts (author_id, created_at DESC, id DESC);
