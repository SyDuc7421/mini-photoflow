-- The feed's own copy of what it needs. There are no foreign keys: events from post and social
-- travel on different connections, so a like can arrive before the post it belongs to.
-- Counts are derived with count(*) at read time, so applying an event twice changes nothing.
CREATE TABLE IF NOT EXISTS posts (
    post_id BIGINT PRIMARY KEY,
    author_id BIGINT NOT NULL,
    author_name TEXT NOT NULL,
    caption TEXT NOT NULL,
    image_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS posts_author_idx ON posts (author_id, created_at DESC, post_id DESC);

CREATE TABLE IF NOT EXISTS follows (
    follower_id BIGINT NOT NULL,
    followee_id BIGINT NOT NULL,
    PRIMARY KEY (follower_id, followee_id)
);

CREATE TABLE IF NOT EXISTS likes (
    post_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    PRIMARY KEY (post_id, user_id)
);

CREATE TABLE IF NOT EXISTS comments (
    post_id BIGINT NOT NULL,
    comment_id BIGINT NOT NULL,
    PRIMARY KEY (post_id, comment_id)
);
