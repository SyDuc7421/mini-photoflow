# mini-photoflow

App share photo nhỏ (đăng ký/đăng nhập, đăng ảnh, feed, follow, like, comment) được build **hai lần**: một bản monolith và một bản microservice. Mục đích là học và so sánh hai cách tổ chức kiến trúc trên cùng một app. Chỉ có REST API, không có GUI, không dành cho production.

Công nghệ: Go, Postgres, MinIO, Docker Compose; bản microservice thêm gRPC và NATS.

## Repo có gì

```
api/openapi.yaml          hợp đồng REST dùng chung cho cả hai bản
tests/contract/           bộ test e2e, chạy được trên cả hai bản bằng BASE_URL
internal/                 package Go dùng chung (apierr, jwtutil, cursor, events, grpcutil, dbutil)
scripts/                  sinh proto, failure.sh (tắt một service), bench-feed.sh (đo độ trễ feed)
docs/                     SPEC, ARCHITEC, sơ đồ HTML, thiết kế và kế hoạch
monolith/                 bản monolith
microservices/            bản microservice
```

## Đọc gì ở đâu

| Muốn biết | Đọc |
|---|---|
| App phải làm gì, các quy tắc và tiêu chí chấp nhận | [docs/SPEC.md](docs/SPEC.md) |
| Hai bản được tổ chức thế nào và so sánh ra sao | [docs/ARCHITEC.md](docs/ARCHITEC.md) |
| Nhìn kiến trúc bằng sơ đồ (mở trong trình duyệt) | [docs/architecture.html](docs/architecture.html) |
| Danh sách route, request và response | [api/openapi.yaml](api/openapi.yaml) |
| Lý do thiết kế và kế hoạch triển khai | `docs/superpowers/specs/`, `docs/superpowers/plans/` |

## Trạng thái

Xong cả 12 task (Task 0 đến Task 11). Bộ test hợp đồng (20 test) pass trên monolith và trên microservice ở cả hai chế độ feed (`sync`, `event`).

## Bắt đầu

Yêu cầu: Go 1.22+ và Docker Compose.

```bash
git clone https://github.com/SyDuc7421/mini-photoflow.git
cd mini-photoflow

# test đơn vị các package dùng chung
go test ./internal/...

# monolith (app :8080, MinIO :9000)
docker compose -f monolith/docker-compose.yml up -d --build
docker compose -f monolith/docker-compose.yml down        # dừng trước khi chạy bản microservice: trùng cổng 8080/9000

# microservice, feed bước 1 (FEED_MODE=sync, mặc định)
docker compose -f microservices/docker-compose.yml up -d --build
# hoặc feed bước 2: xóa read-model cũ rồi chạy lại ở chế độ event
docker compose -f microservices/docker-compose.yml down -v
FEED_MODE=event docker compose -f microservices/docker-compose.yml up -d --build

# test hợp đồng: chạy trên bản đang lên, -count=1 để không dùng kết quả cache
BASE_URL=http://localhost:8080 go test -count=1 ./tests/contract/... -v

# tắt một service rồi xem login/post/feed còn chạy không (chỉ cho bản microservice)
./scripts/failure.sh social      # hoặc auth, post, feed

# đo độ trễ GET /feed trên bản đang lên
./scripts/bench-feed.sh 200
```

Postgres của microservice chạy 4 database (`auth`, `post`, `social`, `feed`), mỗi cái một role riêng. Script khởi tạo `microservices/initdb/init.sql` chỉ chạy lần đầu; sau khi sửa nó, hoặc khi đổi `FEED_MODE` giữa `sync` và `event`, chạy `docker compose -f microservices/docker-compose.yml down -v` (xem "Giới hạn" bên dưới).

Test cần database (tự bỏ qua nếu thiếu biến môi trường):

```bash
# monolith: test phân trang cùng created_at ở cấp store (compose của monolith mở 127.0.0.1:55432)
TEST_DATABASE_URL=postgres://photo:photo@localhost:55432/photo go test ./monolith/feed/...
# microservice: read-model của feed (compose mở 127.0.0.1:55433)
TEST_DATABASE_URL=postgres://feed:feed@localhost:55433/feed go test ./microservices/feed/...
# event qua NATS thật: compose không mở cổng NATS, nên tự chạy một broker
docker run --rm -d --name nats-test -p 4222:4222 nats:2.10-alpine
TEST_NATS_URL=nats://localhost:4222 go test ./internal/events/...
docker stop nats-test
```

### Sinh code gRPC

Chỉ cần Go; `buf` và plugin protoc được tải tự động.

```bash
./scripts/gen-proto.sh   # proto ở microservices/proto, code sinh ra ở microservices/pb
```

## Kết quả so sánh

Số liệu đo ngày 2026-11-22 trên một máy macOS (Docker qua OrbStack), mọi thứ chạy trên localhost, một lần đo mỗi cấu hình. Đây là chỉ báo thô để so sánh tương đối, không phải benchmark: không có độ trễ mạng thật, dữ liệu rất nhỏ.

### 1. Data ownership

| | Monolith | Microservice `sync` | Microservice `event` |
|---|---|---|---|
| Database | 1 DB, khóa ngoại giữa mọi bảng | 3 database (auth, post, social) trên cùng một Postgres, mỗi database một role, không có khóa ngoại xuyên service | thêm database `feed` chứa bản sao (posts, follows, likes, comments) |
| Feed | một truy vấn `JOIN` | ghép từ 3 service bằng gRPC | đọc bảng riêng của `feed` |
| Code Go (không tính test, code sinh ra, `internal/`) | 880 dòng | 1736 dòng, thêm 203 dòng proto và 3457 dòng code sinh ra | như `sync` |
| Riêng phần feed | 167 dòng (`feed.go`) | 427 dòng cho service `feed` (cả hai chế độ, gồm read-model và schema) | |

`internal/` dùng chung: monolith chỉ dùng `apierr` và `jwtutil` (58 dòng), microservice dùng cả `internal/` (266 dòng, gồm `events`, `grpcutil`, `cursor`, `dbutil`).

Nhận xét:
- Khóa ngoại làm monolith đúng "miễn phí": token của user đã bị xóa làm follow/like/comment vi phạm khóa ngoại và monolith trả 401. Ở microservice, `social` phải tự hỏi `auth` và `post`; chỗ nào bỏ qua thì dữ liệu mồ côi lọt vào (like của user không còn tồn tại vẫn ghi được, vì cố ý không gọi `auth` để like chạy được lúc `auth` chết).
- Sao chép dữ liệu có giá: tên tác giả được chép vào `post` lúc tạo bài, nên **đăng ảnh phải gọi `auth`** (tắt `auth` thì đăng ảnh 503, ở cả hai chế độ feed). Read-model chép thêm bài, follow, like, comment.
- URL ảnh ký sẵn hết hạn sau 1 giờ nên read-model không lưu URL mà lưu `image_key`; `feed` phải có thông tin ký của MinIO. Ở monolith không có vấn đề này.

### 2. Giao tiếp

Độ trễ `GET /feed?limit=20` (50 bài, 5 tác giả, 200 request tuần tự, đo hai lượt):

| | p50 | p95 |
|---|---|---|
| Monolith | 0,8 ms | 1,2 đến 2,0 ms |
| Microservice `sync` | 1,4 ms | 2,0 đến 2,2 ms |
| Microservice `event` | 0,9 ms | 1,4 đến 1,6 ms |

- Một lần xem feed là 1 truy vấn SQL (monolith); với microservice thì ngoài hop gateway tới `feed` còn có 4 lời gọi gRPC từ `feed` tới 3 service (`sync`: `ListFollowing`, `ListPostsByAuthors`, rồi `CountsForPosts` song song với `GetUsers`), hoặc `feed` không gọi ai (`event`).
- `sync` chậm hơn khoảng 0,6 ms vì thêm các hop, và đây là localhost; trên mạng thật mỗi hop sẽ đắt hơn nhiều. `event` đưa độ trễ đọc về gần monolith.
- Cái giá của `event` là độ trễ cập nhật và độ tin cậy: từ lúc like đến lúc thấy trong feed là khoảng 1,2 ms ở trung vị, tối đa 2,4 ms (đo tay 20 lần: gửi like rồi gọi lặp `GET /feed` đến khi thấy, nên đã gồm thời gian HTTP và một lần poll), nhưng NATS pub/sub chỉ giao tối đa một lần. Event phát lúc `feed` tắt thì mất vĩnh viễn, và không có backfill.
- Một số lỗi chỉ xuất hiện ở bản microservice và chỉ được phát hiện nhờ review: giới hạn nhận 4 MB mặc định của gRPC (một bài có đủ comment làm `GET comments` trả 413; đã sửa), gRPC giữ IP container cũ tới 30 giây sau khi service được tạo lại (đã sửa), hai thao tác like/unlike nhanh có thể phát event sai thứ tự (đã sửa bằng khóa theo từng cạnh), và event của hai service đến `feed` không có thứ tự chung.

### 3. Khi một phần chết

Mã trạng thái của login / đăng ảnh / feed khi tắt một service (`scripts/failure.sh` cho microservice; hàng monolith đo tay bằng `docker compose -f monolith/docker-compose.yml stop app` rồi `start app`; `000` là không kết nối được):

| Tắt | Monolith | Microservice `sync` | Microservice `event` |
|---|---|---|---|
| (không tắt) | 200 / 201 / 200 | 200 / 201 / 200 | 200 / 201 / 200 |
| app (monolith) | 000 / 000 / 000 | | |
| `auth` | | 503 / 503 / 503 | 503 / 503 / **200** |
| `post` | | 200 / 503 / 503 | 200 / 503 / **200** |
| `social` | | 200 / 201 / 503 | 200 / 201 / **200** |
| `feed` | | 200 / 201 / 503 | 200 / 201 / 503 |

- Monolith: một process, chết là chết hết; chỉ tắt Postgres thì trả 503. Bật lại app thì chạy lại sau khoảng 1 giây.
- `sync`: feed cần cả bốn service, nên tắt bất kỳ service nào cũng làm feed 503 (đúng thiết kế).
- `event`: feed vẫn trả 200 khi `auth`, `post` hay `social` chết, kể cả khi cả ba cùng tắt; ảnh vẫn tải được. Dữ liệu có thể cũ. Chỉ khi chính `feed` chết thì feed mới 503.
- Ngoài bảng: tắt `social` thì follow/like/comment trả 503 ở cả hai chế độ; tắt `auth` thì follow/unfollow, thêm comment và xem comment (khi bài có comment) lỗi, nhưng like vẫn chạy.
- Login và feed tự hồi phục trong 0 đến 2 giây sau khi bật lại service (script chỉ chờ hai thứ này).
- Bảng "ai cần ai" của thiết kế ban đầu sai ở hai chỗ, đã sửa trong [docs/ARCHITEC.md](docs/ARCHITEC.md): đăng ảnh cần `auth`, và comment (thêm, xem) cũng cần `auth`.
- Cả hai bản đều còn điểm chết chung: Postgres (4 database trên một instance ở microservice) và gateway. Hàng monolith ở trên so sánh việc tắt cả tiến trình với việc tắt một service, nên không đối xứng.

### 4. Vận hành

| | Monolith | Microservice |
|---|---|---|
| Container | 3 (app, postgres, minio) | 8 (gateway, auth, post, social, feed, postgres, minio, nats) |
| Image Go | 1 image, 25 MB | 5 image, tổng khoảng 154 MB (24 đến 35 MB mỗi cái) |
| Build nguội (`build --no-cache`) | 31 giây | 43 giây (một Dockerfile cho cả 5 service) |
| Từ `up -d` đến request đầu tiên thành công | 3,3 giây | 5,3 giây |
| RAM lúc rảnh, cả stack | khoảng 156 MiB | khoảng 215 MiB |
| Bộ test hợp đồng (20 test) | 4,4 giây | 4,3 đến 4,6 giây |

- Phần lớn RAM là Postgres và MinIO; mỗi service Go 6 đến 10 MiB (riêng `post` 25 đến 35 MiB). Phần thêm của microservice (khoảng 60 MiB) chủ yếu là 5 tiến trình Go thay cho 1, cộng NATS khoảng 10 MiB.
- Số thứ phải cấu hình tăng nhiều: 8 container, 4 role Postgres, địa chỉ gRPC, NATS, cách ký URL ảnh. Hầu hết lỗi vận hành (DNS, giới hạn kích thước, thứ tự event) nằm ở những chỗ này, không nằm ở logic nghiệp vụ.

### Tóm lại

Với app nhỏ này, monolith đơn giản hơn ở mọi trục ngoại trừ một: khi một phần chết, nó chết hết. Microservice `sync` không chịu lỗi tốt hơn monolith cho feed (feed vẫn phụ thuộc mọi thứ) nhưng cô lập login và đăng ảnh khỏi lỗi của `social` và `feed` (không khỏi `auth`); gateway và Postgres vẫn là điểm chết chung như monolith. Chỉ khi thêm read-model và event (`event`) thì feed mới sống được qua sự cố, và bù lại ta nhận độ trễ cập nhật, mất event và dữ liệu bị sao chép.

## Giới hạn

- Read-model của `feed` (`event`) chỉ chứa event nhận được lúc chạy `FEED_MODE=event`; không backfill. Khi đổi chế độ, chạy `docker compose down -v`.
- NATS là pub/sub thường: giao tối đa một lần, không phát lại.
- Chỉ có API, không có GUI; không dành cho production; mật khẩu và khóa trong compose là giá trị dev.
