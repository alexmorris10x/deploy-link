# Deploy Link

[English](#english) · [中文](#中文)

## English

Deploy Xcode-built apps beyond the local network. Deploy Link keeps an
authenticated Apple RemotePairing tunnel open over Tailscale for each device
profile, so a signed iOS app built on a Mac can be installed after a paired
device roams away from its acquisition network.

> [!IMPORTANT]
> Establish the Deploy Link bridge first. Xcode builds and signs the app; Deploy
> Link then installs Xcode's `.app` output through that bridge. This project does
> not make the paired iOS device appear as a native remote Xcode Run
> Destination.

```text
Xcode build + signing
        |
        v
signed .app -> Deploy Link CLI -> owner-only Unix socket
                                      |
                                      v
                            persistent bridge on macOS
                                      |
                                      v
                         Tailscale -> paired iOS device
```

### Current network boundary

- The Mac and paired iOS device may be on different physical networks.
- Both devices must be online in the same Tailscale tailnet.
- The bridge must first become `active` while iOS exposes RemotePairing. Any
  Wi-Fi connection can open that listener; it does not have to be the Mac's
  LAN.
- After the bridge is active, the same session can serve installs across
  unrelated Wi-Fi networks. An iPhone session can also survive a Wi-Fi-to-5G
  roam and serve repeated installs.
- Each device profile has an independent launchd supervisor, socket, local
  forward port, and log.
- A cold start on pure cellular is not supported. If the outer session is lost,
  join any Wi-Fi so the daemon can reacquire the listener.

### Requirements

- Apple-silicon Mac with Xcode Command Line Tools;
- iPhone or iPad with Developer Mode enabled;
- Tailscale signed into the same tailnet on the Mac and iOS device;
- an Apple RemotePairing record trusted by the iOS device;
- an Xcode project configured with valid iOS development signing.

### 1. Bootstrap the pairing identity

If you already have an owner-only `pymobiledevice3` RemotePairing record, reuse
it. Otherwise, connect the trusted iOS device over USB once and follow the
[`pymobiledevice3` iOS 17+ tunnel guide](https://github.com/doronz88/pymobiledevice3/blob/master/docs/guides/ios17-tunnels.md) to install that tool, then create the record:

```sh
python3 -m pymobiledevice3 lockdown remotepairing --pair
```

The record is normally written under `~/.pymobiledevice3/`. It contains private
key material and must remain readable only by its owner. Deploy Link reads it in
place and never copies it into the repository or profile.

### 2. Install the bridge

```sh
git clone https://github.com/zjz-connect/xcode-deploy-link-deploy-beyond-local-network.git
cd xcode-deploy-link-deploy-beyond-local-network
./scripts/install.sh
```

The deterministic installer downloads the pinned Go toolchain and pinned Link
Core source, applies the repository patch, and installs the binary under:

```text
~/Library/Application Support/Deploy Link/bin/deploy-link
```

It does not install Go globally.

### 3. Configure and start the bridge

The commands below use an iPhone profile as an example. Keep the device on any
Wi-Fi for this first acquisition. Replace the values with its RemotePairing
identifier, Tailscale IP, and pairing-record path:

```sh
deploy_link="$HOME/Library/Application Support/Deploy Link/bin/deploy-link"
profile="$HOME/Library/Application Support/Deploy Link/iphone.json"

"$deploy_link" configure \
  --profile "$profile" \
  --device-label "development iphone" \
  --remote-identifier "REMOTE-PAIRING-IDENTIFIER" \
  --target-tailnet-ip "TAILSCALE-IP" \
  --pair-record "$HOME/.pymobiledevice3/remote_REMOTE-PAIRING-IDENTIFIER.plist"

"$deploy_link" doctor --profile "$profile"
"$deploy_link" launch-agent install --profile "$profile"
"$deploy_link" status --profile "$profile"
```

Do not switch the iPhone to 5G until `status` reports `active`. The default
RemotePairing port is `49152`; pass `--remote-pairing-port` to `configure` only
when the discovered listener uses a different port.

Repeat configuration and LaunchAgent installation for an iPad or another
iPhone. Give each profile a different `--local-forward-port`. The supervisor
waits if a foreground daemon already owns that profile, so installing it does
not intentionally stop a working session. It also requests an idle-sleep
assertion while the Mac is on AC power. A closed laptop lid or Mac restart can
still end the warm Apple connection.

Installing the same profile again does not restart its loaded service. Schedule
an intentional remove-and-install upgrade while the device can expose its
listener, because replacing the service process ends the warm session.

When Apple changes the listener port, Deploy Link uses a rate-limited,
protocol-verified scan and retains the learned port in memory. This removes the
usual manual port update, but it cannot expose a listener that iOS has closed
on cellular.

### 4. Build and sign with Xcode

Build a device app with your normal Apple development team and provisioning
profile. For example:

```sh
xcodebuild \
  -project YourApp.xcodeproj \
  -scheme YourApp \
  -configuration Debug \
  -destination 'generic/platform=iOS' \
  -derivedDataPath "$PWD/.build/DeployLinkDerivedData" \
  build
```

For a workspace, replace `-project YourApp.xcodeproj` with
`-workspace YourApp.xcworkspace`. A successful device build produces a signed
`.app`, usually under
`.build/DeployLinkDerivedData/Build/Products/Debug-iphoneos/`.

### 5. Install Xcode's output through the bridge

Once the bridge is active, the paired device may remain on unrelated Wi-Fi, or
an iPhone may roam to 5G. Install the signed result:

```sh
"$deploy_link" install \
  --profile "$profile" \
  "$PWD/.build/DeployLinkDerivedData/Build/Products/Debug-iphoneos/YourApp.app"
```

Slow public networks can take longer than five minutes. The profile's
`install_timeout_seconds` is a no-progress limit, not a total install limit.
Payload bytes, Apple installation stages, and device percentages reset it. A
separate hard safety limit is at least 30 minutes. If the install stream truly
stalls, the daemon closes only that stream and preserves the warm outer
session. ZipConduit cannot resume, so a later retry starts from byte zero.

The short-lived CLI submits the request to the persistent daemon. Success is
reported only after streaming zip conduit completes and InstallationProxy reads
back the same bundle identifier, version, and build from the device. Later
builds reuse the same warm generation.

### 6. Submit an unattended install

Use an install job when the terminal or agent should be free after submission.
`submit` validates the signed app, makes a private snapshot, records its full
content SHA-256 digest, and starts a macOS-owned one-shot LaunchAgent. The command returns a job ID, and
the one-shot process dispatches the job to the persistent daemon. The caller
can then disconnect without owning the transfer.

```sh
"$deploy_link" job submit --profile "$profile" /absolute/path/to/YourApp.app
"$deploy_link" job status --profile "$profile" <job-id>
"$deploy_link" job wait --profile "$profile" <job-id>
"$deploy_link" job remove --profile "$profile" <job-id>
```

`status` reads the durable receipt. `wait` follows it until the state becomes
`installed`, `failed`, or `interrupted`. A receipt can also show `queued` or
`running`. A profile can have only one active job. Deploy Link rejects another
submission instead of creating a queue.

The one-shot trigger uses `RunAtLoad` without `KeepAlive`. After the daemon
durably changes `queued` to `running`, the daemon owns the transfer and receipt
even if the trigger disconnects. It never retries a failed or interrupted
transfer. Before it claims the job, the daemon verifies the snapshot's bundle
identity, version, build, and content digest against the submitted receipt.
`installed` is written only after the random daemon instance ID and
session generation remain unchanged, the install count advances by exactly
one, and InstallationProxy reads back the exact bundle identifier, version,
and build.

The private snapshot and receipt remain until `remove` cleans the terminal job.
The Mac must stay awake and reachable, and the warm profile session must remain
available while the job runs. The job is independent of the submitting caller.
A running transfer cannot continue through loss of the Apple session or a Mac
shutdown.

Useful commands:

```sh
"$deploy_link" status --profile "$profile"
"$deploy_link" watch --profile "$profile"
"$deploy_link" stop --profile "$profile"
"$deploy_link" launch-agent remove --profile "$profile"
```

For a supervised profile, `stop` recycles the current daemon and the supervisor
starts it again. Use `launch-agent remove` to stop and disable that profile
service.

### Development

```sh
./scripts/test.sh
```

The test entry point verifies the pinned downstream patch, affected Link Core
packages, Deploy Link unit tests, race tests, and `go vet`. See
[`docs/architecture.md`](docs/architecture.md) for ownership and failure
boundaries.

Deploy Link is released under the [MIT License](LICENSE). Link Core is derived
from [`danielpaulus/go-ios`](https://github.com/danielpaulus/go-ios); see
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).

---

## 中文

Deploy Link 用于在局域网之外安装由 Xcode 构建的 App。它为每个设备 profile
通过 Tailscale 持续保有一条经过认证的 Apple RemotePairing 隧道，使 Mac 上由
Xcode 构建并签名的 iOS App，可以在设备离开首次连接的网络后继续安装。

> [!IMPORTANT]
> 必须先建立 Deploy Link bridge。Xcode 负责构建和签名，Deploy Link 再通过
> bridge 安装 Xcode 生成的 `.app`。本项目不会让已配对的 iOS 设备变成 Xcode
> 原生的远程 Run Destination。

```text
Xcode 构建和签名
        |
        v
已签名 .app -> Deploy Link CLI -> 仅限当前用户的 Unix socket
                                      |
                                      v
                              macOS 常驻 bridge
                                      |
                                      v
                            Tailscale -> 已配对的 iOS 设备
```

### 当前网络边界

- Mac 和已配对的 iOS 设备可以位于不同的物理网络。
- 两台设备必须在线并加入同一个 Tailscale tailnet。
- iOS 暴露 RemotePairing listener 时，bridge 必须先进入 `active`。任意 Wi-Fi
  都可以打开该 listener，不要求与 Mac 位于同一个局域网。
- bridge 激活后，同一条 session 可以跨不同 Wi-Fi 网络安装。iPhone session 也
  可以跨越 Wi-Fi 到 5G 的切换，并用于多次安装。
- 每个设备 profile 都有独立的 launchd supervisor、socket、本地转发端口和日志。
- 暂不支持纯蜂窝网络冷启动。外层 session 丢失后，需要让设备加入任意 Wi-Fi，
  daemon 才能重新获取 listener。

### 前置条件

- Apple 芯片 Mac，并安装 Xcode Command Line Tools；
- iPhone 或 iPad 已开启 Developer Mode；
- Mac 和 iOS 设备已登录同一个 Tailscale tailnet；
- iOS 设备已信任的 Apple RemotePairing record；
- 已正确配置 iOS 开发签名的 Xcode 项目。

### 1. 首次建立配对身份

如果已有仅限当前用户访问的 `pymobiledevice3` RemotePairing record，可以直接
复用。否则先通过 USB 连接并信任 iOS 设备，按照
[`pymobiledevice3` 的 iOS 17+ tunnel 指南](https://github.com/doronz88/pymobiledevice3/blob/master/docs/guides/ios17-tunnels.md)
安装该工具，然后生成 record：

```sh
python3 -m pymobiledevice3 lockdown remotepairing --pair
```

record 通常位于 `~/.pymobiledevice3/`。其中包含私钥，只能由当前用户读取。
Deploy Link 会在原路径读取它，不会将其复制到仓库或 profile。

### 2. 安装 bridge

```sh
git clone https://github.com/zjz-connect/xcode-deploy-link-deploy-beyond-local-network.git
cd xcode-deploy-link-deploy-beyond-local-network
./scripts/install.sh
```

可复现安装脚本会下载固定版本的 Go 工具链和 Link Core 源码，应用仓库内的固定
patch，并将二进制文件安装到：

```text
~/Library/Application Support/Deploy Link/bin/deploy-link
```

它不会在系统中全局安装 Go。

### 3. 配置并启动 bridge

下列命令以 iPhone profile 为例。首次连接时先让设备保持在任意 Wi-Fi。将下列
值替换为设备的 RemotePairing identifier、Tailscale IP，以及 pairing record
的绝对路径：

```sh
deploy_link="$HOME/Library/Application Support/Deploy Link/bin/deploy-link"
profile="$HOME/Library/Application Support/Deploy Link/iphone.json"

"$deploy_link" configure \
  --profile "$profile" \
  --device-label "development iphone" \
  --remote-identifier "REMOTE-PAIRING-IDENTIFIER" \
  --target-tailnet-ip "TAILSCALE-IP" \
  --pair-record "$HOME/.pymobiledevice3/remote_REMOTE-PAIRING-IDENTIFIER.plist"

"$deploy_link" doctor --profile "$profile"
"$deploy_link" launch-agent install --profile "$profile"
"$deploy_link" status --profile "$profile"
```

必须等 `status` 显示 `active` 后，才能把 iPhone 切换到 5G。默认 RemotePairing
端口是 `49152`；只有实际发现的 listener 使用其他端口时，才需要在 `configure`
中传入 `--remote-pairing-port`。

配置 iPad 或其他 iPhone 时，请为每个 profile 设置不同的
`--local-forward-port`，并分别安装 LaunchAgent。supervisor 会等待已占用该
profile socket 的前台 daemon，因此安装服务不会主动中断正在工作的 session。
Mac 使用交流电源时，服务还会请求阻止系统因空闲而睡眠。合上 MacBook 屏幕或
重启 Mac 仍可能结束 Apple session。

对同一个 profile 再次执行安装不会重启已加载的服务。若要升级服务，请在设备
可以暴露 listener 时明确执行移除和重新安装，因为替换服务进程会结束 warm
session。

Apple 改变 listener 端口后，Deploy Link 会执行限频且经过协议验证的扫描，并在
内存中保留新端口。该功能可以减少手动更新端口，但无法让 iOS 在纯蜂窝网络上
重新开放已经关闭的 listener。

### 4. 使用 Xcode 构建并签名

使用正常的 Apple 开发团队与 provisioning profile 构建设备 App。例如：

```sh
xcodebuild \
  -project YourApp.xcodeproj \
  -scheme YourApp \
  -configuration Debug \
  -destination 'generic/platform=iOS' \
  -derivedDataPath "$PWD/.build/DeployLinkDerivedData" \
  build
```

如果使用 workspace，将 `-project YourApp.xcodeproj` 替换为
`-workspace YourApp.xcworkspace`。成功的设备构建会生成已签名 `.app`，通常位于
`.build/DeployLinkDerivedData/Build/Products/Debug-iphoneos/`。

### 5. 通过 bridge 安装 Xcode 输出

bridge 激活后，已配对的设备可以留在不同的 Wi-Fi 网络，iPhone 也可以切换到
5G。安装已签名的构建结果：

```sh
"$deploy_link" install \
  --profile "$profile" \
  "$PWD/.build/DeployLinkDerivedData/Build/Products/Debug-iphoneos/YourApp.app"
```

较慢的公共网络可能需要五分钟以上。profile 中的 `install_timeout_seconds` 表示
“持续没有进度”的限制，而不是整个安装的总时限。payload 字节、Apple 安装阶段及
设备百分比的推进都会重置该限制；另有至少 30 分钟的硬性安全上限。若安装 stream
确实停滞，daemon 只会关闭该 stream，并保留 warm outer session。ZipConduit 不支持
断点续传，因此之后重试会从零开始。

短生命周期 CLI 会把请求交给常驻 daemon。只有 streaming zip conduit 完成，且
InstallationProxy 从设备回读到相同的 bundle identifier、version 和 build 后，
命令才会报告成功。之后的构建会复用同一个 warm generation。

### 6. 提交无人值守安装

如果希望提交后释放 terminal 或 agent，请使用 install job。`submit` 会验证已签名
App，制作仅限当前用户访问的 snapshot，记录完整内容的 SHA-256 digest，启动一个由 macOS 管理的一次性
LaunchAgent，并返回 job ID。之后 caller 即使断开，也不会中断该传输。

```sh
"$deploy_link" job submit --profile "$profile" /absolute/path/to/YourApp.app
"$deploy_link" job status --profile "$profile" <job-id>
"$deploy_link" job wait --profile "$profile" <job-id>
"$deploy_link" job remove --profile "$profile" <job-id>
```

`status` 读取持久 receipt。`wait` 会持续读取，直到状态变为 `installed`、
`failed` 或 `interrupted`。receipt 也可能显示 `queued` 或 `running`。每个 profile
只能有一个 active job；Deploy Link 会拒绝第二个提交，不会建立 queue。

一次性 trigger 使用 `RunAtLoad`，但不使用 `KeepAlive`。daemon 将 receipt 从
`queued` 持久写为 `running` 后，即使 trigger 断开，daemon 仍会负责传输和
receipt。失败或中断后不会自动重试。只有随机 daemon instance ID 和 session
generation 都没有变化、install count 恰好增加一，并且 InstallationProxy 回读到
完全相同的 bundle identifier、version 和 build，receipt 才会写入 `installed`。
daemon 在接管 job 前，也会验证 snapshot 的 bundle identity、version、build 和
content digest 都与提交时的 receipt 相同。

private snapshot 和 receipt 会一直保留，直到对 terminal job 明确执行 `remove`。
job 运行期间，Mac 必须保持唤醒并可访问，profile 的 warm session 也必须保持
可用。job 不依赖提交它的 caller，但正在运行的传输无法跨越 Apple session 丢失
或 Mac 关机。

常用命令：

```sh
"$deploy_link" status --profile "$profile"
"$deploy_link" watch --profile "$profile"
"$deploy_link" stop --profile "$profile"
"$deploy_link" launch-agent remove --profile "$profile"
```

对于受 supervisor 管理的 profile，`stop` 会回收当前 daemon，supervisor 随后会
重新启动它。若要停止并禁用该 profile 服务，请使用 `launch-agent remove`。

### 开发与验证

```sh
./scripts/test.sh
```

测试入口会验证固定的下游 patch、受影响的 Link Core package、Deploy Link 单元
测试、race test 和 `go vet`。所有权与失败边界见
[`docs/architecture.md`](docs/architecture.md)。

Deploy Link 使用 [MIT License](LICENSE) 发布。Link Core 衍生自
[`danielpaulus/go-ios`](https://github.com/danielpaulus/go-ios)，完整归属见
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)。
