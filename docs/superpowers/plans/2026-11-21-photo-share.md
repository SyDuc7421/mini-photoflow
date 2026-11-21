# Photo-share Monolith vs Microservice Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build cùng một app share photo (REST API) hai lần, một bản monolith và một bản microservice (gRPC nội bộ, feed bước 1 sync rồi bước 2 event qua NATS), cùng chạy qua một bộ test hợp đồng.

**Architecture:** Một Go module ở root. Monolith là 1 binary, package theo miền, 1 Postgres. Microservice gồm gateway (REST→gRPC, verify JWT) + auth/post/social/feed, mỗi service một DB riêng trong 1 Postgres, ảnh ở MinIO do `post` quản lý. `tests/contract` chạy bằng `BASE_URL` nên đúng cho cả hai bản.

**Tech Stack:** Go 1.22+ (`net/http` mux), `pgx/v5`, `golang-jwt/jwt/v5`, `golang.org/x/crypto/bcrypt`, `minio-go/v7`, `google.golang.org/grpc` + protobuf, `nats.go`, Docker Compose, Postgres 16, MinIO, NATS.

**Spec:** `docs/superpowers/specs/2026-11-21-photo-share-monolith-vs-microservice-design.md`

## Global Constraints

- Ngôn ngữ Go cho cả hai bản; nội bộ microservice chỉ dùng gRPC, không HTTP.
- Hai bản có cùng REST API và cùng bảng mã lỗi (định nghĩa trong `api/openapi.yaml`).
- Mỗi service microservice chỉ truy cập DB của mình (user Postgres riêng); không đọc bảng của service khác.
- Chỉ API, không GUI; không production; chạy bằng `docker compose up`.
- JWT hết hạn 24 giờ; bcrypt cho password; caption tối đa 500 ký tự; ảnh tối đa 5MB.
- Follow/unfollow/like/unlike idempotent. Post/user không tồn tại 404; follow chính mình 400; thiếu/sai JWT 401; đăng ký trùng 409.
- Gateway gắn user id vào gRPC metadata (`x-user-id`); service không nhận user id từ body.
- `social` gọi gRPC sang `auth` để kiểm tra user tồn tại khi follow.
- Feed bước 1: service phụ thuộc chết → `GET /feed` trả 5xx. Feed bước 2: sống khi `social`/`auth` chết; read-model lưu sẵn tên tác giả, số like/comment, cờ "mình đã like".
- Test hợp đồng poll/retry tối đa ~2 giây trước khi assert nội dung feed.
- Feed cursor là `created_at` + id; chọn feed bước 1/2 bằng biến môi trường `FEED_MODE=sync|event`.

## Review Focus

1. Upload ảnh > 5MB → 413, không lưu gì vào MinIO hay DB.
2. Caption > 500 ký tự → 400.
3. Hai post cùng `created_at`: phân trang bằng cursor không trùng, không sót.
4. User chưa follow ai gọi feed → `200` với mảng rỗng `[]` (không phải `null`).
5. `limit` hoặc `before` sai định dạng → 400.

Mỗi dòng trên có test trong Task 0 (contract) và được chạy ở cả hai bản. Riêng dòng 3: contract test không ép được hai post trùng `created_at` qua API, nên Task 4 và Task 10 phải có thêm test cấp store chèn sẵn các dòng cùng `created_at` rồi phân trang.

## Cấu trúc file

```
go.mod
api/openapi.yaml
internal/apierr/apierr.go           # {"error":{"code","message"}} + Write()
internal/jwtutil/jwtutil.go         # Sign / Verify
tests/contract/                     # test e2e, BASE_URL
monolith/{main.go,docker-compose.yml,migrations/,auth/,post/,social/,feed/}
microservices/
  proto/{auth,post,social,feed}.proto  (+ generated pb dir)
  gateway/ auth/ post/ social/ feed/   # mỗi cái có main.go, Dockerfile
  docker-compose.yml
scripts/failure.sh                  # thử tắt service
README.md                           # kết quả so sánh
```

---

### Task 0: Hợp đồng API và test hợp đồng

**Files:**
- Create: `go.mod`, `api/openapi.yaml`, `internal/apierr/apierr.go`, `internal/jwtutil/jwtutil.go`, `tests/contract/contract_test.go`, `tests/contract/client_test.go`

**Interfaces:**
- Produces: `apierr.Write(w http.ResponseWriter, status int, code, msg string)`; `jwtutil.Sign(secret []byte, userID int64, ttl time.Duration) (string, error)`; `jwtutil.Verify(secret []byte, token string) (userID int64, err error)`. Contract suite đọc env `BASE_URL`.

- [ ] **Step 1:** `git init`, `go mod init photoshare`. Viết `api/openapi.yaml` với các route trong spec và bảng lỗi (400/401/404/409/413) dùng định dạng `{"error":{"code":"...","message":"..."}}`.
- [ ] **Step 2:** Viết `apierr` và `jwtutil`, mỗi cái kèm test đơn vị (`TestVerifyRejectsExpired`, `TestWriteFormat`).
- [ ] **Step 3: Viết test hợp đồng (chưa có server nào).** Mỗi test dùng username ngẫu nhiên, helper `eventually(2*time.Second, fn)` cho assert nội dung feed. Tên test:
  `TestRegisterDuplicate409`, `TestLoginWrongPassword401`, `TestNoTokenIs401`, `TestPostRequiresImage`, `TestPostOver5MB413`, `TestCaptionOver500Is400`, `TestFollowSelf400`, `TestFollowMissingUser404`, `TestFollowTwiceIdempotent`, `TestUnfollowNotFollowingIs204`, `TestLikeMissingPost404`, `TestLikeTwiceCountsOnce`, `TestCommentAndList`, `TestFeedOnlyFollowedNewestFirst`, `TestFeedEmptyIsEmptyArray`, `TestFeedItemFields` (post, author name, image URL, like count, comment count, liked_by_me), `TestFeedCursorNoDupesWithSameTimestamp`, `TestFeedBadLimitOrCursor400`.
- [ ] **Step 4:** Chạy `BASE_URL=http://localhost:8080 go test ./tests/contract/... -v`. Expected: FAIL (connection refused) ở mọi test hợp đồng; test đơn vị của `apierr`/`jwtutil` PASS.
- [ ] **Step 5:** Commit `feat: api contract and contract tests`.

### Task 1: Monolith — khung, compose, auth

**Files:**
- Create: `monolith/main.go`, `monolith/docker-compose.yml`, `monolith/Dockerfile`, `monolith/migrations/001_init.sql`, `monolith/auth/auth.go`

**Interfaces:**
- Consumes: `apierr.Write`, `jwtutil.Sign/Verify`.
- Produces: `auth.Register(mux *http.ServeMux, db *pgxpool.Pool, secret []byte)` đăng ký `POST /register`, `POST /login`; middleware `auth.Require(secret []byte, next func(w, r, userID int64)) http.HandlerFunc`; hàm `auth.UserExists(ctx, db, id int64) (bool, error)` và `auth.Names(ctx, db, ids []int64) (map[int64]string, error)`.

- [ ] **Step 1:** Viết `001_init.sql` cho cả monolith: `users`, `posts`, `follows`, `likes`, `comments` với FK; `main.go` chạy migration nhúng (`embed`) khi khởi động rồi nghe cổng 8080. Compose gồm app + Postgres + MinIO.
- [ ] **Step 2:** `docker compose up -d --build`, chạy contract. Expected: các test register/login/401 PASS, còn lại FAIL.
- [ ] **Step 3:** Implement `auth` (bcrypt, username unique → 409, sai password → 401, token 24h). Chạy lại contract; `TestRegisterDuplicate409`, `TestLoginWrongPassword401`, `TestNoTokenIs401` PASS.
- [ ] **Step 4:** Commit `feat(monolith): skeleton and auth`.

### Task 2: Monolith — post và MinIO

**Files:** Create `monolith/post/post.go`.

**Interfaces:**
- Consumes: `auth.Require`.
- Produces: `post.Register(mux, db, secret []byte, store, public *minio.Client, bucket string)` (`store` upload qua mạng nội bộ, `public` chỉ ký presigned URL với host mà client truy cập được) đăng ký `POST /posts` (multipart: `image`, `caption`), `GET /posts/{id}`; `post.Exists(ctx, db, id int64) (bool, error)`; response có `image_url` presigned truy cập được từ host.

- [ ] **Step 1:** Chạy contract, xác nhận `TestPostRequiresImage`, `TestPostOver5MB413`, `TestCaptionOver500Is400` FAIL.
- [ ] **Step 2:** Implement: kiểm tra caption ≤ 500 và dùng `http.MaxBytesReader` 5MB (→ 413) **trước** khi ghi MinIO; chỉ nhận content-type ảnh (JPEG/PNG) còn lại 400; ghi MinIO rồi insert DB.
- [ ] **Step 3:** Chạy contract; 3 test trên PASS, và ảnh lấy được qua `image_url`.
- [ ] **Step 4:** Commit `feat(monolith): posts with minio`.

### Task 3: Monolith — social (follow, like, comment)

**Files:** Create `monolith/social/social.go`.

**Interfaces:**
- Consumes: `auth.Require`, `auth.UserExists`, `post.Exists`.
- Produces: `social.Register(mux, db)` đăng ký `PUT/DELETE /users/{id}/follow`, `PUT/DELETE /posts/{id}/like`, `POST/GET /posts/{id}/comments`.

- [ ] **Step 1:** Chạy contract, xác nhận các test follow/like/comment FAIL.
- [ ] **Step 2:** Implement với `INSERT ... ON CONFLICT DO NOTHING` (idempotent), `DELETE` luôn 204. Follow chính mình → 400; user/post không tồn tại → 404.
- [ ] **Step 3:** Chạy contract; các test `Follow*`, `Unfollow*`, `Like*`, `Comment*` PASS.
- [ ] **Step 4:** Commit `feat(monolith): follow like comment`.

### Task 4: Monolith — feed

**Files:** Create `monolith/feed/feed.go`.

**Interfaces:**
- Produces: `feed.Register(mux, db)` đăng ký `GET /feed?limit=&before=`; `before` là cursor `"<created_at_unix_micro>_<id>"`; response `{"items":[...],"next":"<cursor>|null"}`; item có `id, author, caption, image_url, likes, comments, liked_by_me, created_at`.

- [ ] **Step 1:** Chạy contract, xác nhận `TestFeed*` FAIL.
- [ ] **Step 2:** Implement một truy vấn `JOIN` (posts × follows × users, đếm like/comment bằng subquery), sắp xếp `(created_at, id) DESC`, cursor so sánh theo tuple. Thêm test cấp store `TestFeedPagingSameCreatedAt` (chèn 5 post cùng `created_at`, phân trang limit=2, không lặp/sót). `limit` mặc định 20, tối đa 50. Mảng rỗng phải là `[]`.
- [ ] **Step 3:** Chạy toàn bộ contract trên monolith. Expected: **tất cả PASS**.
- [ ] **Step 4:** Commit `feat(monolith): feed`. Monolith hoàn tất.

### Task 5: Microservice — proto, hạ tầng, gateway, auth

**Files:**
- Create: `microservices/proto/*.proto`, `microservices/docker-compose.yml`, `microservices/{gateway,auth}/main.go`, `microservices/{gateway,auth}/Dockerfile`, script tạo 4 DB + 4 user Postgres (`microservices/initdb/init.sql`)

**Interfaces:**
- Produces proto: `auth.Auth` { `Register`, `Login` (trả JWT), `UserExists(id) → bool`, `GetUsers(ids) → map<id,name>` }; `post.Post` { `CreatePost` (client-streaming: metadata + chunk, giới hạn 5MB), `GetPost`, `ListPostsByAuthors(author_ids, before, limit)`, `PostExists` }; `social.Social` { `Follow`, `Unfollow`, `Like`, `Unlike`, `AddComment`, `ListComments`, `ListFollowing(user_id)`, `CountsForPosts(post_ids, viewer_id)` }; `feed.Feed.GetFeed(user_id, before, limit)`. Gateway đọc `x-user-id` metadata từ gateway sang service; mã lỗi gRPC map sang HTTP trong gateway (NotFound→404, AlreadyExists→409, InvalidArgument→400, Unauthenticated→401, ResourceExhausted→413, Unavailable→503).

- [ ] **Step 1:** Viết proto, sinh code (`protoc` hoặc `buf`, ghi lệnh vào `README.md`). Compose gồm Postgres (init 4 DB/4 user), MinIO, gateway, auth.
- [ ] **Step 2:** Implement `auth` service và các route `/register`, `/login` ở gateway (gateway verify JWT bằng `jwtutil.Verify`, gắn `x-user-id`).
- [ ] **Step 3:** Chạy contract với `BASE_URL=http://localhost:8080` (cổng gateway). Expected: 3 test auth PASS.
- [ ] **Step 4:** Commit `feat(ms): proto, gateway, auth`.

### Task 6: Microservice — post service

**Files:** Create `microservices/post/main.go` (+Dockerfile); modify gateway thêm `POST /posts`, `GET /posts/{id}`.

- [ ] **Step 1:** Chạy contract, xác nhận `TestPost*` FAIL.
- [ ] **Step 2:** Implement `post`: nhận stream, đếm byte, vượt 5MB → `ResourceExhausted` và không ghi gì; ghi MinIO, insert DB riêng, trả presigned URL truy cập được từ host. Gateway stream request multipart sang gRPC, kiểm caption ≤ 500.
- [ ] **Step 3:** Chạy contract; `TestPost*`, `TestCaptionOver500Is400` PASS.
- [ ] **Step 4:** Commit `feat(ms): post service`.

### Task 7: Microservice — social service

**Files:** Create `microservices/social/main.go` (+Dockerfile); modify gateway thêm route follow/like/comment.

- [ ] **Step 1:** Chạy contract, xác nhận test follow/like/comment FAIL.
- [ ] **Step 2:** Implement `social`: follow gọi `auth.UserExists` qua gRPC, like/comment gọi `post.PostExists`; idempotent bằng `ON CONFLICT DO NOTHING`; không có FK sang bảng của service khác.
- [ ] **Step 3:** Chạy contract; mọi test `Follow*`, `Unfollow*`, `Like*`, `Comment*` PASS.
- [ ] **Step 4:** Commit `feat(ms): social service`.

### Task 8: Microservice — feed bước 1 (sync gRPC)

**Files:** Create `microservices/feed/main.go` (+Dockerfile); modify gateway thêm `GET /feed`.

- [ ] **Step 1:** Chạy contract, xác nhận `TestFeed*` FAIL.
- [ ] **Step 2:** Implement `feed` (không có DB): `ListFollowing` → `ListPostsByAuthors` → `CountsForPosts` + `GetUsers`, ghép lại theo thứ tự `(created_at, id) DESC`. Bất kỳ lời gọi nào lỗi → trả lỗi (gateway ra 503).
- [ ] **Step 3:** Chạy toàn bộ contract trên microservice. Expected: **tất cả PASS**, giống monolith.
- [ ] **Step 4:** Thêm `scripts/failure.sh` (tắt `social`, gọi login/post/feed, in mã trạng thái). Commit `feat(ms): feed step 1 and failure script`.

### Task 9: Microservice — phát event (bước 2, phía producer)

**Files:** Modify `microservices/{post,social}/main.go`, `microservices/docker-compose.yml` (thêm NATS); Create `internal/events/events.go`.

**Interfaces:**
- Produces: `events.Publish(nc *nats.Conn, subject string, v any) error`; subject và payload: `post.created {post_id, author_id, author_name, caption, image_url, created_at}`, `follow.set {follower_id, followee_id, following bool}`, `like.set {post_id, user_id, liked bool}`, `comment.added {post_id}`. `author_name` lấy từ `auth.GetUsers` khi `post` tạo bài (nên lưu sẵn tên).

- [ ] **Step 1:** Viết test đơn vị `TestEventPayloadRoundTrip` cho `events`; chạy, FAIL rồi PASS sau khi implement.
- [ ] **Step 2:** Sau mỗi ghi DB thành công, `post`/`social` publish event tương ứng. Một lỗi publish chỉ log, không làm hỏng request.
- [ ] **Step 3:** Chạy contract với `FEED_MODE=sync`. Expected: vẫn PASS toàn bộ (event chưa ai nhận).
- [ ] **Step 4:** Commit `feat(ms): publish events`.

### Task 10: Microservice — feed read-model (bước 2, phía consumer)

**Files:** Modify `microservices/feed/main.go`, thêm DB `feed` trong `initdb/init.sql`; Create `microservices/feed/readmodel.go`.

**Interfaces:**
- Produces: `readmodel.Apply(ctx, ev Event) error` (idempotent theo event); `readmodel.Feed(ctx, userID int64, before string, limit int) ([]Item, string, error)`. `FEED_MODE=event` chọn đường đọc này, `sync` giữ đường gọi gRPC ở Task 8.

- [ ] **Step 1:** Viết test đơn vị `TestApplyIsIdempotent`, `TestFeedFromReadModelOrder` và `TestReadModelPagingSameCreatedAt` (cùng `created_at`, không lặp/sót); chạy, FAIL.
- [ ] **Step 2:** Implement bảng riêng của `feed` (posts, follows, like/comment counters, likes theo user) và consumer NATS; `GetFeed` ở chế độ `event` không gọi service nào khác.
- [ ] **Step 3:** Chạy contract với `FEED_MODE=event`. Expected: toàn bộ PASS (dựa vào `eventually`).
- [ ] **Step 4:** Chạy `scripts/failure.sh` ở cả hai chế độ: tắt `social` → chế độ `sync` feed 503, chế độ `event` feed 200. Commit `feat(ms): feed read model`.

### Task 11: README so sánh

**Files:** Create `README.md`.

- [ ] **Step 1:** Ghi cách chạy từng bản, lệnh sinh proto, cách chạy contract và failure script.
- [ ] **Step 2:** Chạy và ghi lại bốn điểm so sánh với số liệu thật: (1) data ownership (feed là `JOIN` vs gRPC vs read-model); (2) giao tiếp; (3) kết quả `failure.sh` cho `sync`/`event` và khi tắt `auth`; (4) số container, thời gian build và khởi động. Mỗi điểm kèm nhận xét ngắn.
- [ ] **Step 3:** Commit `docs: comparison results`.
