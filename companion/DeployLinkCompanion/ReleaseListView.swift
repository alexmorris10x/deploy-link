import SwiftUI

struct ReleaseListView: View {
    @Environment(ReleaseStore.self) private var store
    @Environment(\.scenePhase) private var scenePhase
    @State private var showingSettings = false
    @State private var confirming: Confirmation?

    /// An install or removal that needs a second tap.
    struct Confirmation: Identifiable {
        enum Kind { case reinstall, downgrade, delete }
        let kind: Kind
        let release: Release
        let installed: InstalledApp?
        var id: String { "\(kind)-\(release.id)" }
    }

    var body: some View {
        @Bindable var store = store
        NavigationStack {
            content
                .navigationTitle("Builds")
                .toolbar {
                    ToolbarItem(placement: .topBarTrailing) {
                        Button("Settings", systemImage: "gearshape") { showingSettings = true }
                    }
                }
                .refreshable { await store.refresh() }
                .sheet(isPresented: $showingSettings) {
                    SettingsView()
                        .environment(store)
                }
                .alert(item: $confirming) { confirmation in
                    alert(for: confirmation)
                }
                .alert("Not available", isPresented: Binding(get: { store.notice != nil }, set: { if !$0 { store.notice = nil } })) {
                    Button("OK", role: .cancel) {}
                } message: {
                    Text(store.notice ?? "")
                }
        }
        // Poll while the app is in front: every 2 seconds while a download is running, otherwise every 15.
        .task(id: scenePhase) {
            guard scenePhase == .active else { return }
            while !Task.isCancelled {
                await store.refresh()
                let busy = store.releases.contains { release in
                    switch store.installState(of: release) {
                    case .waiting, .downloading: true
                    default: false
                    }
                }
                try? await Task.sleep(for: .seconds(busy ? 2 : 15))
            }
        }
    }

    private var checking: some View {
        ProgressView("Checking the Mac…")
            .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    @ViewBuilder
    private var content: some View {
        if !store.hasServer {
            ContentUnavailableView {
                Label("Your builds, from your Mac", systemImage: "desktopcomputer")
            } description: {
                Text("Install the free Deploy Link server on your Mac, connect both devices to Tailscale, then enter the HTTPS address from setup. Your Mac must stay awake. Builds must be signed for this device.")
            } actions: {
                Button("Connect Your Mac") { showingSettings = true }
                    .buttonStyle(.borderedProminent)
                    .accessibilityIdentifier("deploylink.connect")
                Link("Mac setup guide", destination: ReleaseStore.setupURL)
            }
        } else {
        switch store.state {
        case .idle:
            checking
        case .loading where store.releases.isEmpty:
            checking
        case .failed(let message) where store.releases.isEmpty:
            ContentUnavailableView {
                Label("Can't load builds", systemImage: "wifi.exclamationmark")
            } description: {
                Text(message)
            } actions: {
                Button("Try Again") { Task { await store.refresh() } }
                    .buttonStyle(.borderedProminent)
            }
        default:
            List {
                if case .failed(let message) = store.state {
                    Section {
                        Label(message, systemImage: "exclamationmark.triangle")
                            .foregroundStyle(.orange)
                    }
                }
                if store.releases.isEmpty {
                    ContentUnavailableView(
                        "No builds waiting",
                        systemImage: "tray",
                        description: Text("Builds the Mac publishes to the queue appear here.")
                    )
                } else {
                    ForEach(store.apps, id: \.first!.bundleIdentifier) { releases in
                        AppSection(releases: releases, confirm: { confirming = $0 })
                    }
                    if case .loaded(let date) = store.state {
                        Section {
                        } footer: {
                            Text("Updated \(date, style: .relative) ago")
                        }
                    }
                }
            }
        }
        }
    }

    private func alert(for confirmation: Confirmation) -> Alert {
        let release = confirmation.release
        let device = store.family
        switch confirmation.kind {
        case .reinstall:
            return Alert(
                title: Text("Install \(release.versionLabel) again?"),
                message: Text("This \(device) previously downloaded \(release.title) \(release.versionLabel). iOS does not report whether installation completed."),
                primaryButton: .default(Text("Install Again")) { Task { await store.install(release) } },
                secondaryButton: .cancel()
            )
        case .downgrade:
            return Alert(
                title: Text("Install an older build?"),
                message: Text("This \(device) last downloaded \(confirmation.installed?.versionLabel ?? "a newer build"). \(release.versionLabel) is older; installation may replace the newer app."),
                primaryButton: .destructive(Text("Install Older Build")) { Task { await store.install(release) } },
                secondaryButton: .cancel()
            )
        case .delete:
            return Alert(
                title: Text("Remove \(release.versionLabel)?"),
                message: Text("This deletes \(release.title) \(release.versionLabel) from the Mac's queue for every device. Apps already installed stay installed."),
                primaryButton: .destructive(Text("Remove")) { Task { await store.delete(release) } },
                secondaryButton: .cancel()
            )
        }
    }
}

/// One app: what this device has, the newest build, and older builds folded away.
private struct AppSection: View {
    @Environment(ReleaseStore.self) private var store
    let releases: [Release]
    let confirm: (ReleaseListView.Confirmation) -> Void
    @State private var showingOlder = false

    var body: some View {
        let latest = releases[0]
        let older = Array(releases.dropFirst())
        Section {
            row(latest, isLatest: true)
            if !older.isEmpty {
                DisclosureGroup("Older builds (\(older.count))", isExpanded: $showingOlder) {
                    ForEach(older) { row($0, isLatest: false) }
                }
                .font(.subheadline)
            }
        } header: {
            Text(latest.title)
        } footer: {
            onThisDevice(latest.bundleIdentifier)
        }
    }

    private func row(_ release: Release, isLatest: Bool) -> some View {
        let installed = store.installedBuild(of: release.bundleIdentifier)
        return ReleaseRow(release: release, isLatest: isLatest, state: store.installState(of: release)) { state in
            switch state {
            case .downloaded: confirm(.init(kind: .reinstall, release: release, installed: installed))
            case .older: confirm(.init(kind: .downgrade, release: release, installed: installed))
            default: Task { await store.install(release) }
            }
        }
        .swipeActions {
            if store.canManage {
                Button("Remove", systemImage: "trash", role: .destructive) {
                    confirm(.init(kind: .delete, release: release, installed: installed))
                }
            }
        }
    }

    @ViewBuilder
    private func onThisDevice(_ bundleIdentifier: String) -> some View {
        if let installed = store.installedBuild(of: bundleIdentifier) {
            let gone = installed.available ? "" : " — no longer in the queue"
            if let date = installed.date {
                Text("Downloaded on this \(store.family): \(installed.versionLabel), \(date, style: .relative) ago\(gone). Installation is not confirmed.")
            } else {
                Text("Running on this \(store.family): \(installed.versionLabel)\(gone)")
            }
        } else {
            Text("No completed download recorded for this \(store.family)")
        }
    }
}

private struct ReleaseRow: View {
    let release: Release
    let isLatest: Bool
    let state: InstallState
    let tap: (InstallState) -> Void

    var body: some View {
        HStack(alignment: .center, spacing: 14) {
            icon
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(release.versionLabel)
                        .font(.headline.monospacedDigit())
                        .lineLimit(1)
                    if isLatest {
                        Text("Latest")
                            .font(.caption2.weight(.semibold))
                            .padding(.horizontal, 6)
                            .padding(.vertical, 2)
                            .background(.blue.opacity(0.15), in: Capsule())
                            .foregroundStyle(.blue)
                    }
                }
                if let created = release.created {
                    Text("Built \(created, style: .relative) ago · \(ByteCountFormatter.string(fromByteCount: Int64(release.ipaBytes), countStyle: .file))")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                if !release.note.isEmpty {
                    Text(release.note)
                        .font(.caption)
                }
                status
                    .font(.caption.weight(.medium))
            }
            Spacer(minLength: 8)
            button
        }
        .padding(.vertical, 4)
    }

    @ViewBuilder
    private var button: some View {
        switch state {
        case .running:
            Text("Running")
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(.secondary)
        case .downloading(let fraction):
            ProgressView(value: fraction)
                .progressViewStyle(.circular)
                .overlay { Text("\(Int(fraction * 100))").font(.system(size: 9).monospacedDigit()) }
                .frame(width: 34, height: 34)
        case .waiting:
            ProgressView()
                .frame(width: 34, height: 34)
        case .downloaded:
            Button("Downloaded") { tap(state) }
                .buttonStyle(.bordered)
                .buttonBorderShape(.capsule)
        case .older, .busy:
            Button("Install") { tap(state) }
                .buttonStyle(.bordered)
                .buttonBorderShape(.capsule)
                .disabled(state == .busy)
        case .install, .update:
            Button(state == .update ? "Update" : "Install") { tap(state) }
                .buttonStyle(.borderedProminent)
                .buttonBorderShape(.capsule)
        }
    }

    @ViewBuilder
    private var status: some View {
        switch state {
        case .running:
            Label("The build you're running", systemImage: "checkmark.circle.fill")
                .foregroundStyle(.green)
        case .downloaded:
            Label("Downloaded; installation unconfirmed", systemImage: "arrow.down.circle.fill")
                .foregroundStyle(.green)
        case .waiting:
            Label("Tap Install on the iOS prompt", systemImage: "hourglass")
                .foregroundStyle(.orange)
        case .downloading:
            Label("Downloading from the Mac…", systemImage: "arrow.down.circle")
                .foregroundStyle(.blue)
        case .busy:
            Label("Another build of this app is downloading", systemImage: "hourglass")
                .foregroundStyle(.secondary)
        case .older:
            Label("Older than the last downloaded build", systemImage: "arrow.uturn.backward.circle")
                .foregroundStyle(.secondary)
        case .install, .update:
            if let others = release.downloadedBy, !others.isEmpty {
                Label("Downloaded on \(others.map(\.device).joined(separator: ", "))", systemImage: "arrow.down.circle")
                    .foregroundStyle(.secondary)
            }
        }
    }

    private var icon: some View {
        AsyncImage(url: release.iconURL) { image in
            image.resizable().scaledToFill()
        } placeholder: {
            Image(systemName: "app.dashed")
                .font(.title)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(.quaternary)
        }
        .frame(width: 48, height: 48)
        .clipShape(RoundedRectangle(cornerRadius: 11, style: .continuous))
    }
}

private struct SettingsView: View {
    @Environment(ReleaseStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    @State private var address = ""
    @State private var token = ""
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("https://your-mac.your-tailnet.ts.net", text: $address)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .accessibilityIdentifier("deploylink.server")
                    Link("Set up your Mac", destination: ReleaseStore.setupURL)
                } header: { Text("Mac address") } footer: {
                    Text("Use the HTTPS address printed by the Mac installer. Connect Tailscale on both devices. The Mac must be awake and the builds must be signed for your device.")
                }
                Section {
                    SecureField("Management token (optional)", text: $token)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    if store.canManage {
                        Label("A token is saved for your current Mac", systemImage: "key.fill")
                        Button("Remove saved token", role: .destructive) { store.forgetManagementToken() }
                    }
                } header: { Text("Manage the queue") } footer: {
                    Text("Only needed to remove builds. On your Mac, run ota-queue admin-token --copy, then paste here. The token stays in this device’s Keychain. Leave blank to keep an existing token for this address.")
                }
                Section("About") {
                    LabeledContent("Version", value: AppInfo.version)
                    LabeledContent("Build", value: AppInfo.build)
                    Link("Privacy", destination: URL(string: "https://alexmorris10x.github.io/deploy-link/privacy.html")!)
                    Link("Support", destination: URL(string: "https://alexmorris10x.github.io/deploy-link/support.html")!)
                    Text("Downloaded means the transfer completed. Confirm installation on your Home Screen or in the installed app.")
                        .font(.footnote).foregroundStyle(.secondary)
                }
            }
            .navigationTitle(store.hasServer ? "Settings" : "Connect your Mac")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") {
                        if store.configure(address: address, token: token) {
                            token = ""
                            dismiss()
                            Task { await store.refresh() }
                        } else {
                            error = store.notice
                            store.notice = nil
                        }
                    }
                    .disabled(address.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
            .onAppear { address = store.serverURL }
            .alert("Could not save", isPresented: Binding(get: { error != nil }, set: { if !$0 { error = nil } })) {
                Button("OK", role: .cancel) {}
            } message: { Text(error ?? "") }
        }
    }
}
