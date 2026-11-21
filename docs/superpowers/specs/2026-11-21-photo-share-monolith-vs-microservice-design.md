# Photo-share: so sánh Monolith vs Microservice — Design

Ngày: 2026-11-21. Trạng thái: chờ người dùng duyệt (đã qua builder check).

## Ý định và lý do

Dự án học tập. Build **cùng một app share photo nhỏ hai lần** (một bản monolith, một bản microservice) để tự cảm nhận khác biệt khi phát triển theo hai kiến trúc. Không phải sản phẩm thật.

Bốn thứ muốn cảm nhận (do người dùng chọn, giữ cả bốn):
1. **Data ownership**: monolith dùng 1 DB + `JOIN`; microservice mỗi service sở hữu DB riêng.
2. **Giao tiếp giữa các phần**: gọi hàm trực tiếp vs gRPC đồng bộ vs event.
3. **Lỗi từng phần**: một phần chết thì phần còn lại sống được không.
4. **Chạy và deploy**: 1 container vs nhiều container.

## Chức năng của app

Đăng ký/đăng nhập, đăng ảnh (kèm caption), xem feed (ảnh của người mình follow, mới nhất trước), follow/unfollow, like/unlike, comment. Chỉ có API, không có GUI.

## Goals

- Hai bản có **cùng REST API ra ngoài** và hành vi giống nhau, để khác biệt chỉ nằm ở kiến trúc bên trong.
- Một bộ test hợp đồng (contract/e2e) chạy được trên cả hai bản bằng cách đổi base URL. Hai bản cùng qua bộ test là bằng chứng chúng tương đương.
- `docker compose up` là chạy được từng bản.
- Code đủ nhỏ để đọc hết trong một buổi; so sánh hai bản qua việc đọc code, chạy, và thử làm hỏng từng phần.
- Microservice thể hiện cả hai kiểu feed: sync gRPC (bước 1) và event-driven (bước 2), để đặt cạnh nhau.

## Non-goals

Production, scale thật, cloud, Kubernetes, service discovery, distributed tracing, retry/circuit breaker phức tạp, GUI, xử lý ảnh (resize/thumbnail), tìm kiếm, notification, phân quyền phức tạp, refresh token.

## Anti-goals (thành công về kỹ thuật nhưng vẫn là thất bại)

- Hai bản khác API hoặc khác hành vi, khiến so sánh vô nghĩa.
- Microservice "giả": các service dùng chung một DB, hoặc đọc bảng của nhau.
- Thêm tính năng hoặc hạ tầng làm che mất bốn điểm so sánh ở trên.

## Ràng buộc

- Ngôn ngữ: **Go** cho cả hai bản (quyết định của người dùng).
- gRPC cho giao tiếp nội bộ microservice (yêu cầu của người dùng). Không dùng HTTP nội bộ.
- Ảnh lưu ở **MinIO** chạy trong Docker.
- Chạy local bằng Docker Compose.

## Các cách đã cân nhắc (microservice)

- **A. gRPC đồng bộ thuần.** Gateway → gRPC → các service; feed gọi `post` và `social` rồi ghép. Ít hạ tầng nhất, nhưng không có event và feed hỏng khi một service chết.
- **B. gRPC + event qua NATS.** Truy vấn dùng gRPC, hành động phát event, `feed` giữ bản sao dữ liệu. Thấy rõ eventual consistency, feed sống khi `social` chết, nhưng thêm broker và consumer.
- **C. A trước, B thêm sau.** (**Đã chọn.**) Build A hoàn chỉnh và chạy được, rồi nâng riêng luồng feed lên event, giữ cả hai để so sánh. Lý do: thấy được cả hai kiểu trên cùng một codebase, và có bản chạy sớm.

Monolith chỉ có một cách hợp lý: một Go binary, module hóa theo miền, một Postgres.

## Các quyết định đã chốt (do người dùng)

- Chọn cách C.
- Go cho cả hai bản.
- Không production; ảnh trên MinIO trong Docker; chỉ API.
- Giữ cả 4 điểm so sánh.
- gRPC thay HTTP cho nội bộ microservice.

## Các quyết định do người dùng đồng ý đề xuất ban đầu

- Gateway REST đặt ngoài microservice (REST → gRPC), để hai bản cùng API.
- Một container Postgres với 4 database riêng, mỗi service có user riêng chỉ truy cập DB của mình.
- MinIO do service `post` quản lý.
- Auth bằng JWT, gateway verify, các service tin gateway.

## Thiết kế

### Cấu trúc repo

```
monolith/          # 1 Go binary + docker-compose.yml
microservices/     # gateway + auth + post + social + feed, proto/, docker-compose.yml
api/openapi.yaml   # hợp đồng REST dùng chung
tests/contract/    # test e2e, tham số hóa bằng BASE_URL
```

### REST API (cả hai bản)

*(Chi tiết cụ thể là lựa chọn của mình, chưa được người dùng duyệt riêng; người xây dựng có thể điều chỉnh nếu giữ nguyên tinh thần.)*

- `POST /register`, `POST /login` → JWT
- `POST /posts` (multipart: ảnh + caption)
- `GET /posts/{id}`
- `GET /feed?limit=&before=` (bài của người mình follow, mới nhất trước, phân trang theo con trỏ)
- `PUT /users/{id}/follow`, `DELETE /users/{id}/follow`
- `PUT /posts/{id}/like`, `DELETE /posts/{id}/like`
- `POST /posts/{id}/comments`, `GET /posts/{id}/comments`

Mỗi item của feed gồm: post, tên tác giả, URL ảnh, số like, số comment, mình đã like chưa.

### Microservice: ranh giới service

| Service | Sở hữu | Ghi chú |
|---|---|---|
| gateway | không có DB | REST → gRPC, verify JWT |
| auth | users, credentials | cấp token, trả thông tin user (có batch) |
| post | posts, ảnh trên MinIO | |
| social | follows, likes, comments | |
| feed | (bước 1) không có; (bước 2) bản sao read-model | |

Bước 1: `feed` lấy danh sách following từ `social`, bài từ `post`, đếm like/comment từ `social`, tên tác giả từ `auth`, rồi ghép.
Bước 2: `post` và `social` phát event (post tạo, follow/unfollow, like/unlike, comment); `feed` consume qua NATS và giữ bảng riêng để dựng feed không cần gọi sang service khác. Giữ cả hai cài đặt feed chạy được để so sánh (ví dụ chọn bằng cấu hình).

### Monolith

Một binary, các package theo miền (auth, post, social, feed) gọi nhau trực tiếp qua hàm; một DB; feed là một truy vấn có `JOIN`.

### Điều cần chứng minh bằng thực nghiệm (kịch bản so sánh)

- Tắt `social`: login và đăng ảnh còn chạy không? Feed thế nào ở bước 1 và bước 2?
- Like một bài: số đếm cập nhật ở đâu, trễ bao lâu (bước 2)?
- Số container, thời gian build/khởi động, số bước để thêm một field mới vào feed.

## Hành vi khi lỗi và tính nhất quán (người dùng đã chọn theo đề xuất)

- **Bước 1 (gRPC đồng bộ)**: nếu `post`, `social` hoặc `auth` chết, `GET /feed` trả lỗi 5xx rõ ràng.
- **Bước 2 (event)**: feed vẫn trả được khi `social`/`auth` chết. Để làm được, read-model lưu sẵn tên tác giả (lấy từ event khi nhận), số like/comment và cờ "mình đã like".
- **Test hợp đồng với feed bước 2**: được phép poll/retry ngắn (tối đa ~2 giây) trước khi assert. Độ trễ eventual consistency là một phần của điều cần cảm nhận, không che giấu bằng cơ chế read-your-writes.

## Các quyết định do mình (Claude) đưa ra, người dùng chấp nhận theo đề xuất

- **Lỗi và edge case**: viết một bảng mã lỗi duy nhất trong `api/openapi.yaml` trước khi code; cả hai bản theo bảng đó. Follow/unfollow/like/unlike là idempotent. Bài hoặc user không tồn tại → 404; follow chính mình → 400; thiếu/sai JWT → 401; đăng ký trùng → 409.
- **User id**: gateway verify JWT rồi gắn user id vào gRPC metadata; service không nhận user id từ body.
- **Kiểm tra user tồn tại khi follow**: `social` gọi gRPC sang `auth`. Monolith dùng FK/JOIN. Giữ khác biệt này vì nó là một điểm so sánh: tắt `auth` thì follow cũng lỗi.
- **Upload ảnh**: gateway chuyển bytes sang `post` bằng gRPC client-streaming, giới hạn 5MB; `post` ghi MinIO và trả URL presigned truy cập được từ host.
- **Hạ tầng**: monolith có Postgres riêng trong compose của nó; microservices có 1 Postgres với 4 database + user riêng. Test hợp đồng dùng username ngẫu nhiên nên không cần DB sạch. Tắt service để thử lỗi bằng tay, kèm một script nhỏ.
- **Thứ tự build**: monolith → microservice bước 1 (A) → bước 2 (B). Chọn feed bước 1/2 bằng biến môi trường.
- **Chi tiết nhỏ**: JWT hết hạn 24 giờ; bcrypt cho password; cursor feed là `created_at` + id; caption tối đa 500 ký tự.

## Việc để người xây dựng quyết

Thư viện (router, gRPC codegen, migration, NATS client), schema chi tiết, định dạng lỗi, cách cấu hình chọn feed bước 1/2, cấu trúc package chi tiết, cách seed dữ liệu test.
