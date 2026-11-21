# Photo-share: Monolith vs Microservice

Dự án học tập: build **cùng một app share photo** (REST API) hai lần, một bản monolith và một bản microservice (gRPC nội bộ), rồi so sánh hai bản.

Chức năng: đăng ký/đăng nhập, đăng ảnh, xem feed, follow, like, comment. Chỉ có API, không có GUI, không dành cho production.

## Bốn điều muốn cảm nhận khi so sánh

1. **Data ownership**: 1 DB + `JOIN` so với mỗi service một DB riêng.
2. **Giao tiếp**: gọi hàm so với gRPC đồng bộ so với event (NATS).
3. **Lỗi từng phần**: một phần chết thì phần còn lại có sống không.
4. **Chạy và deploy**: 1 container so với nhiều container.

## Trạng thái

| Phần | Trạng thái |
|---|---|
| Thiết kế (`docs/superpowers/specs/`) | xong, đã duyệt |
| Kế hoạch (`docs/superpowers/plans/`) | xong, 12 task |
| Task 0: hợp đồng API + bộ test hợp đồng | **xong** |
| Task 1–4: monolith | chưa làm |
| Task 5–8: microservice, feed bước 1 (gRPC) | chưa làm |
| Task 9–10: feed bước 2 (event qua NATS) | chưa làm |
| Task 11: số liệu so sánh | chưa làm |

Hiện chưa có server nào chạy được; mục "Chạy" của từng bản sẽ thêm khi các task tương ứng xong.

## Tài liệu

- [docs/SPEC.md](docs/SPEC.md): đặc tả chức năng, ràng buộc và tiêu chí chấp nhận.
- [docs/ARCHITEC.md](docs/ARCHITEC.md): kiến trúc của hai bản và cách so sánh.
- [docs/architecture.html](docs/architecture.html): sơ đồ tương tác (mở bằng trình duyệt), có thể bấm tắt service để xem phần nào còn chạy.
- [api/openapi.yaml](api/openapi.yaml): hợp đồng REST dùng chung cho cả hai bản.

## Cấu trúc repo

```
api/openapi.yaml          # hợp đồng REST + bảng mã lỗi
internal/apierr/          # định dạng lỗi JSON dùng chung
internal/jwtutil/         # ký và verify JWT
tests/contract/           # test e2e, chạy bằng BASE_URL
docs/superpowers/         # thiết kế và kế hoạch
monolith/                 # (Task 1-4)
microservices/            # (Task 5-10)
```

## Chạy test

Yêu cầu Go 1.22+.

```bash
# test đơn vị
go test ./internal/...

# test hợp đồng: cần một server đang chạy tại BASE_URL
BASE_URL=http://localhost:8080 go test ./tests/contract/... -v
```

Khi chưa có server, toàn bộ test hợp đồng fail với `connection refused`, đó là trạng thái đúng ở Task 0. Cùng một bộ test sẽ chạy trên cả hai bản; hai bản cùng qua bộ test là bằng chứng chúng tương đương.

## Quy ước API (tóm tắt)

- Auth: `Authorization: Bearer <jwt>`, token sống 24 giờ.
- Lỗi: `{"error":{"code":"...","message":"..."}}` với mã `bad_request`, `unauthorized`, `not_found`, `conflict`, `payload_too_large`, `unavailable`.
- Caption tối đa 500 ký tự, ảnh tối đa 5 MiB.
- Follow/unfollow/like/unlike idempotent.

Chi tiết đầy đủ nằm trong `api/openapi.yaml`.
