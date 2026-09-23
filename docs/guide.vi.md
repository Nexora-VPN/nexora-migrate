# Hướng dẫn nexora-migrate

[English](guide.en.md) · [فارسی](guide.fa.md) · [中文](guide.zh.md) · [Русский](guide.ru.md) · [Tiếng Việt](guide.vi.md)

nexora-migrate sao chép người dùng và cấu hình của một bảng điều khiển VPN khác
sang Nexora: thông tin đăng nhập, dung lượng còn lại, ngày hết hạn, inbound,
outbound, định tuyến, DNS và quản trị viên. Khi định dạng liên kết của bảng cũ cho
phép, khách hàng vẫn giữ được liên kết đăng ký hiện có.

## 1. Tải về

| Hệ điều hành | Tệp |
| --- | --- |
| Windows (Intel/AMD) | [nexora-migrate-windows-amd64.zip](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-windows-amd64.zip) |
| Windows (ARM) | [nexora-migrate-windows-arm64.zip](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-windows-arm64.zip) |
| Linux (Intel/AMD) | [nexora-migrate-linux-amd64.tar.gz](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-linux-amd64.tar.gz) |
| Linux (ARM) | [nexora-migrate-linux-arm64.tar.gz](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-linux-arm64.tar.gz) |
| macOS (Apple silicon) | [nexora-migrate-darwin-arm64.tar.gz](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-darwin-arm64.tar.gz) |

Các liên kết này luôn trỏ tới phiên bản mới nhất. Để kiểm tra tệp đã tải, hãy so
sánh với [SHA256SUMS](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/SHA256SUMS).

## 2. Chạy

**Windows.** Giải nén rồi nhấp đúp vào `nexora-migrate.exe`. Nếu SmartScreen chặn,
chọn *More info → Run anyway*.

**Linux** (ví dụ trên máy chủ của bảng cũ):

```sh
curl -LO https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-linux-amd64.tar.gz
tar -xzf nexora-migrate-linux-amd64.tar.gz
./nexora-migrate
```

**macOS.** Giải nén tệp. macOS chặn các chương trình tải từ internet, vì vậy hãy
cho phép chương trình này một lần rồi chạy:

```sh
tar -xzf nexora-migrate-darwin-arm64.tar.gz
xattr -d com.apple.quarantine nexora-migrate
./nexora-migrate
```

Chương trình in ra một liên kết như sau. Hãy mở nó trong trình duyệt:

```
http://127.0.0.1:8787/?key=wiurPXyaBxxkRVrwFBA0XLc9RIz1ZwCR
```

Khóa trong liên kết chỉ dùng được một lần; không có nó thì không ai mở được trang.
Trang có English, فارسی, 中文, Русский và Tiếng Việt; đổi ngôn ngữ ở đầu trang.

**Chạy trên máy chủ.** Trình hướng dẫn chỉ lắng nghe trên `127.0.0.1`. Đừng mở cổng
cho nó; hãy kết nối qua đường hầm SSH và mở liên kết đã in trên máy tính của bạn:

```sh
ssh -L 8787:127.0.0.1:8787 root@your-server
```

Tùy chọn hữu ích: `-listen 127.0.0.1:9000` đổi cổng, `-no-browser` chỉ in liên
kết, `-version` hiển thị phiên bản.

## 3. Năm bước

1. **Bảng nguồn.** Chọn bảng cũ, sau đó cung cấp tệp cơ sở dữ liệu của nó hoặc địa
   chỉ của bảng đang chạy.
2. **Xem lại và chọn.** Mọi mục đã chuyển đổi được liệt kê theo nhóm. Bạn có thể
   chọn tất cả, một nhóm hoặc từng dòng. Dòng màu vàng được chuyển kèm một thay
   đổi, và ghi chú cho biết đã đổi gì. Dòng màu đỏ không thể chuyển, nhưng vẫn được
   liệt kê để bạn biết.
3. **Kết nối Nexora.** Nhập địa chỉ Nexora và đăng nhập bằng tên người dùng và mật
   khẩu hoặc token API. Nếu tài khoản bật đăng nhập hai bước, hãy nhập thêm mã hiện
   tại từ ứng dụng xác thực.
4. **Xem trước.** Trang này cho biết chính xác những gì sẽ được tạo, dung lượng còn
   lại của giấy phép và những tên đã tồn tại. Chưa có gì được ghi.
5. **Chuyển.** Tiến trình được hiển thị trực tiếp. Khi xong, bạn có thể lưu báo
   cáo, và nút ở cuối trang sẽ đóng chương trình.

## 4. Các bảng được hỗ trợ

| Bảng | Đọc từ | Những gì được chuyển |
| --- | --- | --- |
| **s-ui** | `s-ui.db` hoặc bảng đang chạy | client, inbound, outbound, endpoint, định tuyến, DNS, quản trị viên |
| **3x-ui** | `x-ui.db` hoặc bảng đang chạy | client, inbound (Xray chuyển sang sing-box), WireGuard dưới dạng endpoint, outbound, định tuyến, DNS, quản trị viên |
| **x-ui** (bản gốc của vaxilu và các fork, gồm cả alireza0) | `x-ui.db` hoặc bảng đang chạy | giống 3x-ui |
| **Marzban** | bảng đang chạy | người dùng, quản trị viên và cấu hình Xray: inbound, outbound, định tuyến, DNS |
| **PasarGuard** | bảng đang chạy | giống Marzban; mỗi lõi Xray trở thành một mẫu riêng |
| **Hiddify** | bảng đang chạy | người dùng và quản trị viên |
| **Marzneshin** | bảng đang chạy | người dùng và quản trị viên |
| **Remnawave** | bảng đang chạy | người dùng |

**Tệp cơ sở dữ liệu.** s-ui, 3x-ui và x-ui lưu mọi thứ trong một tệp SQLite. Hãy sao
chép tệp đó từ máy chủ và kéo thả vào trang. Bảng cũ không cần đang chạy:

```sh
scp root@your-server:/etc/x-ui/x-ui.db .           # 3x-ui và x-ui
scp root@your-server:/usr/local/s-ui/db/s-ui.db .  # s-ui
```

Nếu trình hướng dẫn chạy ngay trên máy chủ đó, bạn có thể nhập đường dẫn tệp.

**Bảng đang chạy.** Ba bảng này cũng có thể được đọc trực tiếp từ bảng đang chạy.
Trình hướng dẫn đăng nhập, tải bản sao lưu giống như nút sao lưu của chính bảng đó,
đọc rồi xóa ngay. Hãy dán địa chỉ đúng như khi bạn mở trong trình duyệt, kể cả
đường dẫn bí mật của bảng. 3x-ui còn nhận token API và mã hai bước, s-ui nhận khóa
API. Bản x-ui gốc của vaxilu không có điểm cuối sao lưu, vì vậy hãy dùng tệp.

**3x-ui hay x-ui?** Cả hai đều dùng tên tệp `x-ui.db` nhưng lưu định tuyến và
outbound ở những chỗ khác nhau. Chọn **3x-ui** cho 3x-ui của MHSanaei; chọn **x-ui**
cho x-ui gốc của vaxilu hoặc các fork của nó. Nếu chọn sai, người dùng vẫn được
chuyển nhưng định tuyến và outbound sẽ trống, và trình hướng dẫn sẽ cảnh báo.

**Các bảng còn lại** dùng MySQL hoặc PostgreSQL nên được đọc qua API của chính
chúng. Hãy cung cấp địa chỉ bảng và thông tin đăng nhập của một quản trị viên sudo
(hoặc khóa API nếu bảng hỗ trợ). Hiddify còn cần đường dẫn proxy bí mật (phần của
địa chỉ quản trị nằm giữa tên miền và `/admin`) và UUID của một quản trị viên làm
khóa API.

## 5. Trước khi bắt đầu chuyển

**Liên kết đăng ký.** Nexora cũng trả lời `/sub/{token}` bằng token cũ. Với
**s-ui, 3x-ui, x-ui, Marzban và PasarGuard**, liên kết hiện có tiếp tục hoạt động
khi tên miền cũ trỏ về Nexora, và khách hàng không cần làm gì. **Marzneshin,
Hiddify và Remnawave** dùng định dạng liên kết mà Nexora không phục vụ, nên người
dùng của chúng sẽ nhận liên kết mới. Trình hướng dẫn nêu rõ điều này cho từng người
dùng.

**Mỗi người dùng một bộ thông tin đăng nhập.** Nexora lưu một UUID và một mật khẩu
cho mỗi người dùng, còn một số bảng lưu riêng cho từng giao thức. Khi chúng khác
nhau, UUID lấy từ VLESS/VMess, mật khẩu lấy từ Trojan/Shadowsocks, và dòng đó ghi
tên mọi giao thức có khóa bị bỏ.

**Các node tạm dừng trong khi chuyển.** Nexora đẩy mỗi người dùng mới tới các node
của nó. Để tránh hàng nghìn lần đẩy, trình hướng dẫn tắt các node trong lúc chuyển
và đồng bộ mỗi node một lần ở cuối. Các node được bật lại kể cả khi quá trình
chuyển thất bại hoặc bạn dừng nó. Node đã bị giới hạn dung lượng tắt sẽ được giữ
nguyên.

**Inbound được chuyển đổi, không sao chép máy móc.** Xray và sing-box không đặt tên
mọi thiết lập giống nhau. Khóa VLESS Encryption và thiết lập XHTTP được chuyển sang,
nên các client hiện có vẫn khớp với máy chủ. Thiết lập không có tương đương
(fallback, mux, QUIC, làm rối header TCP) bị bỏ kèm ghi chú trên dòng. Khi dùng VLESS
Encryption, Nexora không phục vụ flow XTLS Vision. Inbound REALITY không có khóa
riêng sẽ nhận khóa mới, nên client của nó cần liên kết mới. Hãy kiểm tra từng
inbound trước khi đặt nó lên node.

**Outbound `direct`.** Nexora tự thêm một outbound `direct` đơn giản vào mọi node.
Vì vậy outbound `direct` đơn giản từ bảng cũ không được tạo lại, và các quy tắc trỏ
tới nó sẽ dùng outbound của Nexora.

**WireGuard.** Các peer WireGuard được chuyển nguyên trạng lên endpoint của chúng và
không trở thành người dùng Nexora.

**Mật khẩu quản trị viên không thể chuyển.** Quản trị viên được nhập sẽ nhận một mật
khẩu được tạo, chỉ hiển thị một lần ở trang cuối và không được lưu ở đâu cả, vì vậy
hãy ghi lại. Nếu bạn đăng nhập bằng xác thực hai bước và có chọn quản trị viên,
trang xem trước sẽ yêu cầu một mã mới, vì Nexora xác nhận việc tạo quản trị viên
bằng mã.

**Sau khi chuyển.** Inbound, outbound và quy tắc đã nhập được gom vào một mẫu
Nexora. Hãy gán mẫu đó cho các node trong Nexora, rồi kiểm tra rằng node kết nối
được và liên kết hoạt động.

## 6. Bảo mật

Trong lúc chạy, chương trình giữ thông tin đăng nhập quản trị của cả hai bảng và
mọi token đăng ký. Vì vậy:

- nó chỉ lắng nghe trên `127.0.0.1`, và liên kết được in có khóa dùng một lần;
- mọi địa chỉ lắng nghe khác đều cần `-allow-remote` cùng với `-tls-cert` và
  `-tls-key`, nếu không chương trình sẽ không khởi động;
- sau khi xong không để lại gì. Cơ sở dữ liệu bạn kéo vào trang được giữ trong tệp
  tạm cho đến khi chương trình đóng, bản sao lưu tải từ bảng đang chạy bị xóa ngay
  sau khi đọc, và báo cáo chỉ được lưu khi bạn nhấn nút.
