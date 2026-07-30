<div align="center">

# OpenFrp

---

在任何地方访问你家里的服务。一个带网页界面的 OpenWrt 插件，管理隧道、域名和 HTTPS 证书。

[English](README.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [日本語](README.ja.md)

![Version](https://img.shields.io/badge/VERSION-v1.1.2-8A2BE2?style=for-the-badge&labelColor=444)
![OpenWrt](https://img.shields.io/badge/OPENWRT-21.02%2B-00B5E2?style=for-the-badge&labelColor=444)
![Arch](https://img.shields.io/badge/ARCH-x86__64%20%7C%20ARM64-000000?style=for-the-badge&labelColor=444)
![Licence](https://img.shields.io/badge/LICENCE-MIT-F5A623?style=for-the-badge&labelColor=444)

</div>

## 这东西是干什么的

你的 NAS、家庭服务器、摄像头、自己搭的网站——外面都访问不到，因为家宽路由器没有公网 IP。

OpenFrp 用一台便宜的 VPS 当大门。访客的流量先到 VPS，再通过路由器保持着的隧道送到你内网的那台机器上。

```
访客 ──▶ 你的 VPS(公网 IP) ──隧道──▶ 你的路由器 ──▶ NAS / 服务器 / 摄像头
```

你需要两样东西：

- **一台跑 OpenWrt 的路由器**，插件和网页界面装在这上面。
- **一台有公网 IP 的 VPS**。什么服务商、什么配置都行，最便宜的就够用。安装时需要用一次 SSH。

域名是可选的。没有域名就用 `IP:端口` 访问；有域名就能用 `nas.example.com` 加真正的 HTTPS。

## 安装

在路由器上跑一条命令：

```bash
wget -O - https://raw.githubusercontent.com/zoefix/openfrp/main/scripts/install.sh | sh
```

脚本会判断路由器的架构、挑对应的版本、用发布时公布的校验和验证下载、先跑一次确认这台机器能执行，然后才安装。缺的依赖会用 `apk` 或 `opkg`（看你系统用哪个）补上。

升级就是把同样的命令再跑一遍。卸载：

```bash
wget -O /tmp/openfrp-install.sh https://raw.githubusercontent.com/zoefix/openfrp/main/scripts/install.sh
sh /tmp/openfrp-install.sh --uninstall
```

这样会保留配置，重装后隧道还在。想连配置、证书和流量记录一起清掉就用 `--purge`。

其他参数：`--version v0.4.0` 装指定版本，`--lang zh-cn` 顺带装语言包，`OPENFRP_API=…` 在连不上 github.com 时指向镜像。

然后打开 **LuCI → 服务 → OpenFrp**。菜单没出来就退出重新登录一次。

## 开始配置

### 第一步：配置 VPS

进 **服务器** 页，点 **添加服务器**，填：

| | |
|---|---|
| 地址 | 你 VPS 的 IP |
| SSH 用户 | 一般是 `root` |
| SSH 密码 | 这时候才问，不勾保存的话不会存下来 |

点 **通过 SSH 部署**。这会把服务端装到 VPS 上：自动识别发行版和 init 系统，上传二进制，写服务，开防火墙，然后启动。大约半分钟，日志会实时显示。

装完连接令牌会自动填好。**状态** 页应该显示服务器已连接。

> VPS 上已经跑着 OpenFrp？那就跳过部署，直接手填地址、端口和令牌。

### 第二步：加一条隧道

进 **隧道** 页点 **添加**，选类型：

| 类型 | 用来做什么 | 访问方式 |
|---|---|---|
| **HTTP** | 网站、NAS 面板，凡是用浏览器打开的 | `https://nas.example.com` |
| **TCP** | SSH、数据库、我的世界、远程桌面 | `你的VPS地址:端口` |
| **UDP** | 游戏服务器、WireGuard、DNS | `你的VPS地址:端口` |

**HTTP** 隧道要填：

- **内网地址 / 端口**——服务实际跑在哪，比如 `192.168.1.50` 和 `5000`。
- **域名**——访客用的名字，比如 `nas.example.com`。
- **启用 HTTPS**——同时在 443 端口提供服务。

**TCP** 和 **UDP** 则是填 **远程端口**：VPS 上哪个端口转发到你的服务。

勾上 **已启用**，点 **保存并应用**。

### 第三步：把域名指向 VPS

在你的 DNS 服务商那里加一条 `A` 记录：

```
nas.example.com.   A   <你的 VPS IP>
```

或者用泛解析，这样底下所有名字都能过来，不用一条条加：

```
*.example.com.     A   <你的 VPS IP>
```

等生效——通常一两分钟——然后打开 `http://nas.example.com`，应该就能看到你的服务了。

### 第四步：签一张 HTTPS 证书

进 **证书管理** 页，点 **申请证书**，填域名提交。像 `nas.example.com` 这种普通名字不需要别的：域名已经指向 VPS 了，VPS 自己就能回应证书颁发机构的验证请求。

**泛域名**（`*.example.com`）不行——证书颁发机构一定要走 DNS 验证，所以要先在 **DNS** 页填你的 DNS 服务商 API 凭据。支持：阿里云、DNSPod、华为云、Cloudflare、NameSilo、PowerDNS、西部数码。

签好以后编辑那条隧道，把 **TLS 处理方式** 设成 *由远端服务器处理 HTTPS*，选上证书。证书会推送到 VPS，过程中不断连接，之后自动续期。

## 域名路由规则

同一台 VPS 上，任意多条隧道共用 80 和 443 端口，靠请求里的域名区分。支持泛解析，`*` 代表**恰好一级**：

| 写法 | 匹配 | 不匹配 |
|---|---|---|
| `aaa.com` | `aaa.com` | 任何子域名 |
| `*.aaa.com` | `www.aaa.com`、`nas.aaa.com` | `x.bb.aaa.com` |
| `*.bb.aaa.com` | `x.bb.aaa.com` | `y.x.bb.aaa.com` |

精确的名字优先级永远高于泛匹配，所以你可以把 `*.aaa.com` 指向一条隧道、`shop.aaa.com` 单独指向另一条。

这和 DNS、HTTPS 证书的规则是一致的：一张 `*.aaa.com` 证书覆盖 `www.aaa.com`，但不覆盖 `x.bb.aaa.com`。保持一致意味着访客永远不会被路由到一条证书覆盖不了他所输入域名的隧道上。

## 几种常见配置

**带网页面板的 NAS**——HTTP 隧道，内网端口 `5000`，域名 `nas.example.com`，开 HTTPS，按第四步签证书。

**SSH 连回家里的机器**——TCP 隧道，内网 `192.168.1.10:22`，远程端口 `2222`。连的时候用 `ssh -p 2222 用户名@你的VPS地址`。

**游戏服务器**——UDP（或 TCP，看游戏），内网端口填服务端监听的那个，远程端口填一样的，这样不用另外通知玩家。

**一台 VPS 上放多个网站**——每个网站一条 HTTP 隧道，各自填自己的域名。它们共用 443 端口，不用额外配置。

## 限速和流量

每条隧道都可以在编辑框里限制：

- **下行限速**——发往访客方向，单位 KB/s。填 `0` 不限。
- **上行限速**——来自访客方向，单位 KB/s。
- **流量上限**——双向合计，单位 MB。达到后这条隧道停止接受新连接。VPS 有月流量套餐的话很有用。

**状态** 页显示每条隧道的实时上下行速率，每天的总量会保留 400 天。

## 保持更新

**状态** 页会显示当前运行的版本。有新版本时旁边会出现一个按钮，点了会显示更新了什么，让你确认。

更新会把所有东西一起换掉——客户端、服务端二进制、网页界面、语言包——然后重启服务。如果新版本起不来，会自动装回旧版本，所以一个有问题的版本不会让路由器的隧道全断。

想从命令行升级的话，重跑一遍安装脚本是一样的效果。

## 出问题了怎么办

### 服务器显示未连接

最常见的原因不是防火墙，是**路由器上的透明代理**。OpenClash、Passwall、ShellCrash 都会把出站 TCP 全部重定向，隧道的控制连接被顺手抓走，送到一个转发不了它的代理节点上。

特征很好认：日志里连接成功了然后立刻断开，而 VPS 那边的日志一条记录都没有。

两种改法，随便哪种都行：

- 在那台服务器的编辑框里打开 **绕过代理**。
- 或者在代理配置里，在最后那条兜底规则前面加一条直连：
  ```yaml
  - "IP-CIDR,<你的VPS地址>/32,DIRECT,no-resolve"
  ```

### 域名打开显示「没有隧道在提供这个名字」

请求已经到 VPS 了，说明 DNS 是对的。要么隧道没启用，要么这个名字不在它的**域名**列表里。注意 `*.example.com` 不覆盖 `a.b.example.com`。

### 申请证书失败

普通域名先确认它已经指向 VPS——证书颁发机构要能访问到。泛域名则检查 **DNS** 页里的凭据，那里有个 **测试** 按钮能验证是否可用。

### 内网服务把所有访客都记成了路由器

在隧道里打开 **客户端 IP**，然后配置你的服务信任它。nginx 的写法：

```nginx
listen 5000 proxy_protocol;
set_real_ip_from <你路由器的内网地址>;
real_ip_header proxy_protocol;
```

**要先配置好服务**——没准备好接收 PROXY 协议头的服务，一旦你打开这个开关，会拒绝掉每一个请求。

### DNS 和证书页面显示不可用

这两个功能需要 SQLite，而 SQLite 没有 MIPS 版本。隧道不受影响，所有类型都能正常用，只是路由器上管不了证书。

## 卸载

```bash
sh /tmp/openfrp-install.sh --uninstall
```

它只删自己装过的文件——安装时记了一份清单——停掉并禁用服务，`/etc/config/openfrp` 会保留，重装后隧道配置还在。想彻底清掉就自己删这个文件。

VPS 那边，部署页面有个**移除**操作，可以干净地卸掉服务端。

## 给开发者

```bash
make build       # 两个二进制，静态编译，不依赖 CGO
make check       # vet、gofmt、带竞态检测的测试
make test-linux  # 在 Linux 上跑测试，splice(2) 只有那里才真正生效
make bundle      # 安装脚本和更新功能用的发布包
```

`make test-linux` 是重要的那条：零拷贝相关的断言在 macOS 上会跳过，因为 `splice(2)` 只有 Linux 才有。

## 许可证

MIT。见 [LICENSE](LICENSE)。
