import SwiftUI

struct ReleaseListView: View {
    @Environment(ReleaseStore.self) private var store
    @Environment(\.scenePhase) private var scenePhase
    @State private var showingSettings = false

    var body: some View {
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
        }
        // Poll while the app is in front so a new build shows up without a pull.
        .task(id: scenePhase) {
            guard scenePhase == .active else { return }
            while !Task.isCancelled {
                await store.refresh()
                try? await Task.sleep(for: .seconds(15))
            }
        }
    }

    private var checking: some View {
        ProgressView("Checking the Mac…")
            .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    @ViewBuilder
    private var content: some View {
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
                    Section {
                        ForEach(store.releases) { release in
                            ReleaseRow(release: release, requestedAt: store.installRequests[release.id]) {
                                store.install(release)
                            }
                        }
                    } footer: {
                        if case .loaded(let date) = store.state {
                            Text("Updated \(date, style: .relative) ago")
                        }
                    }
                }
            }
        }
    }
}

private struct ReleaseRow: View {
    let release: Release
    let requestedAt: Date?
    let install: () -> Void

    var body: some View {
        HStack(alignment: .center, spacing: 14) {
            icon
            VStack(alignment: .leading, spacing: 3) {
                Text(release.title)
                    .font(.headline)
                    .lineLimit(1)
                Text("\(release.version) (\(release.build))")
                    .font(.subheadline.monospacedDigit())
                    .foregroundStyle(.secondary)
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
            if release.isRunningBuild {
                Text("Running")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(.secondary)
            } else {
                Button("Install", action: install)
                    .buttonStyle(.borderedProminent)
                    .buttonBorderShape(.capsule)
            }
        }
        .padding(.vertical, 4)
    }

    @ViewBuilder
    private var status: some View {
        if release.isRunningBuild {
            Label("This is the build you're running", systemImage: "checkmark.circle.fill")
                .foregroundStyle(.green)
        } else if let downloaded = release.lastDownloaded {
            Label("Downloaded \(downloaded, style: .relative) ago", systemImage: "arrow.down.circle.fill")
                .foregroundStyle(.green)
        } else if let requestedAt {
            Label("Install started \(requestedAt, style: .relative) ago — tap Install on the iOS prompt", systemImage: "hourglass")
                .foregroundStyle(.orange)
        } else if release.isThisApp {
            Label("Update for this app", systemImage: "arrow.triangle.2.circlepath")
                .foregroundStyle(.blue)
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
        .frame(width: 56, height: 56)
        .clipShape(RoundedRectangle(cornerRadius: 13, style: .continuous))
    }
}

private struct SettingsView: View {
    @Environment(ReleaseStore.self) private var store
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        @Bindable var store = store
        NavigationStack {
            Form {
                Section {
                    TextField("https://…", text: $store.serverURL)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    Button("Use the Mac's default address") {
                        store.serverURL = ReleaseStore.defaultServer
                    }
                } header: {
                    Text("Mac address")
                } footer: {
                    Text("The Mac's private Tailscale address. Keep Tailscale connected on this iPhone; builds install over Wi-Fi or cellular.")
                }
                Section("This app") {
                    LabeledContent("Version", value: AppInfo.version)
                    LabeledContent("Build", value: AppInfo.build)
                }
            }
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") {
                        dismiss()
                        Task { await store.refresh() }
                    }
                }
            }
        }
    }
}
