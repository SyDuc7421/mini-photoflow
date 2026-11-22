# mini-photoflow

App share photo nhỏ (đăng ký/đăng nhập, đăng ảnh, feed, follow, like, comment) được build **hai lần**: một bản monolith và một bản microservice. Mục đích là học và so sánh hai cách tổ chức kiến trúc trên cùng một app. Chỉ có REST API, không có GUI, không dành cho production.

Công nghệ: Go, Postgres, MinIO, Docker Compose; bản microservice thêm gRPC và NATS.

## Repo có gì

```
api/openapi.yaml          hợp đồng REST dùng chung cho cả hai bản
tests/contract/           bộ test e2e, chạy được trên cả hai bản bằng BASE_URL
internal/                 package Go dùng chung (apierr, jwtutil, grpcutil, dbutil)
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

- Xong: Task 0 (hợp đồng API), Task 1–4 (monolith, qua toàn bộ test hợp đồng), Task 5 (microservice: proto, gateway, auth).
- Chưa làm: microservice post/social/feed (Task 6–10), README so sánh có số liệu (Task 11).

## Bắt đầu

Yêu cầu: Go 1.22+ và Docker Compose.

```bash
git clone https://github.com/SyDuc7421/mini-photoflow.git
cd mini-photoflow

# test đơn vị các package dùng chung
go test ./internal/...

# monolith (app :8080, MinIO :9000). Dừng trước khi chạy bản kia vì trùng cổng.
docker compose -f monolith/docker-compose.yml up -d --build

# microservice (gateway :8080, MinIO :9000)
docker compose -f microservices/docker-compose.yml up -d --build

# test hợp đồng: chạy trên bản đang lên, -count=1 để không dùng kết quả cache
BASE_URL=http://localhost:8080 go test -count=1 ./tests/contract/... -v
```

Postgres của microservice chạy 4 database (`auth`, `post`, `social`, `feed`), mỗi cái một role riêng. Script khởi tạo `microservices/initdb/init.sql` chỉ chạy lần đầu; sau khi sửa nó, chạy `docker compose -f microservices/docker-compose.yml down -v`.

### Sinh code gRPC

Chỉ cần Go; `buf` và plugin protoc được tải tự động.

```bash
./scripts/gen-proto.sh   # proto ở microservices/proto, code sinh ra ở microservices/pb
```
