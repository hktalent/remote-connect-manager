# 远程连接管理器（Remote Connect Manager）

一个自托管的 Web 控制台，把分散的 SSH / VNC / RDP / Telnet 目标集中登记成卡片，点击卡片即可直接在浏览器里操作远程会话；每个卡片用该连接最后一次的界面截图作为背景，一眼就能看出每台机器当前的状态。

![演示](img/demo.jpg)

## 项目介绍

日常要维护多台 Kali / Linux / Windows 主机时，配置散落在各个客户端里，每次都要重新翻 IP、端口、密钥。这个项目把「配置登记 + 一键连接 + 界面留影」做成一个页面：

- 后端 Go（Gin + gorilla/websocket），用 `golang.org/x/crypto/ssh` 直连 SSH，用 noVNC 协议代理 VNC，用 Guacamole 协议经 `guacd` 连接 RDP 与 Telnet。
- 前端纯静态（xterm.js / noVNC / guacamole-common-js），全部依赖本地化到 `web/vendor/`，通过 `embed.FS` 打进二进制，离线可用。
- 配置与密钥以 JSON 形式落盘到 `data/`，截图存 `data/screenshots/`，直接挂载卷即可迁移备份。

## 功能介绍

### 连接管理

- SSH / VNC / RDP / Telnet 四种类型统一登记：名称、主机、端口、用户名、分辨率、认证方式。
- 卡片网格展示；有截图时用最后界面图作背景，没有截图时显示类型图标。
- 卡片右上角提供「编辑配置」「删除」；点击卡片打开会话窗口。
- 会话中的连接卡片会高亮并标注「运行中」，用来区分正在使用的连接。

### SSH 会话

- 支持密钥登录与密码登录；密钥登录时可选择密钥库中的任意私钥。
- 自动扫描 `~/.ssh`：`id_rsa`、`id_rsa.pub`、`authorized_keys` 等文件在启动时自动导入密钥库，同名同内容跳过，同名不同内容自动加哈希后缀，避免覆盖。
- 交互式终端（xterm.js + PTY），窗口大小变化会把 `cols/rows` 同步给远端，`vim`、`htop`、`tmux` 都能正常使用。
- **终端自适应**：用 `ResizeObserver` 监听会话容器，浏览器窗口缩放、全屏切换、布局变化时自动重排并同步 PTY 行列数，不会出现内容溢出或留白。
- **系统剪贴板粘贴**：在会话里直接按 `Cmd+V`（macOS）/ `Ctrl+V`（Windows、Linux）即可粘贴，无需右键菜单；终端用 bracketed paste，`vim` 等全屏程序也不会错位。
- **终端复制**：用鼠标选中文本后按 `Cmd+C`（macOS）/ `Ctrl+Shift+C`（Windows、Linux）即可写入系统剪贴板；`Ctrl+C` 仍保留为发送 SIGINT。canvas 渲染的选区不是 DOM 选区，浏览器原生复制无法直接取到，所以由前端接管。VNC / RDP / Telnet 没有可选中的文本层，需要在远端桌面里复制，文本经剪贴板通道自动写回本地。

### SFTP 文件管理与端口转发

- **SFTP 文件管理**：SSH 会话工具栏点「🗂」打开面板，可浏览目录、进上级、直接输入路径跳转、下载文件、上传文件、新建目录、删除文件/目录。后端复用同一套密钥/密码认证，按需建立独立 SFTP 连接，用完即断。
- **SSH 端口转发**：SSH 会话工具栏点「⇄」，填目标主机（默认 `127.0.0.1`）和目标端口，即可创建一条本地转发；界面显示 `localhost:<本地端口>` 访问地址，支持随时删除。
- 转发端口取自 `TUNNEL_PORT_BASE`~`TUNNEL_PORT_MAX`（默认 20000-20010），Docker 已把该区间映射到宿主机，因此在宿主机用 `localhost:20000` 即可访问远端内网服务（MySQL、Redis、内部 Web 等）。
- 隧道随进程生命周期存在，重启应用后需重新创建；连接断开时会自动释放端口。
- **粘贴 / 拖拽上传（仅 SSH）**：把文件或文件夹从 Finder / 资源管理器拖进 SSH 会话窗口，或复制后在会话里按 `Cmd+V` / `Ctrl+V`，文件会直接落到远端当前目录，右下角浮层显示逐个上传的进度、成功数与失败原因。
  - 目标目录：文件面板（「🗂」）打开时用它的当前目录，否则用该账号的 home（`pwd` 语义，即 SFTP 的初始目录）。
  - 文件夹会递归上传并自动创建子目录；同名文件直接覆盖。
  - 粘贴优先判断剪贴板里是否有文件，有文件就上传、不当作文本粘贴；拖拽遮罩只在真正携带文件时出现。
  - 浏览器兼容性：拖拽在 Chrome / Edge / Safari 上支持文件与文件夹；Firefox 能拖文件但不支持文件夹（`webkitGetAsEntry` 缺失）；Chrome 对「Finder 里直接复制文件再粘贴」支持不稳定，推荐用拖拽。
  - VNC / RDP / Telnet 没有文件通道，在会话里粘贴文件会提示改用 SSH 或共享盘。
- **拖出下载**：文件面板里的文件名可直接拖到桌面或 Finder / 资源管理器中的文件夹，松手即落成文件。依赖浏览器的 `DownloadURL`，Chrome / Edge 支持，Safari / Firefox 不支持。
- **同步到本地目录**：点「同步到本地」，选一个本地文件夹，当前远端目录会递归写入其下的同名子目录（保留目录层级与空目录），右下角浮层显示进度与失败项，关闭浮层即取消。基于 File System Access API，仅 Chrome / Edge 可用（需 HTTPS 或 localhost），目录只写一次、不建立持续同步关系。

### 文件与剪贴板能力对比

| 能力 | SSH | RDP | VNC | Telnet |
| --- | --- | --- | --- | --- |
| 上传 | 拖拽 / 粘贴 / 「🗂」面板上传 | 「⇪」写入共享盘 | 无 | 无 |
| 下载 | 面板下载、拖出到桌面、同步到本地 | 共享盘面板下载、拖出、同步到本地 | 无 | 无 |
| 复制文本到本地 | 选中后 `Cmd+C` / `Ctrl+Shift+C` | 远端桌面内复制后自动写回 | 远端桌面内复制后自动写回 | 同 RDP |
| 本地文本粘贴到远端 | `Cmd+V` / `Ctrl+V` | 同左 | 同左 | 同左 |

> 浏览器不允许网页把任意文件 / 文件夹写入系统剪贴板，所以「在远端复制文件，再到本地 Finder / 资源管理器粘贴」在 Web 端无法实现；等价做法是文件面板的「拖出下载」和「同步到本地」。

### 密钥管理

- 密钥列表：名称、来源（哪个 `~/.ssh` 文件）、创建时间、公钥内容。
- 生成新密钥、导入密钥、重新扫描 `~/.ssh`、一键复制公钥。
- 密钥可编辑：改名称、改私钥正文、改公钥；改名会同步更新所有引用该密钥的连接配置，改正文会校验是否是合法私钥。
- 识别加密私钥（带 passphrase）：列表标记「（加密）」，连接时在密码栏填入口令即可。

### VNC / RDP / Telnet

- VNC 走 WebSocket 裸转发到 noVNC 客户端；服务器需要密码时自动使用配置里保存的密码。
- **VNC 画质调节**：连接配置里的「画质」提供 高清 / 均衡 / 流畅 三档，分别映射 noVNC 的 `qualityLevel` 与 `compressionLevel`（9/3、6/6、3/9），画面不缩放，只调整颜色精度与压缩强度，卡顿时选「流畅」。
- RDP / Telnet 由后端完成 Guacamole 握手（`select → args → size/image/video/audio/timezone → connect → ready`），前端只负责渲染与输入，`guacd` 由 compose 里的独立服务提供。
- **RDP 画质可调**：连接配置里的「画质」对应 RDP 色深，可选 24 位（高清）、16 位（推荐，省带宽）、8 位（极速）。分辨率不变，只降低颜色精度，带宽和卡顿随之下降；改完需重连生效。
- **优先有损编码**：握手时按 `image/webp → image/jpeg → image/png` 声明支持的格式，避免大块画面走 PNG 无损编码；同时设置 `force-lossless=false`。
- **RDP 文件传输**：开启共享盘（`drive-name=rcm`），会话工具栏出现「⇪」上传按钮，选中的文件会出现在远端的共享盘中——Windows 为 `\\tsclient\rcm`，xrdp 为 `~/thinclient_drives/rcm`。
- **共享盘下载**：RDP 会话工具栏点「🖴」打开「共享盘」面板，可浏览目录、下载文件（支持断点续传），文件也能直接拖到桌面或本地文件夹，或用「同步到本地」把当前目录整个写入本地文件夹。共享盘落地在 guacd 的 `drive-path`（默认 `/tmp/rcm-drive`），Docker 部署时与主程序共用宿主机 `./data/rdp-drive`；远端往共享盘写入文件后，点「刷新」即可看到并下载。guacd 若下发 Guacamole `file` / `filesystem` 推送指令，浏览器也会自动弹出下载（`onfile`、`onfilesystem` 均已接）。
- **双向剪贴板**：本地按 `Cmd+V` / `Ctrl+V` 通过 Guacamole clipboard 流把文本送入远端；远端复制的文本也会写回本地系统剪贴板（非 HTTPS 环境自动降级为 `execCommand`）。
- **声音**：握手声明 `audio/L16`、`audio/L8`，RDP 会话的声音在浏览器里直接播放（需先点击过会话画面，满足浏览器的自动播放策略）。
- **Telnet**：新增连接类型，默认端口 23，复用同一套 guacd 通道；配色、字号、`xterm-256color` 终端类型均已调好。Telnet 是明文协议，只建议在内网或隧道内使用。

### 会话窗口

- 缩放（`−` / `⟲` / `+`）、全屏（`⛶`）。
- **最小化（`↘`）**：把当前窗口等比例动画缩回该连接卡片的位置，连接**不中断**，卡片标记「运行中」；再点卡片即从卡片位置等比放大恢复原会话。
- **关闭（`✕`）**：先截图并等上传完成，再断开连接，随后自动刷新该卡片的背景图。

| 快捷键 | 作用 |
| --- | --- |
| `Cmd+V` / `Ctrl+V` | 粘贴文本；若剪贴板里是文件且当前为 SSH，则直接上传到远端当前目录 |
| `Cmd+C`（macOS）/ `Ctrl+Shift+C`（Windows、Linux） | 把终端选区写入系统剪贴板 |
| `Ctrl+C` | 照常发送 SIGINT，不会被复制逻辑拦截 |

### 卡片背景图

- 会话进行中每 2 秒检测一次画面是否变化（降采样指纹比对），有变化时**最短间隔 10 秒**上传一次并替换卡片背景；最小化到后台后仍持续更新。
- 截图统一降采样为 JPEG，服务端只保留每台连接的最新一张，长时间挂着也不会把磁盘写满。

## 特点

- **零客户端**：浏览器打开就能用，不需要在每台设备上装终端 / 远程桌面软件。
- **开箱即用**：启动时自动接管 `~/.ssh` 里的密钥，已有免密登录习惯无需改动。
- **离线可构建**：前端依赖与 Go 依赖都走 `vendor/`，`docker build` 不需要外网。
- **数据即文件**：JSON + 截图文件，备份就是拷 `data/` 目录，没有数据库依赖。
- **会话保活**：最小化只是「收进卡片」，SSH/远程桌面会话在后台继续跑，随时点开继续操作。
- **文件双向传输**：SSH 支持拖拽 / 粘贴上传、面板下载、拖出到桌面与整目录同步；RDP 通过共享盘同样能双向取文件。
- **剪贴板贯通**：文本可双向同步，终端选区能直接复制到系统剪贴板，粘贴也不需要右键菜单。
- **单二进制 + 内嵌前端**：`go build` 出一个可执行文件，前端资源打包在二进制内。

## 技术栈

| 层 | 技术 |
| --- | --- |
| 后端 | Go 1.21+、Gin、gorilla/websocket |
| SSH | `golang.org/x/crypto/ssh`（密钥/密码认证、PTY、WindowChange、端口转发） |
| SFTP | `github.com/pkg/sftp`（按需建立独立会话，复用同一套认证） |
| VNC | WebSocket ↔ TCP 裸转发 + noVNC 1.4 |
| RDP | Guacamole 协议隧道 + `guacd` 1.5.5 |
| Telnet | Guacamole 协议隧道 + `guacd` 1.5.5（终端渲染） |
| 前端 | 原生 JS、xterm.js 5.3（含 canvas 渲染器用于截图）、noVNC、guacamole-common-js |
| 存储 | JSON 文件（`data/connections.db`）+ 截图文件（`data/screenshots/`） |
| 部署 | Docker 多阶段构建 + docker compose |

## 目录结构

```
remote-connect-manager/
├── main.go                     # 路由、embed 前端、启动时自动导入 ~/.ssh 密钥
├── internal/
│   ├── config/                 # 连接配置 CRUD（JSON 持久化）
│   ├── keys/                   # 密钥库：扫描、生成、导入、编辑、加载签名器
│   ├── ssh/                    # SSH WebSocket + PTY 透传
│   ├── sftp/                   # SFTP 文件管理 API（列表 / 上传 / 下载 / 建目录 / 删除）
│   ├── tunnel/                 # SSH 端口转发（本地监听 → 远端目标）
│   ├── vnc/                    # VNC WebSocket 裸转发
│   ├── rdp/                    # RDP / Telnet：guacd 握手与双向透传
│   ├── rdpdrive/               # RDP 共享盘浏览与下载 API
│   ├── screenshot/             # 截图落盘与旧图清理
│   └── testutil/               # 单元测试用的内存 SSH / SFTP 服务端
├── web/
│   ├── index.html              # 页面结构
│   ├── css/style.css
│   ├── js/app.js               # 卡片、配置弹窗、密钥弹窗、会话与截图逻辑
│   └── vendor/                 # xterm / noVNC / guacamole 本地化依赖
├── data/                       # 运行时数据（配置、截图、密钥），首次启动自动创建
├── vendor/                     # Go 依赖（离线构建）
├── .github/workflows/release.yml  # 推 v* 标签自动编译三平台二进制并发布
├── Dockerfile
├── docker-compose.yml
└── start.sh
```

## 编译

### 本地编译

需要 Go 1.21+。依赖已随仓库提供（`vendor/`），无需联网：

```bash
git clone https://github.com/hktalent/remote-connect-manager.git
cd remote-connect-manager

# 使用 vendor 离线构建
GOFLAGS=-mod=vendor go build -o remote-connect-manager .

# 可选：跑一下
PORT=8080 ./remote-connect-manager
```

> 若想更新依赖，可用 `GOFLAGS=-mod=mod GOPROXY=https://goproxy.cn,direct go mod tidy` 后再 `go mod vendor`。

### 跨平台发布（GitHub Actions）

仓库内置 [.github/workflows/release.yml](.github/workflows/release.yml)：**推送 `v*` 标签即自动编译并发布 Release**，产物是不依赖 Docker 的独立可执行文件。

```bash
git tag v1.0.0
git push origin v1.0.0
```

工作流会并行编译 5 个目标并上传到 Release：

| 平台 | 架构 | 产物 |
| --- | --- | --- |
| Linux | amd64 / arm64 | `remote-connect-manager_<版本>_linux_<arch>.tar.gz` |
| macOS | amd64（Intel）/ arm64（Apple Silicon） | `remote-connect-manager_<版本>_darwin_<arch>.tar.gz` |
| Windows | amd64 | `remote-connect-manager_<版本>_windows_amd64.zip` |

- 编译参数：`CGO_ENABLED=0`（纯静态、无 C 依赖）、`-trimpath`、`-ldflags "-s -w -X main.version=<标签>"`，前端 `web/` 由 `embed.FS` 打包进二进制。
- 也可以在 Actions 页面手动触发（`Release → Run workflow`），填一个版本号即可。
- 需要本地复现某个平台时：

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 GOFLAGS=-mod=vendor \
  go build -trimpath -ldflags "-s -w -X main.version=v1.0.0" -o rcm.exe .
```

### Docker 构建

```bash
docker build -t remote-connect-manager:latest .
```

多阶段构建：`golang` 镜像里离线编译出静态二进制，再拷进 `debian:bookworm-slim`，镜像内只保留可执行文件、`web/` 与启动脚本。

## 使用

### 1. 用 docker compose 启动（推荐）

```bash
cd remote-connect-manager
docker compose up -d --build
```

启动后访问：<http://localhost:8088>

compose 会拉起两个容器：

- `remote-connect-manager`：主服务，宿主机 `8088` → 容器 `8080`。
- `rcm-guacd`：`guacamole/guacd:1.5.5`，供 RDP / Telnet 使用。

默认挂载与参数（见 [docker-compose.yml](docker-compose.yml)）：

| 项 | 说明 |
| --- | --- |
| `./data:/app/data` | 配置、密钥、截图持久化到宿主机 |
| `${HOME}/.ssh:/host-ssh:ro` | 只读挂载宿主机 SSH 目录，启动时自动导入私钥 |
| `./data/rdp-drive:/tmp/rcm-drive` | 两个容器共用，RDP 共享盘落地目录（浏览器显示下载的文件都在这） |
| `PORT=8080` | 容器内监听端口 |
| `GUACD_ADDR=guacd:4822` | RDP / Telnet 使用的 guacd 地址 |
| `host.docker.internal:host-gateway` | 让容器能访问宿主机上的虚拟机 / 端口转发 |

改端口只需改 compose 里的 `"8088:8080"` 左侧，例如 `"9090:8080"`。

### 2. 独立运行（Windows / macOS / Linux，免 Docker）

到 [Releases](https://github.com/hktalent/remote-connect-manager/releases) 下载对应平台的包，解压后在**同一个目录**里运行（配置、密钥、截图默认写在当前目录的 `data/`）：

**macOS / Linux**

```bash
tar -xzf remote-connect-manager_<版本>_<平台>_<架构>.tar.gz
chmod +x remote-connect-manager
./remote-connect-manager              # 默认 :8080
PORT=9090 ./remote-connect-manager    # 换端口
```

> macOS 首次运行若提示「无法验证开发者」，右键 → 打开，或执行 `xattr -d com.apple.quarantine ./remote-connect-manager`。

**Windows**

```powershell
# 解压 zip 后，在解压目录打开 PowerShell
.\remote-connect-manager.exe
# 换端口
$env:PORT=9090; .\remote-connect-manager.exe
```

打开 <http://127.0.0.1:8080> 即可使用。

独立运行的注意事项：

- **数据目录跟随工作目录**：`data/connections.db`、`data/keys/`、`data/screenshots/` 都相对于启动时的当前目录。建议在固定目录里启动（Windows 快捷方式需设置「起始位置」），迁移时拷 `data/` 即可。
- **密钥来源**：会自动扫描 `$HOME/.ssh`（Windows 为 `C:\Users\<你>\.ssh`）；想扫描其它目录用 `SSH_SCAN_DIR` 指定。
- **RDP 依赖 guacd**：SSH / VNC 开箱可用，RDP 需要本机或局域网内有 `guacd`：

```bash
docker run -d --name guacd -p 4822:4822 guacamole/guacd:1.5.5   # 只需这一个容器
GUACD_ADDR=127.0.0.1:4822 ./remote-connect-manager
```

- **生产环境**建议关闭 Gin 调试输出：`GIN_MODE=release ./remote-connect-manager`。
- 版本号来自编译期注入，启动日志第一行会打印（如 `remote-connect-manager v1.0.0`）。

### 3. 配置一条连接

1. 点右上角「+ 新建连接」，选择类型（SSH / VNC / RDP / Telnet），填主机、端口、用户名。
2. SSH 选「密钥」认证并挑一把密钥；私钥带口令的话在密码栏填口令。VNC / RDP / Telnet 直接填密码。
3. 保存后点击卡片即可连接。终端里正常操作，窗口大小变化会同步到远端。
4. 想切回卡片视图点 `↘`（会话不中断），想结束会话点 `✕`（关闭后卡片背景自动更新为该会话最后一屏）。

### 4. 密钥

点右上角「密钥管理」：

- 「扫描 ~/.ssh」重新扫描并导入新密钥；
- 「生成新密钥」按名称生成一对新密钥；
- 每个密钥可「复制公钥」（粘到目标机器 `~/.ssh/authorized_keys`）、「编辑」（改名 / 改内容）、「删除」。

密钥文件实际存放在 `data/keys/`，来源目录可通过环境变量 `SSH_SCAN_DIR` 指定（默认依次尝试 `/host-ssh`、`$HOME/.ssh`）。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PORT` | `8080` | HTTP 监听端口 |
| `GUACD_ADDR` | `127.0.0.1:4822` | guacd 地址，RDP 使用 |
| `SSH_SCAN_DIR` | `/host-ssh` → `$HOME/.ssh` | 密钥扫描目录 |

## 配置说明

### 数据目录

所有运行时数据都在启动目录的 `data/` 下（Docker 部署时对应容器内 `/app/data`）：

```
data/
├── connections.db        # 连接配置，实际是 JSON 数组（不是 SQLite）
├── keys/                 # 密钥库
│   ├── id_rsa            # 私钥（文件名即密钥名）
│   ├── id_rsa.pub        # 公钥
│   └── .manifest.json    # 密钥来源记录：{ "id_rsa": "/Users/you/.ssh/id_rsa" }
└── screenshots/          # 卡片背景图：<连接ID>_<纳秒时间戳>.jpg|png（每连接只保留最新一张）
```

### 连接配置（`data/connections.db`）

每次请求都会重新读取该文件，因此**直接编辑 JSON 也能生效**（改完刷新页面即可，无需重启）。格式：

```json
[
  {
    "id": "0b6f3c1e-9a2d-4b7c-8f10-3d1a2b3c4d5e",
    "name": "戴尔kali",
    "type": "ssh",
    "host": "192.168.0.121",
    "port": 22,
    "username": "root",
    "auth_type": "key",
    "key_name": "id_ed25519",
    "width": 0,
    "height": 0,
    "screenshot": "/screenshots/0b6f...jpg",
    "created_at": "2026-11-16T13:37:00+08:00",
    "updated_at": "2026-11-16T13:37:00+08:00",
    "last_connect": "2026-11-16T21:56:51+08:00"
  }
]
```

| 字段 | 说明 |
| --- | --- |
| `id` | UUID，新建时自动生成 |
| `name` | 卡片显示名 |
| `type` | `ssh` / `vnc` / `rdp` / `telnet` |
| `host` / `port` | 目标地址；RDP 默认 3389，VNC 5900，SSH 22，Telnet 23 |
| `username` | 登录用户（VNC 可留空） |
| `auth_type` | `key` 或 `password`（SSH 生效） |
| `key_name` | 引用 `data/keys/` 下的密钥名；私钥带口令时口令填在 `password` |
| `password` | VNC / RDP / Telnet 密码，或 SSH 密钥口令 |
| `width` / `height` / `security` | RDP / VNC 画面参数，缺省由服务端决定（1280×720、`any`） |
| `color_depth` | RDP 色深：`24`（默认）/ `16`（推荐，省带宽）/ `8`（极速），分辨率不变只降颜色精度 |
| `vnc_quality` | VNC 画质：`high` / `medium`（默认）/ `low`，映射 noVNC 的 `qualityLevel` 与 `compressionLevel` |
| `screenshot` | 卡片背景图地址，由程序自动写入，不要手填 |

> `data/connections.db` 里可能含明文密码，请当作敏感文件保护（Docker 部署时它落在宿主机 `./data`）。

### 密钥文件

- 密钥名必须能通过文件名安全校验（字母、数字、`_`、`-`、`.`），改名会同步更新所有引用它的连接。
- 从 `~/.ssh` 扫描时：能解析的私钥会推导公钥；加密私钥（`-----BEGIN ... ENCRYPTED-----`）也会导入并标记「加密」，连接时用口令解锁；同名不同内容会加 `_<sha256前6位>` 后缀，不会覆盖已有密钥。
- `.pub` 文件、`config`、`known_hosts*`、`authorized_keys*` 会被跳过。

### 端口与运行参数

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `PORT` | `8080` | 监听端口（Docker compose 里映射为宿主机 `8088`） |
| `GUACD_ADDR` | `127.0.0.1:4822` | RDP / Telnet 使用的 guacd 地址 |
| `TUNNEL_PORT_BASE` | `20000` | SSH 端口转发可用区间起点 |
| `TUNNEL_PORT_MAX` | `20010` | SSH 端口转发可用区间终点（compose 已映射该区间） |
| `RCM_DRIVE_PATH` | `/tmp/rcm-drive` | RDP 共享盘目录（同时作为 guacd 的 `drive-path` 与本地下载源） |
| `SSH_SCAN_DIR` | `/host-ssh` → `$HOME/.ssh` | 启动时扫描导入密钥的目录 |
| `GIN_MODE` | `debug` | 设为 `release` 关闭调试日志 |

## API 一览

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/connections` | 连接列表（不含密码 / 私钥） |
| GET/POST | `/api/connections`、`/api/connections/:id` | 查询 / 新建 / 修改 / 删除配置 |
| GET/POST | `/api/connections/:id/screenshot` | 取截图地址 / 上传截图（`{"data":"data:image/jpeg;base64,..."}`） |
| GET | `/api/keys`、`/api/keys/:name`、`/api/keys/:name/public` | 密钥列表 / 私钥详情 / 公钥 |
| POST | `/api/keys/generate`、`/api/keys/import`、`/api/keys/scan` | 生成 / 导入 / 扫描密钥 |
| PUT/DELETE | `/api/keys/:name` | 编辑（含改名）/ 删除密钥 |
| GET | `/api/readme` | 项目 README（「关于」弹窗渲染用） |
| GET | `/api/sftp/:id/list?path=` | 列出远端目录 |
| GET | `/api/sftp/:id/download?path=` | 下载远端文件 |
| POST | `/api/sftp/:id/upload` | 上传文件（multipart：`file` + `path`，可选 `rel` 为相对 `path` 的目标路径，用于文件夹上传，父目录自动创建） |
| POST | `/api/sftp/:id/mkdir`、`/api/sftp/:id/remove` | 新建目录 / 删除文件或目录 |
| GET/POST | `/api/tunnels` | 隧道列表 / 新建（`connection_id`、`remote_host`、`remote_port`） |
| DELETE | `/api/tunnels/:id` | 关闭隧道 |
| GET | `/api/rdp-drive/list?path=` | 列出 RDP 共享盘（guacd `drive-path`）目录 |
| GET | `/api/rdp-drive/download?path=` | 下载共享盘文件（支持 Range） |
| WS | `/ws/ssh/:id`、`/ws/vnc/:id`、`/ws/guacamole/:id`、`/ws/telnet/:id` | SSH / VNC / RDP / Telnet 会话通道 |

## 常见问题

**RDP 连不上 / 报 guacd 相关错误**：确认 `rcm-guacd` 容器在运行（`docker ps`），且 `GUACD_ADDR` 指向正确；非 Docker 部署需自行启动 `guacd`。

**RDP 卡顿 / 画面延迟**：编辑该连接，把「画质」调成 16 位或 8 位（分辨率不变），保存后**重连**生效；如果仍然卡，再考虑降低 `width`/`height` 或改善链路。默认已优先使用 WebP/JPEG 有损编码。

**VNC 卡顿**：编辑该连接，把「画质」调成「流畅」（颜色精度下降、压缩加强），重连生效。

**RDP 上传的文件在哪**：点会话工具栏的「⇪」选择文件，文件进入远端共享盘 `rcm`——Windows 在 `\\tsclient\rcm`，xrdp 在 `~/thinclient_drives/rcm`。反向下载在会话工具栏的「🖴」面板里，看到的就是同一个共享盘。

**远端复制的文本没进本地剪贴板**：HTTPS 下用 `navigator.clipboard`，HTTP 下自动降级为 `execCommand('copy')`；部分浏览器会拦截后台写入，点一下页面再复制一次即可。

**Telnet 安全性**：Telnet 无加密，账号密码明文传输，只建议在可信内网或 SSH 隧道内使用。

**隧道端口访问不到**：确认宿主机端口已映射（`docker port remote-connect-manager` 应包含 20000-20010），并检查目标端口在远端是否监听（如 `ss -lntp`）；隧道只转发 TCP。

**SFTP 面板打不开或报权限错误**：SFTP 用的是连接配置里的同一账号与密钥；确认该账号有目标目录权限，且远端 sshd 未禁用 sftp 子系统（`Subsystem sftp ...`）。

**「同步到本地」没反应 / 文件拖不出去**：两者依赖 Chromium 专有 API（File System Access、`DownloadURL`），Firefox 与 Safari 不支持，且页面必须通过 HTTPS 或 localhost 打开。拖出一次只能带一个文件，文件夹请用「同步到本地」。

**按 `Cmd+V` 粘贴没反应**：确认焦点在会话窗口内（点一下终端画面），且浏览器加载的是带版本号的最新前端；若仍无反应，强制刷新（`Cmd+Shift+R`）清掉旧缓存。配置弹窗里的输入框仍使用浏览器默认粘贴。

**终端里选中文本按 `Cmd+C` 复制不出来**：先确认文本确实被选中（有高亮）。快捷键为 macOS `Cmd+C`、Windows / Linux `Ctrl+Shift+C`；`Ctrl+C` 仍用于发送 SIGINT，不会复制。canvas 渲染的选区不是 DOM 选区，所以由前端接管写入剪贴板。

**能不能在远端复制文件后，到本地 Finder / 资源管理器里直接粘贴？**：不能。浏览器 Clipboard API 只允许写入文本和图片，无法把任意文件或文件夹放进系统剪贴板。请用文件面板的「拖出下载」或「同步到本地」。

**列表里没有我的密钥**：确认 `~/.ssh` 已只读挂载进容器，或在「密钥管理」里点「扫描 ~/.ssh」；加密私钥也能导入，但连接时需填口令。

**卡片背景一直是图标**：说明还没有成功截图，先完整连一次会话（会话中会每 10 秒更新，关闭时会立即更新）。

**端口冲突**：改 `docker-compose.yml` 中 `"8088:8080"` 的宿主机侧端口后 `docker compose up -d`。

## 说明

本项目面向内网 / 自用场景，默认没有账号体系与 TLS。若要暴露到公网，请自行在反向代理层加认证与 HTTPS。

## License

MIT
