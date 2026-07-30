<div align="center">

# OpenFrp

---

在任何地方存取你家裡的服務。一個帶網頁介面的 OpenWrt 套件，管理隧道、網域與 HTTPS 憑證。

[English](README.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [日本語](README.ja.md)

![Version](https://img.shields.io/badge/VERSION-v1.1.0-8A2BE2?style=for-the-badge&labelColor=444)
![OpenWrt](https://img.shields.io/badge/OPENWRT-21.02%2B-00B5E2?style=for-the-badge&labelColor=444)
![Arch](https://img.shields.io/badge/ARCH-x86__64%20%7C%20ARM64-000000?style=for-the-badge&labelColor=444)
![Licence](https://img.shields.io/badge/LICENCE-MIT-F5A623?style=for-the-badge&labelColor=444)

</div>

## 這東西是做什麼的

你的 NAS、家庭伺服器、攝影機、自己架的網站——外面都連不進來，因為家用路由器沒有公網 IP。

OpenFrp 用一台便宜的 VPS 當大門。訪客的流量先到 VPS，再透過路由器維持著的隧道送到你內網的那台機器。

```
訪客 ──▶ 你的 VPS(公網 IP) ──隧道──▶ 你的路由器 ──▶ NAS / 伺服器 / 攝影機
```

你需要兩樣東西：

- **一台跑 OpenWrt 的路由器**，套件與網頁介面裝在這上面。
- **一台有公網 IP 的 VPS**。任何供應商、任何規格都行，最便宜的就夠用。安裝時需要用一次 SSH。

網域是選用的。沒有網域就用 `IP:連接埠` 存取；有網域就能用 `nas.example.com` 加上真正的 HTTPS。

## 安裝

在路由器上執行一條指令：

```bash
wget -O - https://raw.githubusercontent.com/zoefix/openfrp/main/scripts/install.sh | sh
```

腳本會判斷路由器的架構、挑選對應的版本、用發佈時公佈的雜湊值驗證下載、先執行一次確認這台機器跑得動，然後才安裝。缺少的相依套件會用 `apk` 或 `opkg`（看你系統用哪個）補上。

升級就是把同樣的指令再跑一次。移除：

```bash
wget -O /tmp/openfrp-install.sh https://raw.githubusercontent.com/zoefix/openfrp/main/scripts/install.sh
sh /tmp/openfrp-install.sh --uninstall
```

這樣會保留設定，重新安裝後隧道還在。想連設定、憑證與流量記錄一併清除就用 `--purge`。

其他參數：`--version v0.4.0` 安裝指定版本，`--lang zh-tw` 順帶安裝語言包，`OPENFRP_API=…` 在連不上 github.com 時指向鏡像。

接著開啟 **LuCI → 服務 → OpenFrp**。選單沒出現的話登出再登入一次。

## 開始設定

### 第一步：設定 VPS

進 **伺服器** 頁，按 **新增伺服器**，填入：

| | |
|---|---|
| 位址 | 你 VPS 的 IP |
| SSH 使用者 | 通常是 `root` |
| SSH 密碼 | 這時候才詢問，不勾選儲存就不會留下 |

按 **透過 SSH 部署**。這會把伺服端裝到 VPS 上：自動辨識發行版與 init 系統，上傳二進位檔，寫入服務，開啟防火牆，然後啟動。大約半分鐘，日誌會即時顯示。

裝完連線權杖會自動填好。**狀態** 頁應該顯示伺服器已連線。

> VPS 上已經在跑 OpenFrp？那就略過部署，直接手動填位址、連接埠與權杖。

### 第二步：新增一條隧道

進 **隧道** 頁按 **新增**，選類型：

| 類型 | 用來做什麼 | 存取方式 |
|---|---|---|
| **HTTP** | 網站、NAS 面板，凡是用瀏覽器開啟的 | `https://nas.example.com` |
| **TCP** | SSH、資料庫、Minecraft、遠端桌面 | `你的VPS位址:連接埠` |
| **UDP** | 遊戲伺服器、WireGuard、DNS | `你的VPS位址:連接埠` |

**HTTP** 隧道要填：

- **內網位址 / 連接埠**——服務實際跑在哪，例如 `192.168.1.50` 與 `5000`。
- **網域**——訪客使用的名稱，例如 `nas.example.com`。
- **啟用 HTTPS**——同時在 443 連接埠提供服務。

**TCP** 與 **UDP** 則是填 **遠端連接埠**：VPS 上哪個連接埠轉發到你的服務。

勾選 **已啟用**，按 **儲存並套用**。

### 第三步：把網域指向 VPS

在你的 DNS 供應商那裡新增一筆 `A` 記錄：

```
nas.example.com.   A   <你的 VPS IP>
```

或使用泛解析，這樣底下所有名稱都會過來，不必一筆筆新增：

```
*.example.com.     A   <你的 VPS IP>
```

等它生效——通常一兩分鐘——然後開啟 `http://nas.example.com`，應該就會看到你的服務。

### 第四步：申請 HTTPS 憑證

進 **憑證管理** 頁，按 **申請憑證**，填入網域後送出。像 `nas.example.com` 這種一般名稱不需要別的：網域已經指向 VPS 了，VPS 自己就能回應憑證頒發機構的驗證要求。

**萬用網域**（`*.example.com`）不行——憑證頒發機構一定要走 DNS 驗證，所以要先在 **DNS** 頁填入你的 DNS 供應商 API 憑據。支援：阿里雲、DNSPod、華為雲、Cloudflare、NameSilo、PowerDNS、西部數碼。

簽發之後編輯那條隧道，把 **TLS 處理方式** 設為 *由遠端伺服器處理 HTTPS*，選擇憑證。憑證會推送到 VPS，過程中不會中斷連線，之後自動續期。

## 網域路由規則

同一台 VPS 上，任意多條隧道共用 80 與 443 連接埠，靠請求裡的網域區分。支援泛解析，`*` 代表**恰好一層**：

| 寫法 | 符合 | 不符合 |
|---|---|---|
| `aaa.com` | `aaa.com` | 任何子網域 |
| `*.aaa.com` | `www.aaa.com`、`nas.aaa.com` | `x.bb.aaa.com` |
| `*.bb.aaa.com` | `x.bb.aaa.com` | `y.x.bb.aaa.com` |

精確的名稱永遠優先於萬用比對，所以你可以把 `*.aaa.com` 指向一條隧道、`shop.aaa.com` 單獨指向另一條。

這和 DNS、HTTPS 憑證的規則一致：一張 `*.aaa.com` 憑證涵蓋 `www.aaa.com`，但不涵蓋 `x.bb.aaa.com`。保持一致代表訪客永遠不會被導向一條憑證涵蓋不了他所輸入名稱的隧道。

## 幾種常見設定

**帶網頁面板的 NAS**——HTTP 隧道，內網連接埠 `5000`，網域 `nas.example.com`，開啟 HTTPS，依第四步申請憑證。

**SSH 連回家裡的機器**——TCP 隧道，內網 `192.168.1.10:22`，遠端連接埠 `2222`。連線時用 `ssh -p 2222 使用者@你的VPS位址`。

**遊戲伺服器**——UDP（或 TCP，看遊戲），內網連接埠填伺服端監聽的那個，遠端連接埠填一樣的，這樣不必另外通知玩家。

**一台 VPS 上放多個網站**——每個網站一條 HTTP 隧道，各自填自己的網域。它們共用 443 連接埠，不必額外設定。

## 限速與流量

每條隧道都可以在編輯框裡限制：

- **下行限速**——發往訪客方向，單位 KB/s。填 `0` 表示不限。
- **上行限速**——來自訪客方向，單位 KB/s。
- **流量上限**——雙向合計，單位 MB。達到後這條隧道停止接受新連線。VPS 有月流量方案的話很有用。

**狀態** 頁顯示每條隧道的即時上下行速率，每日總量會保留 400 天。

## 保持更新

**狀態** 頁會顯示目前執行的版本。有新版本時旁邊會出現一個按鈕，按下會顯示更新了什麼，讓你確認。

更新會把所有東西一起換掉——用戶端、伺服端二進位檔、網頁介面、語言包——然後重新啟動服務。如果新版本起不來，會自動裝回舊版本，所以一個有問題的版本不會讓路由器的隧道全斷。

想從命令列升級的話，重跑一次安裝腳本是一樣的效果。

## 出問題了怎麼辦

### 伺服器顯示未連線

最常見的原因不是防火牆，而是**路由器上的透明代理**。OpenClash、Passwall、ShellCrash 都會把外送 TCP 全部重新導向，隧道的控制連線被順手抓走，送到一個轉發不了它的代理節點上。

特徵很好認：日誌裡連線成功了然後立刻中斷，而 VPS 那邊的日誌一筆記錄都沒有。

兩種改法，任一種都行：

- 在那台伺服器的編輯框裡開啟 **略過代理**。
- 或在代理設定裡，於最後那條全域規則前面加一條直連：
  ```yaml
  - "IP-CIDR,<你的VPS位址>/32,DIRECT,no-resolve"
  ```

### 網域開啟顯示「沒有隧道在提供這個名稱」

請求已經到 VPS 了，代表 DNS 是對的。要麼隧道沒啟用，要麼這個名稱不在它的**網域**清單裡。注意 `*.example.com` 不涵蓋 `a.b.example.com`。

### 申請憑證失敗

一般網域先確認它已經指向 VPS——憑證頒發機構要能連到。萬用網域則檢查 **DNS** 頁裡的憑據，那裡有個 **測試** 按鈕能驗證是否可用。

### 內網服務把所有訪客都記成了路由器

在隧道裡開啟 **用戶端 IP**，然後設定你的服務信任它。nginx 的寫法：

```nginx
listen 5000 proxy_protocol;
set_real_ip_from <你路由器的內網位址>;
real_ip_header proxy_protocol;
```

**要先設定好服務**——沒準備好接收 PROXY 協定標頭的服務，一旦你開啟這個開關，會拒絕掉每一個請求。

### DNS 與憑證頁面顯示無法使用

這兩個功能需要 SQLite，而 SQLite 沒有 MIPS 版本。隧道不受影響，所有類型都能正常使用，只是路由器上管不了憑證。

## 移除

```bash
sh /tmp/openfrp-install.sh --uninstall
```

它只刪自己安裝過的檔案——安裝時記錄了一份清單——停止並停用服務，`/etc/config/openfrp` 會保留，重裝之後隧道設定還在。想徹底清掉就自己刪這個檔案。

VPS 那邊，部署頁面有個**移除**動作，可以乾淨地卸掉伺服端。

## 給開發者

```bash
make build       # 兩個二進位檔，靜態編譯，不依賴 CGO
make check       # vet、gofmt、帶競態偵測的測試
make test-linux  # 在 Linux 上跑測試，splice(2) 只有那裡才真正生效
make bundle      # 安裝腳本與更新功能使用的發佈包
```

`make test-linux` 是重要的那條：零拷貝相關的斷言在 macOS 上會跳過，因為 `splice(2)` 只有 Linux 才有。

## 授權

MIT。見 [LICENSE](LICENSE)。
