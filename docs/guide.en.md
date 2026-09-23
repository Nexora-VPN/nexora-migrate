# nexora-migrate — guide

[English](guide.en.md) · [فارسی](guide.fa.md) · [中文](guide.zh.md) · [Русский](guide.ru.md) · [Tiếng Việt](guide.vi.md)

nexora-migrate copies the users and settings of another VPN panel into Nexora:
credentials, remaining data, expiry dates, inbounds, outbounds, routing, DNS
and admins. Where the old panel's link shape allows it, customers keep the
subscription link they already have.

## 1. Download

| System | File |
| --- | --- |
| Windows (Intel/AMD) | [nexora-migrate-windows-amd64.zip](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-windows-amd64.zip) |
| Windows (ARM) | [nexora-migrate-windows-arm64.zip](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-windows-arm64.zip) |
| Linux (Intel/AMD) | [nexora-migrate-linux-amd64.tar.gz](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-linux-amd64.tar.gz) |
| Linux (ARM) | [nexora-migrate-linux-arm64.tar.gz](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-linux-arm64.tar.gz) |
| macOS (Apple silicon) | [nexora-migrate-darwin-arm64.tar.gz](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-darwin-arm64.tar.gz) |

These links always point at the latest release. To check a download, compare it
with [SHA256SUMS](https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/SHA256SUMS).

## 2. Run

**Windows.** Unzip the file and double-click `nexora-migrate.exe`. If
SmartScreen stops it, choose *More info → Run anyway*.

**Linux** (for example on the server of the old panel):

```sh
curl -LO https://github.com/nexora-vpn/nexora-migrate/releases/latest/download/nexora-migrate-linux-amd64.tar.gz
tar -xzf nexora-migrate-linux-amd64.tar.gz
./nexora-migrate
```

**macOS.** Unpack the file. macOS blocks programs downloaded from the internet,
so allow this one once, then run it:

```sh
tar -xzf nexora-migrate-darwin-arm64.tar.gz
xattr -d com.apple.quarantine nexora-migrate
./nexora-migrate
```

The program prints a link like this one. Open it in your browser:

```
http://127.0.0.1:8787/?key=wiurPXyaBxxkRVrwFBA0XLc9RIz1ZwCR
```

The key in the link works once. Without it nobody can open the page. The page
is in English, فارسی, 中文, Русский and Tiếng Việt; change the language at the
top of the page.

**Running it on a server.** The wizard only listens on `127.0.0.1`. Do not open a
port for it. Connect with an SSH tunnel and open the printed link on your own
computer:

```sh
ssh -L 8787:127.0.0.1:8787 root@your-server
```

Useful options: `-listen 127.0.0.1:9000` picks another port, `-no-browser`
only prints the link, and `-version` prints the version.

## 3. The five steps

1. **Source panel.** Choose the old panel, then give its database file or the
   address of the running panel.
2. **Review and select.** Every converted item is listed in groups. Tick
   everything, one group or single rows. A yellow row comes across with a
   change, and its note says what changed. A red row cannot come across. It is
   still listed so that you know about it.
3. **Connect Nexora.** Give the Nexora address and sign in with a username and
   password or an API token. If your account uses two-factor sign-in, also enter
   the current code from your authenticator app.
4. **Preview.** This page shows exactly what will be created, the room left in
   your licence and any names that already exist. Nothing has been written yet.
5. **Transfer.** Progress is shown live. At the end you can save a report, and
   the button at the bottom closes the program.

## 4. Supported panels

| Panel | Read from | What comes across |
| --- | --- | --- |
| **s-ui** | `s-ui.db`, or the running panel | clients, inbounds, outbounds, endpoints, routing, DNS, admins |
| **3x-ui** | `x-ui.db`, or the running panel | clients, inbounds (Xray converted to sing-box), WireGuard as endpoints, outbounds, routing, DNS, admins |
| **x-ui** (vaxilu's original and its forks, alireza0 included) | `x-ui.db`, or the running panel | the same as 3x-ui |
| **Marzban** | the running panel | users, admins, and the Xray configuration: inbounds, outbounds, routing, DNS |
| **PasarGuard** | the running panel | the same as Marzban; each Xray core becomes its own template |
| **Hiddify** | the running panel | users and admins |
| **Marzneshin** | the running panel | users and admins |
| **Remnawave** | the running panel | users |

**Database file.** s-ui, 3x-ui and x-ui keep everything in one SQLite file. Copy
it from the server and drop it on the page. The old panel does not have to be
running:

```sh
scp root@your-server:/etc/x-ui/x-ui.db .           # 3x-ui and x-ui
scp root@your-server:/usr/local/s-ui/db/s-ui.db .  # s-ui
```

If the wizard runs on that server, you can type the file's path instead.

**Running panel.** The same three panels can also be read from the running
panel. The wizard signs in, downloads the backup that the panel's own backup
button gives, reads it and deletes it. Paste the address exactly as you open it
in your browser, including the panel's secret path. 3x-ui also accepts an API
token and a two-factor code, and s-ui accepts an API key. The original x-ui by
vaxilu has no backup endpoint, so use its file.

**3x-ui or x-ui?** Both use the file name `x-ui.db`, but they keep routing and
outbounds in different places. Choose **3x-ui** for MHSanaei's 3x-ui. Choose
**x-ui** for vaxilu's original x-ui or one of its forks. With the wrong choice
the users still come across, but routing and outbounds arrive empty, and the
wizard warns you.

**The other panels** use MySQL or PostgreSQL, so they are read through their own
API. Give the panel address and a sudo admin's login (or an API key where the
panel has one). Hiddify also needs its secret proxy path (the part of the admin
URL between the domain and `/admin`) and an admin's UUID as the API key.

## 5. Before you start the transfer

**Subscription links.** Nexora also answers `/sub/{token}` with the old token.
For **s-ui, 3x-ui, x-ui, Marzban and PasarGuard**, existing links keep working
once the old domain points at Nexora, and customers change nothing.
**Marzneshin, Hiddify and Remnawave** use link shapes that Nexora does not serve,
so those users get new links. The wizard says so for each user.

**One set of credentials per user.** Nexora keeps one UUID and one password per
user. Some panels keep one per protocol. When they differ, the UUID comes from
VLESS/VMess and the password from Trojan/Shadowsocks, and the row names every
protocol whose secret was dropped.

**Nodes are paused during the transfer.** Nexora pushes every new user to its
nodes. To avoid thousands of pushes, the wizard switches the nodes off during
the transfer and syncs each node once at the end. The nodes are switched back on
even if the transfer fails or you stop it. A node that its usage limit switched
off is left alone.

**Inbounds are converted, not copied blindly.** Xray and sing-box do not name
every setting the same way. VLESS Encryption keys and XHTTP settings come
across, so existing clients keep matching the server. Settings with no
equivalent (fallbacks, mux, QUIC, TCP header obfuscation) are dropped with a
note on the row. Under VLESS Encryption, Nexora serves no XTLS Vision flow. A
REALITY inbound without a private key gets a new key, so its clients need new
links. Check each inbound before you put it on a node.

**The `direct` outbound.** Nexora adds a plain `direct` outbound to every node
itself. A plain `direct` outbound from the old panel is therefore not created
again, and rules that named it use Nexora's.

**WireGuard.** WireGuard peers come across on their endpoint as they were
written. They do not become Nexora users.

**Admin passwords cannot be moved.** Imported admins get a generated password.
It is shown once on the last page and saved nowhere, so write it down. If you
signed in with two-factor and the selection includes admins, the preview page
asks for a fresh code, because Nexora confirms admin creation with one.

**After the transfer.** Imported inbounds, outbounds and rules are grouped in a
Nexora template. Assign that template to your nodes in Nexora, then check that
the nodes connect and the links work.

## 6. Security

While it runs, the program holds the admin logins of both panels and every
subscription token. That is why:

- it listens only on `127.0.0.1`, and the printed link carries a one-time key;
- any other listen address needs `-allow-remote` together with `-tls-cert` and
  `-tls-key`, otherwise it refuses to start;
- it keeps nothing afterwards. A database you drop on the page is held in a
  temporary file until the program closes, a backup downloaded from a running
  panel is deleted as soon as it has been read, and the report is saved only
  when you press the button.
