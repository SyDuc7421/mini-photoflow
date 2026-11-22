-- One database and one login role per service. PUBLIC may not connect, so a service
-- cannot reach another service's database even by accident.
CREATE ROLE auth LOGIN PASSWORD 'auth';
CREATE ROLE post LOGIN PASSWORD 'post';
CREATE ROLE social LOGIN PASSWORD 'social';
CREATE ROLE feed LOGIN PASSWORD 'feed';

CREATE DATABASE auth OWNER auth;
CREATE DATABASE post OWNER post;
CREATE DATABASE social OWNER social;
CREATE DATABASE feed OWNER feed;

REVOKE ALL ON DATABASE auth FROM PUBLIC;
REVOKE ALL ON DATABASE post FROM PUBLIC;
REVOKE ALL ON DATABASE social FROM PUBLIC;
REVOKE ALL ON DATABASE feed FROM PUBLIC;
