# nexora-migrate 使用指南

[English](guide.en.md) · [فارسی](guide.fa.md) · [中文](guide.zh.md) · [Русский](guide.ru.md) · [Tiếng Việt](guide.vi.md)

nexora-migrate 把另一个 VPN 面板的用户和设置复制到 Nexora：用户凭据、剩余流量、
到期时间、入站、出站、路由、DNS 和管理员。只要旧面板的链接格式允许，客户就能继续
使用他们现有的订阅链接。

## 1. 下载

| 系统 | 文件 |
| --- | --- |
| Windows (Intel/AMD) | [nexora-migrate-windows-amd64.zip](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-windows-amd64.zip) |
| Windows (ARM) | [nexora-migrate-windows-arm64.zip](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-windows-arm64.zip) |
| Linux (Intel/AMD) | [nexora-migrate-linux-amd64.tar.gz](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-linux-amd64.tar.gz) |
| Linux (ARM) | [nexora-migrate-linux-arm64.tar.gz](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-linux-arm64.tar.gz) |
| macOS (Apple 芯片) | [nexora-migrate-darwin-arm64.tar.gz](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-darwin-arm64.tar.gz) |

这些链接始终指向最新版本。如需校验下载的文件，请与
[SHA256SUMS](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/SHA256SUMS) 对比。

## 2. 运行

**Windows。** 解压后双击 `nexora-migrate.exe`。如果被 SmartScreen 拦截，
请选择 *更多信息 → 仍要运行*。

**Linux**（例如在旧面板所在的服务器上）：

```sh
curl -LO https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-linux-amd64.tar.gz
tar -xzf nexora-migrate-linux-amd64.tar.gz
./nexora-migrate
```

**macOS。** 解压文件。macOS 会拦截从网上下载的程序，请先放行一次再运行：

```sh
tar -xzf nexora-migrate-darwin-arm64.tar.gz
xattr -d com.apple.quarantine nexora-migrate
./nexora-migrate
```

程序会打印一个类似下面的链接，请在浏览器中打开：

```
http://127.0.0.1:8787/?key=wiurPXyaBxxkRVrwFBA0XLc9RIz1ZwCR
```

链接中的密钥只能使用一次，没有它任何人都无法打开页面。页面提供 English、فارسی、
中文、Русский 和 Tiếng Việt，可在页面顶部切换语言。

**在服务器上运行。** 向导只监听 `127.0.0.1`，不要为它开放端口。请通过 SSH 隧道
连接，然后在自己的电脑上打开打印出的链接：

```sh
ssh -L 8787:127.0.0.1:8787 root@your-server
```

常用参数：`-listen 127.0.0.1:9000` 更换端口，`-no-browser` 只打印链接，
`-version` 显示版本号。

## 3. 五个步骤

1. **源面板。** 选择旧面板，然后提供它的数据库文件或正在运行的面板地址。
2. **检查与选择。** 所有转换后的条目按组列出。可以全选、选择一个组或单独勾选某几行。
   黄色行会带着改动迁移，说明中写明改了什么。红色行无法迁移，但仍会列出，方便你知道。
3. **连接 Nexora。** 填写 Nexora 地址，用用户名和密码或 API 令牌登录。如果账号开启了
   两步登录，还要输入验证器应用中的当前代码。
4. **预览。** 此页准确显示将要创建的内容、许可证剩余额度以及已经存在的名称。此时
   还没有写入任何内容。
5. **迁移。** 实时显示进度。结束后可以保存报告，底部按钮会关闭程序。

## 4. 支持的面板

| 面板 | 读取方式 | 迁移内容 |
| --- | --- | --- |
| **s-ui** | `s-ui.db`，或正在运行的面板 | 客户端、入站、出站、端点、路由、DNS、管理员 |
| **3x-ui** | `x-ui.db`，或正在运行的面板 | 客户端、入站（Xray 转换为 sing-box）、WireGuard（作为端点）、出站、路由、DNS、管理员 |
| **x-ui**（vaxilu 原版及其分支，包括 alireza0） | `x-ui.db`，或正在运行的面板 | 与 3x-ui 相同 |
| **Marzban** | 正在运行的面板 | 用户、管理员，以及 Xray 配置：入站、出站、路由、DNS |
| **PasarGuard** | 正在运行的面板 | 与 Marzban 相同；每个 Xray 核心成为单独的模板 |
| **Hiddify** | 正在运行的面板 | 用户和管理员 |
| **Marzneshin** | 正在运行的面板 | 用户和管理员 |
| **Remnawave** | 正在运行的面板 | 用户 |

**数据库文件。** s-ui、3x-ui 和 x-ui 把所有数据保存在一个 SQLite 文件中。从服务器
复制该文件并拖到页面上即可，旧面板无需运行：

```sh
scp root@your-server:/etc/x-ui/x-ui.db .           # 3x-ui 和 x-ui
scp root@your-server:/usr/local/s-ui/db/s-ui.db .  # s-ui
```

如果向导就运行在那台服务器上，也可以直接输入文件路径。

**正在运行的面板。** 这三个面板也可以直接从运行中的面板读取。向导会登录、下载面板
自带备份按钮提供的备份、读取后立即删除。请按浏览器中打开的样子粘贴地址，包括面板的
秘密路径。3x-ui 还支持 API 令牌和两步验证码，s-ui 支持 API 密钥。vaxilu 原版 x-ui
没有备份接口，请使用文件。

**3x-ui 还是 x-ui？** 两者的文件名都是 `x-ui.db`，但路由和出站保存在不同位置。
MHSanaei 的 3x-ui 请选择 **3x-ui**；vaxilu 原版 x-ui 或其分支请选择 **x-ui**。选错时
用户仍会迁移，但路由和出站为空，向导会提示你。

**其他面板** 使用 MySQL 或 PostgreSQL，因此通过它们自己的 API 读取。请提供面板地址和
一个 sudo 管理员的登录信息（面板支持时也可以用 API 密钥）。Hiddify 还需要秘密代理
路径（管理地址中域名与 `/admin` 之间的部分），并以某个管理员的 UUID 作为 API 密钥。

## 5. 开始迁移之前

**订阅链接。** Nexora 也会用旧令牌响应 `/sub/{token}`。对于 **s-ui、3x-ui、x-ui、
Marzban 和 PasarGuard**，只要把旧域名指向 Nexora，现有链接就能继续使用，客户无需任何
操作。**Marzneshin、Hiddify 和 Remnawave** 的链接格式 Nexora 不提供，这些用户会得到
新链接，向导会逐个用户说明。

**每个用户一套凭据。** Nexora 为每个用户保存一个 UUID 和一个密码，而有些面板按协议
分别保存。两者不一致时，UUID 取自 VLESS/VMess，密码取自 Trojan/Shadowsocks，
被舍弃密钥的协议会写在该行上。

**迁移期间节点会暂停。** Nexora 会把每个新用户推送到它的节点。为避免成千上万次推送，
向导在迁移期间关闭节点，结束时对每个节点同步一次。即使迁移失败或被你中止，节点也会
重新开启。因流量限额而被关闭的节点不会被改动。

**入站是转换，而不是盲目复制。** Xray 和 sing-box 对设置的命名并不完全相同。VLESS
Encryption 密钥和 XHTTP 设置会被迁移，现有客户端仍能与服务器匹配。没有对应项的设置
（fallback、mux、QUIC、TCP 头部伪装）会被舍弃，并在该行注明。启用 VLESS Encryption
时，Nexora 不提供 XTLS Vision 流控。没有私钥的 REALITY 入站会生成新密钥，其客户端需要
新链接。把入站放到节点上之前，请逐一检查。

**`direct` 出站。** Nexora 会自己在每个节点上添加一个普通的 `direct` 出站。因此旧面板
中同名的普通 `direct` 出站不会重复创建，引用它的规则改用 Nexora 的出站。

**WireGuard。** WireGuard peer 会按原样迁移到对应端点上，不会成为 Nexora 用户。

**管理员密码无法迁移。** 导入的管理员会获得一个生成的密码，只在最后一页显示一次，
不会保存在任何地方，请记下来。如果你使用两步登录并且选择了管理员，预览页会要求输入
新的验证码，因为 Nexora 创建管理员时需要验证码确认。

**迁移之后。** 导入的入站、出站和规则会放在一个 Nexora 模板中。请在 Nexora 中把该
模板分配给节点，然后确认节点能连接、链接可用。

## 6. 安全

运行期间，程序掌握两个面板的管理员登录信息以及所有订阅令牌。因此：

- 它只监听 `127.0.0.1`，打印的链接带有一次性密钥；
- 其他监听地址必须同时使用 `-allow-remote`、`-tls-cert` 和 `-tls-key`，否则程序拒绝启动；
- 结束后不留下任何内容。拖到页面上的数据库在程序关闭前保存在临时文件中，从运行中的
  面板下载的备份读取后立即删除，报告只有在你点击按钮时才会保存。
