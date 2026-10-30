import AppKit
import SwiftUI

@main
struct DeployLinkMacApp: App {
    @State private var queue = QueueModel()
    var body: some Scene {
        Window("Deploy Link", id: "main") {
            QueueView(queue: queue)
        }
        .defaultSize(width: 660, height: 710)
        .windowResizability(.contentMinSize)

        MenuBarExtra("Deploy Link", systemImage: "arrow.down.app") {
            QueueMenu(queue: queue)
        }
    }
}

private struct QueueMenu: View {
    @Bindable var queue: QueueModel
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        Text(queue.statusText)
        if queue.snapshot.running { Text("\(queue.snapshot.releases.count) builds in your queue") }
        Divider()
        Button("Open Deploy Link") {
            openWindow(id: "main")
            NSApplication.shared.activate(ignoringOtherApps: true)
        }
        Button("Add Build…") {
            openWindow(id: "main")
            NSApplication.shared.activate(ignoringOtherApps: true)
            queue.chooseBuild()
        }.disabled(!queue.snapshot.configured || queue.busy)
        Button("Open Queue", action: queue.openQueue).disabled(!queue.snapshot.running)
        Button("Copy Address", action: queue.copyAddress).disabled(queue.snapshot.queueURL.isEmpty)
        Divider()
        if queue.snapshot.configured {
            Button(queue.snapshot.running ? "Stop Server" : "Start Server") {
                queue.perform([queue.snapshot.running ? "stop" : "start"])
            }.disabled(queue.busy)
        }
        Toggle("Launch at Login", isOn: Binding(get: { queue.loginStatus == .enabled }, set: queue.setLaunchAtLogin))
        Divider()
        Button("Quit Deploy Link") { NSApplication.shared.terminate(nil) }
    }
}

private struct QueueView: View {
    @Bindable var queue: QueueModel

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                HStack(spacing: 16) {
                    Image(nsImage: NSImage(named: NSImage.applicationIconName) ?? NSImage())
                        .resizable().frame(width: 62, height: 62)
                    VStack(alignment: .leading, spacing: 5) {
                        Text("Your builds, ready to install.").font(.title2.bold())
                        Text("A private queue for your iPhone and iPad.").foregroundStyle(.secondary)
                    }
                    Spacer()
                    if queue.busy || queue.loading { ProgressView().controlSize(.small) }
                }
                GroupBox {
                    VStack(alignment: .leading, spacing: 14) {
                        HStack {
                            Label(queue.statusText, systemImage: queue.snapshot.running ? "checkmark.circle.fill" : "circle")
                                .foregroundStyle(queue.snapshot.running ? Color.green : Color.secondary)
                                .font(.headline)
                            Spacer()
                            if queue.snapshot.configured {
                                Button(queue.snapshot.running ? "Stop Server" : "Start Server") {
                                    queue.perform([queue.snapshot.running ? "stop" : "start"])
                                }.disabled(queue.busy)
                            } else {
                                Button("Set Up Server") { queue.perform(["setup"], success: "Your private queue is ready.") }
                                    .buttonStyle(.borderedProminent).tint(.teal)
                                    .disabled(queue.busy || queue.loading)
                            }
                        }
                        if !queue.snapshot.queueURL.isEmpty {
                            Text(queue.snapshot.queueURL).font(.callout.monospaced()).textSelection(.enabled)
                            HStack {
                                Button("Copy Address", action: queue.copyAddress)
                                Button("Open Queue", action: queue.openQueue).disabled(!queue.snapshot.running)
                            }
                            Text("Paste this address into Deploy Link on your iPhone or iPad. Connect both devices to your Tailscale network.")
                                .font(.callout).foregroundStyle(.secondary)
                        } else {
                            Text("Setup installs the server for your Mac login and uses Tailscale for private HTTPS access. Existing builds and settings are preserved.")
                                .font(.callout).foregroundStyle(.secondary)
                        }
                    }.frame(maxWidth: .infinity, alignment: .leading).padding(8)
                }
                if let error = queue.error {
                    Label(error, systemImage: "exclamationmark.triangle").foregroundStyle(.red)
                        .textSelection(.enabled)
                }
                if let notice = queue.notice {
                    Label(notice, systemImage: "checkmark.circle").foregroundStyle(.secondary)
                }
                if queue.snapshot.configured {
                    VStack(alignment: .leading, spacing: 12) {
                        HStack {
                            Text("Build queue").font(.headline)
                            Spacer()
                            Button("Add Build…", action: queue.chooseBuild).disabled(queue.busy)
                        }
                        if queue.snapshot.releases.isEmpty {
                            Text(queue.snapshot.running ? "Add a signed iOS .app or .ipa to get started." : "Start the server to see your builds.")
                                .foregroundStyle(.secondary).padding(.vertical, 12)
                        }
                        ForEach(queue.snapshot.releases) { release in
                            VStack(alignment: .leading, spacing: 4) {
                                HStack {
                                    Text(release.title).fontWeight(.semibold)
                                    Spacer()
                                    Text("\(release.version) (\(release.build))").foregroundStyle(.secondary)
                                }
                                if !release.note.isEmpty { Text(release.note).font(.callout).foregroundStyle(.secondary) }
                                Divider()
                            }
                        }
                        Text("Builds need valid Apple signing and provisioning for their intended devices. Open the installed app on the device to confirm installation.")
                            .font(.caption).foregroundStyle(.secondary)
                    }
                }
                GroupBox("Mac setup") {
                    VStack(alignment: .leading, spacing: 12) {
                        prerequisite("Full Xcode selected", ready: queue.snapshot.xcode, link: "https://developer.apple.com/xcode/")
                        prerequisite("Tailscale connected", ready: queue.snapshot.connected, link: "https://tailscale.com/download/mac")
                        Toggle("Launch Deploy Link at login", isOn: Binding(get: { queue.loginStatus == .enabled }, set: queue.setLaunchAtLogin))
                        if queue.loginStatus == .requiresApproval {
                            Text("Allow Deploy Link in System Settings → General → Login Items.").font(.caption).foregroundStyle(.secondary)
                        }
                        if queue.snapshot.configured {
                            Toggle("Start the queue at login", isOn: Binding(get: { queue.snapshot.queueAtLogin }, set: {
                                queue.perform(["login", "--enabled", $0 ? "yes" : "no"])
                            })).disabled(queue.busy)
                            Text("Closing this window or quitting Deploy Link leaves the queue running. Stop Server pauses it; the login setting controls the next sign-in.")
                                .font(.caption).foregroundStyle(.secondary)
                            Button("Copy Management Access") {
                                queue.perform(["copy-management"], success: "Management access copied. Paste it into your iOS settings, then clear your clipboard. Keep it private.")
                            }.disabled(queue.busy)
                            Text("Optional: permits removing builds from your phone. Keep this access private.")
                                .font(.caption).foregroundStyle(.secondary)
                        }
                    }.frame(maxWidth: .infinity, alignment: .leading).padding(8)
                }
                HStack {
                    Link("Setup guide", destination: URL(string: "https://alexmorris10x.github.io/deploy-link/setup.html")!)
                    Link("Support", destination: URL(string: "https://alexmorris10x.github.io/deploy-link/support.html")!)
                    Spacer()
                    Button("Refresh") { Task { await queue.refresh() } }.disabled(queue.busy || queue.loading)
                }.font(.callout)
            }.padding(26)
        }
        .frame(minWidth: 560, minHeight: 540)
        .sheet(isPresented: Binding(get: { queue.selectedBuild != nil }, set: { if !$0 { queue.selectedBuild = nil } })) {
            VStack(alignment: .leading, spacing: 18) {
                Text("Add a build").font(.title2.bold())
                Text(queue.selectedBuild?.lastPathComponent ?? "").textSelection(.enabled)
                TextField("Release note (optional)", text: $queue.releaseNote)
                Text("The newest build replaces older queued builds for this app. Installed apps on your devices are unchanged.")
                    .font(.callout).foregroundStyle(.secondary)
                HStack {
                    Button("Cancel") { queue.selectedBuild = nil }.keyboardShortcut(.cancelAction)
                    Spacer()
                    Button("Add Build", action: queue.publish).keyboardShortcut(.defaultAction)
                }
            }.padding(24).frame(width: 430)
        }
    }

    private func prerequisite(_ title: String, ready: Bool, link: String) -> some View {
        HStack {
            Label(title, systemImage: ready ? "checkmark.circle.fill" : "circle")
                .foregroundStyle(ready ? Color.green : Color.secondary)
            Spacer()
            if !ready { Link("Set up", destination: URL(string: link)!) }
        }
    }
}
