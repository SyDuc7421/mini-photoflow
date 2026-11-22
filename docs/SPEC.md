# SPEC: Photo-share (Monolith vs Microservice)

Đặc tả "cái gì" phải chạy được. Kiến trúc nằm ở [ARCHITEC.md](ARCHITEC.md), hợp đồng REST chi tiết ở [api/openapi.yaml](../api/openapi.yaml), lý do thiết kế ở `docs/superpowers/specs/2026-11-21-photo-share-monolith-vs-microservice-design.md`.

## 1. Mục đích

Build cùng một app share photo hai lần (monolith và microservice) để so sánh bốn điểm: data ownership, giao tiếp, lỗi từng phần, chạy/deploy. Không phải sản phẩm thật.

## 2. Phạm vi

**Trong phạm vi:** đăng ký, đăng nhập, đăng ảnh, xem feed, follow/unfollow, like/unlike, comment. Chỉ có REST API.

**Ngoài phạm vi:** production, scale thật, cloud, Kubernetes, GUI, xử lý ảnh (resize/thumbnail), tìm kiếm, notification, phân quyền phức tạp, refresh token, service discovery, tracing, retry/circuit breaker phức tạp.

## 3. Ràng buộc

- Ngôn ngữ: Go (1.22+) cho cả hai bản.
- Microservice dùng gRPC nội bộ, không dùng HTTP nội bộ.
- Ảnh lưu ở MinIO trong Docker. Postgres cho dữ liệu.
- Chạy local bằng Docker Compose (`docker compose up` cho từng bản).
- Hai bản có **cùng REST API và cùng hành vi**, kể cả mã lỗi.
- Microservice: mỗi service chỉ truy cập DB của mình, không có khóa ngoại hay truy vấn xuyên service.

## 4. Yêu cầu chức năng

### 4.1 Tài khoản
- `POST /register {username, password}` → 201 `{id, username}`. Trùng username → 409.
- `POST /login {username, password}` → 200 `{token}`. Sai thông tin → 401.
- JWT HS256, sống 24 giờ, mang user id. Password băm bằng bcrypt.
- Mọi route khác cần `Authorization: Bearer <jwt>`; thiếu, sai hoặc hết hạn → 401.

### 4.2 Bài đăng
- `POST /posts` (multipart: `image`, `caption`) → 201 `{id, author, caption, image_url, created_at}`.
- Ảnh bắt buộc, JPEG hoặc PNG, tối đa 5 MiB. Thiếu hoặc sai loại → 400; quá cỡ → 413 và **không lưu gì** (không có bài, không có file mồ côi).
- Caption tối đa 500 ký tự Unicode (đếm ký tự, không đếm byte); quá giới hạn → 400.
- `GET /posts/{id}` → 200; không tồn tại → 404.
- `image_url` là URL presigned truy cập được từ máy host.

### 4.3 Follow
- `PUT /users/{id}/follow` → 204. Idempotent: gọi hai lần không tạo bản ghi trùng.
- `DELETE /users/{id}/follow` → 204, kể cả khi chưa follow.
- Follow chính mình → 400. User không tồn tại → 404.

### 4.4 Like và comment
- `PUT /posts/{id}/like` → 204, idempotent; một user chỉ tính một like cho mỗi bài.
- `DELETE /posts/{id}/like` → 204, idempotent.
- `POST /posts/{id}/comments {text}` → 201 `{id, post_id, author, text, created_at}`; text rỗng → 400.
- `GET /posts/{id}/comments` → 200 `{items}`, cũ nhất trước.
- Bài không tồn tại → 404.

### 4.5 Feed
- `GET /feed?limit=&before=` → 200 `{items, next}`.
- Gồm bài của những người mình follow, mới nhất trước, sắp xếp theo `(created_at, id)` giảm dần.
- Mỗi item: `id, author, caption, image_url, likes, comments, liked_by_me, created_at`. `author` là username; `liked_by_me` tính riêng cho người đang xem.
- `limit` mặc định 20, tối đa 50 (lớn hơn thì cắt về 50). `limit` hoặc `before` sai định dạng → 400.
- Phân trang bằng cursor `next` (chuỗi mờ đục, `null` ở trang cuối): không lặp, không sót bài, kể cả khi nhiều bài trùng `created_at`.
- Không có bài nào: `items` là `[]`, không phải `null`.

## 5. Định dạng lỗi

```json
{"error": {"code": "not_found", "message": "post not found"}}
```

| HTTP | code |
|---|---|
| 400 | `bad_request` |
| 401 | `unauthorized` |
| 404 | `not_found` |
| 409 | `conflict` |
| 413 | `payload_too_large` |
| 503 | `unavailable` |

## 6. Yêu cầu riêng của microservice

- Gateway verify JWT rồi gắn user id vào gRPC metadata `x-user-id`; service không nhận user id từ body.
- `social` gọi `auth.UserExists` khi follow và `post.PostExists` khi like/comment; tên tác giả comment lấy từ `auth.GetUsers`. `post` cũng lấy tên tác giả từ `auth` khi tạo bài, nên đăng ảnh cần `auth`.
- Upload ảnh đi qua gRPC client-streaming từ gateway sang `post`, giới hạn 5 MiB.
- **Feed `FEED_MODE=sync`**: feed gọi đồng bộ các service khác. Nếu một service phụ thuộc chết → `GET /feed` trả 503.
- **Feed `FEED_MODE=event`**: `post` và `social` phát event qua NATS; `feed` giữ read-model riêng (kèm tên tác giả, số like/comment, cờ "mình đã like") và vẫn trả 200 khi `social` hoặc `auth` chết. Dữ liệu có thể cũ.
- Lỗi publish event chỉ được log, không làm hỏng request ghi.
- Mã lỗi gRPC map sang HTTP ở gateway: `NotFound`→404, `AlreadyExists`→409, `InvalidArgument`→400, `Unauthenticated`→401, `ResourceExhausted`→413, `Unavailable`→503.

## 7. Tiêu chí chấp nhận

1. Bộ test hợp đồng `tests/contract` (chạy bằng `BASE_URL`) qua toàn bộ trên monolith.
2. Cùng bộ test qua toàn bộ trên microservice với `FEED_MODE=sync`, và với `FEED_MODE=event` (test được phép poll tối đa khoảng 2 giây để chờ feed cập nhật).
3. Test cấp store cho phân trang với nhiều bài cùng `created_at` ở monolith (Task 4) và read-model (Task 10).
4. `scripts/failure.sh` cho thấy: tắt `social` thì feed `sync` trả 503 còn feed `event` trả 200.
5. README ghi số liệu thật cho bốn điểm so sánh.

## 8. Thứ tự thực hiện

Monolith → microservice bước 1 (gRPC đồng bộ) → bước 2 (event). Chi tiết 12 task trong `docs/superpowers/plans/2026-11-21-photo-share.md`; cả 12 task đã xong, số liệu so sánh nằm ở [README](../README.md#kết-quả-so-sánh).
