# mini-photoflow

App share photo nhỏ (đăng ký/đăng nhập, đăng ảnh, feed, follow, like, comment) được build **hai lần**: một bản monolith và một bản microservice. Mục đích là học và so sánh hai cách tổ chức kiến trúc trên cùng một app. Chỉ có REST API, không có GUI, không dành cho production.

Công nghệ: Go, Postgres, MinIO, Docker Compose; bản microservice thêm gRPC và NATS.

## Repo có gì

```
api/openapi.yaml          hợp đồng REST dùng chung cho cả hai bản
tests/contract/           bộ test e2e, chạy được trên cả hai bản bằng BASE_URL
internal/                 package Go dùng chung (apierr, jwtutil)
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

## Tiến độ

Kế hoạch gồm 12 task (Task 0 đến Task 11).

- Xong: Task 0, tức hợp đồng API và bộ test hợp đồng.
- Chưa làm: monolith (Task 1–4), microservice (Task 5–10), README so sánh có số liệu (Task 11).

Hiện chưa có server nào chạy được.

## Bắt đầu

Yêu cầu: Go 1.22+. Docker và Docker Compose sẽ cần khi có hai bản chạy được.

```bash
git clone https://github.com/SyDuc7421/mini-photoflow.git
cd mini-photoflow

# test đơn vị các package dùng chung
go test ./internal/...

# test hợp đồng: cần một server đang chạy tại BASE_URL
BASE_URL=http://localhost:8080 go test ./tests/contract/... -v
```

Khi chưa có server, test hợp đồng fail với `connection refused`; đó là trạng thái đúng ở thời điểm này. Cùng một bộ test sẽ chạy trên monolith và microservice bằng cách đổi `BASE_URL`.

Lệnh chạy từng bản (`docker compose up`) sẽ được thêm vào đây khi bản tương ứng xong.
