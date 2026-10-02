import AppKit
import CryptoKit
import Foundation
import SwiftUI
import UniformTypeIdentifiers

@main
struct DeployLinkApp: App {
    @NSApplicationDelegateAdaptor(DeployLinkAppDelegate.self) private var appDelegate
    @StateObject private var store = DeployLinkStore()
    @AppStorage("showMenuBar") private var showMenuBar = true

    var body: some Scene {
        WindowGroup("Deploy Link", id: "dashboard") {
            DeployLinkDashboard()
                .environmentObject(store)
                .frame(minWidth: 680, minHeight: 520)
        }
        .defaultSize(width: 760, height: 620)
        .commands {
            CommandGroup(replacing: .newItem) { }
            CommandGroup(after: .appInfo) {
                Button("Refresh") { store.refresh() }
                    .keyboardShortcut("r")
            }
        }
        MenuBarExtra(isInserted: $showMenuBar) {
            DeployLinkMenu()
                .environmentObject(store)
        } label: {
            Image(systemName: store.menuBarSymbol)
                .foregroundStyle(store.menuBarColor)
                .accessibilityLabel(store.menuBarAccessibilityLabel)
        }
        .menuBarExtraStyle(.menu)
    }
}

final class DeployLinkAppDelegate: NSObject, NSApplicationDelegate {
    private let hideFromDockKey = "hideFromDock"

    func applicationDidFinishLaunching(_ notification: Notification) {
        UserDefaults.standard.register(defaults: [hideFromDockKey: true])
        setDockVisibility(hidden: UserDefaults.standard.bool(forKey: hideFromDockKey))
    }

    func setDockVisibility(hidden: Bool) {
        _ = NSApp.setActivationPolicy(hidden ? .accessory : .regular)
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }
}

struct DeployLinkMenu: View {
    @EnvironmentObject private var store: DeployLinkStore
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        ForEach(store.devices) { device in
            Section(device.deviceLabel) {
                Label {
                    HStack(spacing: 6) {
                        Text(device.bridgeLabel)
                        if device.isConnected {
                            Image(systemName: "checkmark")
                                .foregroundStyle(.green)
                                .accessibilityHidden(true)
                        }
                    }
                } icon: {
                    Image(systemName: device.deviceSymbol)
                }
                Button("Check & Reconnect") { store.checkAndReconnect(device) }
                    .disabled(store.busyDeviceIDs.contains(device.id))
                if device.needsPairingRepair {
                    Button("Repair Pairing…") { store.repairPairing(device) }
                        .disabled(store.busyDeviceIDs.contains(device.id))
                }
            }
        }
        if store.devices.isEmpty { Text("No device profiles") }
        Button("Import Profile…") { store.importProfile() }.disabled(store.isImporting)
        Button("Setup Guide") { store.openSetupGuide() }
        ForEach(store.profileErrors, id: \.self) { Text($0) }
        if let message = store.actionMessage {
            Divider()
            Text(message)
        }
        Divider()
        Button("Open Deploy Link") {
            openWindow(id: "dashboard")
            NSApp.activate(ignoringOtherApps: true)
        }
        Button("Refresh") { store.refresh() }
        Divider()
        Button("Quit Deploy Link") { NSApp.terminate(nil) }
    }
}

@MainActor
final class DeployLinkStore: ObservableObject {
    @Published private(set) var devices: [DeviceInstallStatus] = []
    @Published private(set) var busyDeviceIDs: Set<String> = []
    @Published var selectedDeviceID: String?
    @Published var alertMessage: String?
    @Published var actionMessage: String?
    @Published private(set) var profileErrors: [String] = []
    @Published private(set) var preparation: [String: String] = [:]
    @Published private(set) var isImporting = false
    private var commands: [String: Task<Void, Never>] = [:]
    private var lastRefresh = Date.distantPast

    private let reader = DeployLinkStateReader()
    private let runner = DeployLinkCommandRunner()
    private var refreshTimer: Timer?
    private var refreshInFlight = false

    init() {
        refresh()
        refreshTimer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in
            Task { @MainActor in
                guard let self else { return }
                let visible = NSApp.windows.contains { $0.isVisible && !$0.isMiniaturized }
                let interval: TimeInterval = visible || self.hasActiveTransfer || !self.busyDeviceIDs.isEmpty ? 2 : 15
                if Date().timeIntervalSince(self.lastRefresh) >= interval { self.refresh(force: false) }
            }
        }
    }

    func refresh(force: Bool = true) {
        guard !refreshInFlight else { return }
        refreshInFlight = true
        lastRefresh = Date()
        Task {
            let result = await Task.detached(priority: .utility) { [reader] in
                reader.readStatuses(force: force)
            }.value
            profileErrors = result.errors
            let newDevices = result.devices
            devices = newDevices
            if selectedDeviceID == nil || !newDevices.contains(where: { $0.id == selectedDeviceID }) {
                selectedDeviceID = newDevices.first?.id
            }
            refreshInFlight = false
        }
    }

    func start(_ device: DeviceInstallStatus) {
        run(
            device: device,
            arguments: ["launch-agent", "install", "--profile", device.profilePath],
            successMessage: "Started \(device.deviceLabel); waiting for the device to reconnect."
        )
    }

    func stop(_ device: DeviceInstallStatus) {
        run(
            device: device,
            arguments: ["launch-agent", "remove", "--profile", device.profilePath],
            successMessage: "Stopped \(device.deviceLabel)."
        )
    }

    var menuBarSymbol: String {
        if hasActiveTransfer { return "arrow.up.circle.fill" }
        return allDevicesConnected ? "checkmark.circle.fill" : "xmark.circle.fill"
    }

    var menuBarColor: Color {
        if hasActiveTransfer { return .orange }
        return allDevicesConnected ? .green : .red
    }

    var menuBarAccessibilityLabel: String {
        if hasActiveTransfer { return "Deploy Link, transferring" }
        return allDevicesConnected ? "Deploy Link, connected" : "Deploy Link, disconnected"
    }

    private var hasActiveTransfer: Bool {
        devices.contains { $0.job?.isActive == true }
    }

    var allDevicesConnected: Bool {
        !devices.isEmpty && devices.allSatisfy(\.isConnected)
    }

    func checkAndReconnect(_ device: DeviceInstallStatus) {
        if device.bridgeState == "active" {
            actionMessage = "\(device.deviceLabel) is ready."
        } else if !device.serviceEnabled {
            actionMessage = "Starting \(device.deviceLabel)…"
            start(device)
        } else if device.lastErrorCode == "remote_pairing_cold" {
            actionMessage = "Join any Wi-Fi on \(device.deviceLabel); Deploy Link is already waiting to reconnect."
        } else if device.needsPairingRepair {
            actionMessage = "Connect \(device.deviceLabel) by USB, unlock it, then choose Repair Pairing."
        } else {
            actionMessage = "\(device.deviceLabel) is reconnecting."
            refresh()
        }
    }

    func repairPairing(_ device: DeviceInstallStatus) {
        actionMessage = "Repairing \(device.deviceLabel)…"
        run(
            device: device,
            arguments: ["pair", "repair", "--profile", device.profilePath],
            successMessage: "\(device.deviceLabel) is paired and ready."
        )
    }

    func submit(_ appURL: URL) {
        guard let device = devices.first(where: { $0.id == selectedDeviceID }) else {
            alertMessage = "Choose a device before submitting an app."
            return
        }
        guard device.serviceEnabled else {
            alertMessage = "Start the bridge for \(device.deviceLabel) before submitting an app."
            return
        }
        guard !busyDeviceIDs.contains(device.id) else { return }
        busyDeviceIDs.insert(device.id)
        preparation[device.id] = "Validating app signature…"
        commands[device.id] = Task {
            do {
                try await runner.validateSignedIOSApp(at: appURL)
                try Task.checkCancellation()
                preparation[device.id] = "Preparing and copying the app snapshot…"
                _ = try await runner.run(["job", "submit", "--profile", device.profilePath, appURL.path])
                actionMessage = "App submitted to \(device.deviceLabel)."
                refresh()
            } catch is CancellationError {
                actionMessage = "UI command cancelled. The bridge stays running; refresh to check any job already submitted."
            } catch { alertMessage = error.localizedDescription }
            preparation.removeValue(forKey: device.id)
            busyDeviceIDs.remove(device.id)
            commands.removeValue(forKey: device.id)
        }
    }

    private func run(device: DeviceInstallStatus, arguments: [String], successMessage: String) {
        guard !busyDeviceIDs.contains(device.id) else { return }
        busyDeviceIDs.insert(device.id)
        commands[device.id] = Task {
            do {
                _ = try await runner.run(arguments)
                actionMessage = successMessage
                refresh()
            } catch is CancellationError {
                actionMessage = "UI command cancelled; the device bridge is independent."
            } catch {
                alertMessage = error.localizedDescription
                actionMessage = error.localizedDescription
            }
            busyDeviceIDs.remove(device.id)
            commands.removeValue(forKey: device.id)
        }
    }

    func cancel(_ device: DeviceInstallStatus) { commands[device.id]?.cancel() }
    func chooseApp() {
        let panel = NSOpenPanel()
        panel.title = "Choose a signed iOS app"
        panel.allowedContentTypes = [.applicationBundle]
        panel.canChooseDirectories = true
        panel.canChooseFiles = true
        panel.treatsFilePackagesAsDirectories = false
        panel.allowsMultipleSelection = false
        if panel.runModal() == .OK, let url = panel.url { submit(url) }
    }
    func openSetupGuide() {
        NSWorkspace.shared.open(URL(string: "https://github.com/alexmorris10x/deploy-link#pair-and-configure-each-device")!)
    }
    func importProfile() {
        let panel = NSOpenPanel(); panel.title = "Import a Deploy Link profile"
        panel.allowedContentTypes = [.json]; panel.allowsMultipleSelection = false
        guard panel.runModal() == .OK, let source = panel.url else { return }
        isImporting = true
        Task {
            let home = FileManager.default.homeDirectoryForCurrentUser
            let root = home.appendingPathComponent(".local/share/deploy-link/profiles", isDirectory: true)
            let temporary = root.appendingPathComponent(".import-" + UUID().uuidString + ".pending")
            defer { try? FileManager.default.removeItem(at: temporary); isImporting = false }
            do {
                try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
                let data = try Data(contentsOf: source)
                _ = try JSONDecoder().decode(DeployLinkProfile.self, from: data)
                guard FileManager.default.createFile(atPath: temporary.path, contents: data, attributes: [.posixPermissions: 0o600]) else { throw DeployLinkAppError("Could not stage the imported profile.") }
                _ = try await runner.run(["validate-profile", "--profile", temporary.path])
                let destination = root.appendingPathComponent(source.deletingPathExtension().lastPathComponent + "-" + UUID().uuidString.prefix(8) + ".json")
                try FileManager.default.moveItem(at: temporary, to: destination)
                actionMessage = "Profile imported. Select Start when the device is ready."
                refresh()
            } catch { alertMessage = "Profile import failed: " + error.localizedDescription }
        }
    }
}

struct DeployLinkDashboard: View {
    @EnvironmentObject private var store: DeployLinkStore
    @StateObject private var launchAtLogin = LaunchAtLoginController()
    @AppStorage("showMenuBar") private var showMenuBar = true
    @AppStorage("hideFromDock") private var hideFromDock = true
    @State private var isDropTargeted = false
    @State private var pendingStop: DeviceInstallStatus?

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    deviceSection
                    installSection
                    settingsSection
                }
                .padding(24)
            }
        }
        .background(Color(nsColor: .windowBackgroundColor))
        .alert("Deploy Link", isPresented: alertBinding) {
            Button("OK", role: .cancel) { store.alertMessage = nil }
        } message: {
            Text(store.alertMessage ?? "")
        }
        .confirmationDialog(
            "Stop this bridge?",
            isPresented: stopDialogBinding,
            titleVisibility: .visible,
            presenting: pendingStop
        ) { device in
            Button("Stop \(device.deviceLabel)", role: .destructive) {
                store.stop(device)
                pendingStop = nil
            }
            Button("Cancel", role: .cancel) { pendingStop = nil }
        } message: { device in
            Text("Stopping disconnects the warm Apple session for \(device.deviceLabel). A fresh connection may require the device to be on Wi-Fi again.")
        }
    }

    private var header: some View {
        HStack(spacing: 14) {
            Image(systemName: "link.circle.fill")
                .font(.system(size: 32))
                .foregroundStyle(.tint)
            VStack(alignment: .leading, spacing: 2) {
                Text("Deploy Link").font(.title2.weight(.semibold))
                Text("Install signed development builds on your paired devices")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            Button { store.refresh() } label: {
                Label("Refresh", systemImage: "arrow.clockwise")
            }
        }
        .padding(.horizontal, 24)
        .padding(.vertical, 18)
    }

    @ViewBuilder
    private var deviceSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("Devices").font(.headline)
                Spacer()
                Button("Import Profile…") { store.importProfile() }.disabled(store.isImporting)
                Button("Setup Guide") { store.openSetupGuide() }
            }
            ForEach(store.profileErrors, id: \.self) { error in
                Label(error, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.red).textSelection(.enabled)
            }
            if store.devices.isEmpty {
                VStack(spacing: 8) {
                    Image(systemName: "iphone.slash").font(.largeTitle).foregroundStyle(.secondary)
                    Text("No Device Profiles").font(.headline)
                    Text("Use the setup guide to pair a device, then import its profile.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, minHeight: 170)
            } else {
                ForEach(store.devices) { device in
                    DeviceCard(
                        device: device,
                        isBusy: store.busyDeviceIDs.contains(device.id),
                        start: { store.start(device) },
                        stop: { pendingStop = device },
                        cancel: { store.cancel(device) },
                        preparation: store.preparation[device.id]
                    )
                }
            }
        }
    }

    private var installSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Install an app").font(.headline)
            VStack(spacing: 14) {
                HStack {
                    Text("Device")
                    Spacer()
                    Picker("Device", selection: $store.selectedDeviceID) {
                        ForEach(store.devices) { device in
                            Text(device.deviceLabel).tag(Optional(device.id))
                        }
                    }
                    .labelsHidden()
                    .frame(maxWidth: 280)
                }
                VStack(spacing: 8) {
                    Image(systemName: isDropTargeted ? "arrow.down.app.fill" : "arrow.down.app")
                        .font(.system(size: 32))
                        .foregroundStyle(isDropTargeted ? Color.accentColor : .secondary)
                    Text("Drop a signed iOS .app here").font(.headline)
                    Button("Choose App…") { store.chooseApp() }
                        .keyboardShortcut("o", modifiers: .command)
                        .disabled(store.selectedDeviceID == nil || store.busyDeviceIDs.contains(store.selectedDeviceID ?? ""))
                    Text("Deploy Link validates the bundle and submits a durable install job.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, minHeight: 120)
                .background(
                    RoundedRectangle(cornerRadius: 12)
                        .fill(isDropTargeted ? Color.accentColor.opacity(0.10) : Color(nsColor: .controlBackgroundColor))
                )
                .overlay(
                    RoundedRectangle(cornerRadius: 12)
                        .strokeBorder(isDropTargeted ? Color.accentColor : Color.secondary.opacity(0.25), style: StrokeStyle(lineWidth: 1, dash: [6]))
                )
                .onDrop(of: [UTType.fileURL.identifier], isTargeted: $isDropTargeted, perform: handleDrop)
            }
            .padding(16)
            .background(.background, in: RoundedRectangle(cornerRadius: 14))
            .overlay(RoundedRectangle(cornerRadius: 14).stroke(Color.secondary.opacity(0.16)))
        }
    }

    private var settingsSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("App").font(.headline)
            Toggle("Open Deploy Link at login", isOn: Binding(
                get: { launchAtLogin.isEnabled },
                set: { launchAtLogin.setEnabled($0) }
            ))
            .disabled(launchAtLogin.isChanging)
            Toggle("Show in Menu Bar", isOn: $showMenuBar)
            Toggle("Hide from Dock", isOn: $hideFromDock)
                .onChange(of: hideFromDock) { hidden in
                    NSApp.setActivationPolicy(hidden ? .accessory : .regular)
                }
            Text("Keep Deploy Link available from the menu bar without a Dock icon.")
                .font(.caption)
                .foregroundStyle(.secondary)
            if let note = launchAtLogin.statusNote {
                Text(note).font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(16)
        .background(.background, in: RoundedRectangle(cornerRadius: 14))
        .overlay(RoundedRectangle(cornerRadius: 14).stroke(Color.secondary.opacity(0.16)))
    }

    private var alertBinding: Binding<Bool> {
        Binding(get: { store.alertMessage != nil }, set: { if !$0 { store.alertMessage = nil } })
    }

    private var stopDialogBinding: Binding<Bool> {
        Binding(get: { pendingStop != nil }, set: { if !$0 { pendingStop = nil } })
    }

    private func handleDrop(_ providers: [NSItemProvider]) -> Bool {
        guard let provider = providers.first else { return false }
        provider.loadItem(forTypeIdentifier: UTType.fileURL.identifier, options: nil) { item, _ in
            let url: URL?
            if let data = item as? Data {
                url = URL(dataRepresentation: data, relativeTo: nil)
            } else if let value = item as? URL {
                url = value
            } else if let value = item as? NSURL {
                url = value as URL
            } else {
                url = nil
            }
            Task { @MainActor in
                guard let url else {
                    store.alertMessage = "Deploy Link could not read the dropped file."
                    return
                }
                store.submit(url)
            }
        }
        return true
    }
}

struct DeviceCard: View {
    let device: DeviceInstallStatus
    let isBusy: Bool
    let start: () -> Void
    let stop: () -> Void
    let cancel: () -> Void
    let preparation: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .center, spacing: 12) {
                Image(systemName: device.deviceSymbol)
                    .font(.title2)
                    .foregroundStyle(device.bridgeColor)
                    .frame(width: 30)
                VStack(alignment: .leading, spacing: 2) {
                    Text(device.deviceLabel).font(.headline)
                    HStack(spacing: 6) {
                        Circle().fill(device.bridgeColor).frame(width: 8, height: 8)
                        Text(device.bridgeLabel).font(.caption).foregroundStyle(.secondary)
                    }
                }
                Spacer()
                if isBusy {
                    ProgressView().controlSize(.small)
                    Button("Cancel", action: cancel).help("Cancel this UI command; keep the device bridge running")
                }
                if device.serviceEnabled {
                    Button("Stop", role: .destructive, action: stop).disabled(isBusy)
                } else {
                    Button("Start", action: start).buttonStyle(.borderedProminent).disabled(isBusy)
                }
            }
            if let preparation { ProgressView(preparation).progressViewStyle(.linear) }
            if let job = device.job, job.shouldShowInDashboard {
                Divider()
                HStack {
                    VStack(alignment: .leading, spacing: 3) {
                        Text(job.artifact.bundleIdentifier).font(.subheadline.weight(.medium))
                        Text(job.versionLine).font(.caption).foregroundStyle(.secondary)
                    }
                    Spacer()
                    Text(job.stateLabel).font(.caption.weight(.semibold)).foregroundStyle(job.stateColor)
                }
                Text(job.displayStage).font(.subheadline)
                ProgressView(value: Double(job.progress.percent), total: 100)
                    .tint(job.isStalled ? .orange : job.stateColor)
                HStack {
                    Text("\(job.progress.percent)%")
                    Spacer()
                    Text(job.transferredLine)
                    Text("·")
                    Text(job.elapsedLine)
                }
                .font(.caption.monospacedDigit())
                .foregroundStyle(.secondary)
                if job.isStalled {
                    Label("No byte or device progress for five minutes", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption).foregroundStyle(.orange)
                } else if let error = job.errorLine {
                    Label(error, systemImage: "exclamationmark.triangle.fill")
                        .font(.caption).foregroundStyle(.red)
                }
            }
        }
        .padding(16)
        .background(.background, in: RoundedRectangle(cornerRadius: 14))
        .overlay(RoundedRectangle(cornerRadius: 14).stroke(Color.secondary.opacity(0.16)))
    }
}

@MainActor
final class LaunchAtLoginController: ObservableObject {
    @Published private(set) var isEnabled = false
    @Published private(set) var isChanging = false
    @Published private(set) var statusNote: String?

    private let fileManager = FileManager.default
    private let label = "one.zjz.deploy-link.app"

    init() { refresh() }

    func setEnabled(_ enabled: Bool) {
        guard !isChanging else { return }
        isChanging = true
        do {
            if enabled { try installLaunchAgent() }
            else { try removeLaunchAgent() }
            refresh()
        } catch {
            statusNote = error.localizedDescription
            refresh(preserveNote: true)
        }
        isChanging = false
    }

    private func refresh(preserveNote: Bool = false) {
        isEnabled = fileManager.fileExists(atPath: launchAgentURL.path)
        if !preserveNote {
            if !Bundle.main.bundleURL.path.hasPrefix("/Applications/") {
                statusNote = "Install Deploy Link in Applications before enabling this setting."
            } else {
                statusNote = nil
            }
        }
    }

    private var launchAgentURL: URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/LaunchAgents/\(label).plist")
    }

    private func installLaunchAgent() throws {
        guard let executable = Bundle.main.executableURL,
              Bundle.main.bundleURL.path.hasPrefix("/Applications/") else {
            throw DeployLinkAppError("Install Deploy Link in Applications before enabling Launch at Login.")
        }
        let directory = launchAgentURL.deletingLastPathComponent()
        try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
        let plist: [String: Any] = [
            "Label": label,
            "ProgramArguments": [executable.path],
            "RunAtLoad": true,
            "ProcessType": "Interactive"
        ]
        let data = try PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0)
        let temporary = directory.appendingPathComponent(".\(label).\(UUID().uuidString).plist")
        try data.write(to: temporary, options: .atomic)
        try fileManager.setAttributes([.posixPermissions: 0o600], ofItemAtPath: temporary.path)
        if fileManager.fileExists(atPath: launchAgentURL.path) {
            _ = try fileManager.replaceItemAt(launchAgentURL, withItemAt: temporary)
        } else {
            try fileManager.moveItem(at: temporary, to: launchAgentURL)
        }
    }

    private func removeLaunchAgent() throws {
        // Remove the persisted login item first. If this app was launched by
        // that job, bootout can terminate this process immediately.
        if fileManager.fileExists(atPath: launchAgentURL.path) {
            try fileManager.removeItem(at: launchAgentURL)
        }
        let target = "gui/\(getuid())/\(label)"
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/bin/launchctl")
        process.arguments = ["bootout", target]
        try? process.run()
        process.waitUntilExit()
    }
}

struct DeviceInstallStatus: Identifiable, Equatable {
    let id: String
    let profilePath: String
    let deviceLabel: String
    let serviceEnabled: Bool
    let bridgeState: String?
    let lastErrorCode: String?
    let job: InstallJobReceipt?

    var needsPairingRepair: Bool {
        lastErrorCode == "pair_verify_failed" || lastErrorCode == "pair_record_invalid"
    }

    var isConnected: Bool {
        serviceEnabled && bridgeState == "active"
    }

    var bridgeLabel: String {
        guard serviceEnabled else { return "Stopped" }
        switch bridgeState {
        case "active": return "Connected"
        case "connecting": return "Connecting"
        case "stopping": return "Stopping"
        case "failed": return needsPairingRepair ? "Pairing needs repair" : "Needs attention"
        case .some(let state): return state.replacingOccurrences(of: "_", with: " ").capitalized
        case .none: return "Starting"
        }
    }

    var bridgeColor: Color {
        guard serviceEnabled else { return .secondary }
        switch bridgeState {
        case "active": return .green
        case "failed": return .red
        default: return .orange
        }
    }

    var deviceSymbol: String {
        deviceLabel.localizedCaseInsensitiveContains("ipad") || deviceLabel.localizedCaseInsensitiveContains("mini") ? "ipad" : "iphone"
    }
}

struct DeployLinkProfile: Decodable {
    let deviceLabel: String
    enum CodingKeys: String, CodingKey { case deviceLabel = "device_label" }
}

struct InstallJobReceipt: Decodable, Equatable {
    private static let completedVisibilityDuration: TimeInterval = 60

    let id: String
    let state: String
    let artifact: Artifact
    let progress: Progress
    let errorCode: String?
    let submittedAt: String
    let startedAt: String?
    let finishedAt: String?
    let updatedAt: String

    enum CodingKeys: String, CodingKey {
        case id, state, artifact, progress
        case errorCode = "error_code"
        case submittedAt = "submitted_at"
        case startedAt = "started_at"
        case finishedAt = "finished_at"
        case updatedAt = "updated_at"
    }

    struct Artifact: Decodable, Equatable {
        let bundleIdentifier: String
        let version: String?
        let build: String?
        enum CodingKeys: String, CodingKey { case bundleIdentifier = "bundle_identifier"; case version, build }
    }

    struct Progress: Decodable, Equatable {
        let percent: Int
        let status: String?
        let phase: String?
        let bytesSent: UInt64?
        let bytesTotal: UInt64?
        enum CodingKeys: String, CodingKey {
            case percent, status, phase
            case bytesSent = "bytes_sent"
            case bytesTotal = "bytes_total"
        }
    }

    var isActive: Bool { state == "queued" || state == "running" }
    var isStalled: Bool { state == "running" && Date().timeIntervalSince(updatedDate) >= 300 }
    var shouldShowInDashboard: Bool {
        guard state == "installed" else { return true }
        guard let finishedDate = finishedAt.flatMap(parseDeployLinkDate) else { return false }
        return Date().timeIntervalSince(finishedDate) < Self.completedVisibilityDuration
    }
    var stateLabel: String {
        if isStalled { return "Stalled" }
        switch state {
        case "queued": return "Queued"
        case "running": return "Installing"
        case "installed": return "Installed"
        case "failed": return "Failed"
        case "interrupted": return "Interrupted"
        default: return "Unknown"
        }
    }
    var stateColor: Color {
        if isStalled { return .orange }
        switch state {
        case "installed": return .green
        case "failed", "interrupted": return .red
        default: return .accentColor
        }
    }
    var displayStage: String {
        if isStalled { return "No forward progress" }
        switch progress.phase {
        case "readback": return "Verifying installed app"
        case "payload_complete", "awaiting_device_progress": return "Waiting for device installation"
        case "device_installing": return deviceStatusLabel(progress.status)
        case "installed": return "Install complete"
        case "retrying": return "Retrying app transfer"
        case .some: return "Transferring app"
        case .none: return progress.status ?? stateLabel
        }
    }
    var versionLine: String { "\(artifact.version ?? "Unknown version") · build \(artifact.build ?? "unknown")" }
    var transferredLine: String {
        guard let sent = progress.bytesSent, let total = progress.bytesTotal, total > 0 else { return "—" }
        return String(format: "%.1f / %.1f MB", Double(sent) / 1_000_000, Double(total) / 1_000_000)
    }
    var elapsedLine: String {
        let start = startedAt.flatMap(parseDeployLinkDate) ?? parseDeployLinkDate(submittedAt) ?? Date()
        let end = finishedAt.flatMap(parseDeployLinkDate) ?? Date()
        return formatDuration(max(0, end.timeIntervalSince(start)))
    }
    var errorLine: String? {
        guard state == "failed" || state == "interrupted" else { return nil }
        return errorCode.map { "Deploy Link stopped with \($0)" } ?? "The install did not finish"
    }
    private var updatedDate: Date { parseDeployLinkDate(updatedAt) ?? Date.distantPast }
}

private struct BridgeSnapshot {
    let state: String
    let lastErrorCode: String?
}

final class DeployLinkStateReader: @unchecked Sendable {
    private let fileManager = FileManager.default
    private let decoder = JSONDecoder()
    private var serviceCache: [String: (Date, Bool)] = [:]
    private let receipts = ReceiptCache<InstallJobReceipt>(updated: { parseDeployLinkDate($0.updatedAt) ?? .distantPast }, active: { $0.isActive })

    func readStatuses(force: Bool = false) -> (devices: [DeviceInstallStatus], errors: [String]) {
        var errors: [String] = []
        let devices = profileURLs().compactMap { profileURL -> DeviceInstallStatus? in
            let profile: DeployLinkProfile
            do {
                profile = try decoder.decode(DeployLinkProfile.self, from: Data(contentsOf: profileURL))
                guard !profile.deviceLabel.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { throw DeployLinkAppError("device_label is empty") }
            } catch {
                errors.append("Unreadable profile: " + profileURL.path + ". Repair its JSON/device_label or remove it from the profiles folder.")
                return nil
            }
            let stem = profileURL.deletingPathExtension().lastPathComponent
            let jobsRoot = profileURL.deletingLastPathComponent().appendingPathComponent(".\(stem)-install-jobs", isDirectory: true)
            let snapshot = bridgeSnapshot(profileURL: profileURL)
            let enabled = snapshot != nil || launchAgentIsLoaded(profilePath: profileURL.path, force: force)
            return DeviceInstallStatus(id: profileURL.path, profilePath: profileURL.path, deviceLabel: profile.deviceLabel,
                serviceEnabled: enabled, bridgeState: snapshot?.state, lastErrorCode: snapshot?.lastErrorCode,
                job: receipts.latest(in: jobsRoot, force: force))
        }.sorted { left, right in
            if left.job?.isActive != right.job?.isActive { return left.job?.isActive == true }
            return left.deviceLabel.localizedCaseInsensitiveCompare(right.deviceLabel) == .orderedAscending
        }
        return (devices, errors)
    }

    private func profileURLs() -> [URL] {
        let home = FileManager.default.homeDirectoryForCurrentUser
        let roots = [home.appendingPathComponent(".local/share/deploy-link/profiles", isDirectory: true), home.appendingPathComponent("Library/Application Support/Deploy Link/profiles", isDirectory: true)]
        var seen: Set<String> = []
        return roots.flatMap { root in
            (try? fileManager.contentsOfDirectory(at: root, includingPropertiesForKeys: [.isRegularFileKey], options: [.skipsHiddenFiles])) ?? []
        }.filter { $0.pathExtension == "json" && seen.insert($0.path).inserted }
    }

    private func launchAgentIsLoaded(profilePath: String, force: Bool) -> Bool {
        if !force, let cached = serviceCache[profilePath], Date().timeIntervalSince(cached.0) < 30 { return cached.1 }
        let standardizedPath = URL(fileURLWithPath: profilePath).standardizedFileURL.path
        let digest = SHA256.hash(data: Data(standardizedPath.utf8))
        let profileID = digest.prefix(16).map { String(format: "%02x", $0) }.joined()
        let result = try? BoundedProcess.run(executable: URL(fileURLWithPath: "/bin/launchctl"), arguments: ["print", "gui/\(getuid())/one.zjz.deploy-link.\(profileID)"], timeout: 2)
        let loaded = result?.status == 0
        serviceCache[profilePath] = (Date(), loaded)
        return loaded
    }

    private func bridgeSnapshot(profileURL: URL) -> BridgeSnapshot? {
        let socket = profileURL.deletingPathExtension().appendingPathExtension("sock").path
        guard let data = LocalStatusSocket.read(path: socket),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              object["ok"] as? Bool == true, object["final"] as? Bool == true,
              object["control_schema"] as? Int == 2, let state = object["state"] as? String else { return nil }
        return BridgeSnapshot(state: state, lastErrorCode: object["last_error_code"] as? String)
    }
}

struct DeployLinkCommandRunner {
    static func binaryURL() -> URL? {
        let home = FileManager.default.homeDirectoryForCurrentUser
        return [
            home.appendingPathComponent(".local/share/deploy-link/bin/deploy-link"),
            home.appendingPathComponent("Library/Application Support/Deploy Link/bin/deploy-link")
        ].first { FileManager.default.isExecutableFile(atPath: $0.path) }
    }

    func run(_ arguments: [String]) async throws -> Data {
        guard let binary = Self.binaryURL() else { throw DeployLinkAppError("The Deploy Link command-line tool is not installed.") }
        return try await run(executable: binary, arguments: arguments)
    }

    func validateSignedIOSApp(at url: URL) async throws {
        var isDirectory: ObjCBool = false
        guard url.pathExtension.lowercased() == "app",
              FileManager.default.fileExists(atPath: url.path, isDirectory: &isDirectory),
              isDirectory.boolValue else {
            throw DeployLinkAppError("Drop a built iOS .app bundle, not an archive or source folder.")
        }
        let infoURL = url.appendingPathComponent("Info.plist")
        guard let info = NSDictionary(contentsOf: infoURL) as? [String: Any],
              let bundleID = info["CFBundleIdentifier"] as? String, !bundleID.isEmpty else {
            throw DeployLinkAppError("The app bundle does not contain a valid bundle identifier.")
        }
        let platforms = info["CFBundleSupportedPlatforms"] as? [String] ?? []
        guard platforms.contains("iPhoneOS") else {
            throw DeployLinkAppError("Deploy Link accepts device builds for iPhone or iPad, not simulator or macOS builds.")
        }
        _ = try await run(executable: URL(fileURLWithPath: "/usr/bin/codesign"), arguments: ["--verify", "--deep", "--strict", url.path])
    }

    private func run(executable: URL, arguments: [String]) async throws -> Data {
        let timeout: TimeInterval = arguments.starts(with: ["job", "submit"]) ? 600 : (arguments.starts(with: ["pair", "repair"]) ? 180 : 30)
        let result = try await BoundedProcess.runAsync(executable: executable, arguments: arguments, timeout: timeout)
        guard result.status == 0 else {
            throw DeployLinkAppError("\(executable.lastPathComponent) exited with status \(result.status). " + Self.safeErrorMessage(from: result.data))
        }
        guard !result.truncated else { throw DeployLinkAppError("Command output exceeded the diagnostic limit. Refresh to check its result.") }
        return result.data
    }

    private static func safeErrorMessage(from data: Data) -> String {
        if let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
           let message = object["error"] as? String, !message.isEmpty { return message }
        return "Deploy Link could not complete the command."
    }
}

struct DeployLinkAppError: LocalizedError {
    let message: String
    init(_ message: String) { self.message = message }
    var errorDescription: String? { message }
}

private func parseDeployLinkDate(_ value: String) -> Date? {
    let fractional = ISO8601DateFormatter()
    fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    return fractional.date(from: value) ?? ISO8601DateFormatter().date(from: value)
}

private func formatDuration(_ interval: TimeInterval) -> String {
    let seconds = Int(interval)
    let minutes = seconds / 60
    let hours = minutes / 60
    if hours > 0 { return "\(hours)h \(minutes % 60)m" }
    if minutes > 0 { return "\(minutes)m \(seconds % 60)s" }
    return "\(seconds)s"
}

private func deviceStatusLabel(_ status: String?) -> String {
    switch status {
    case "CreatingStagingDirectory": return "Preparing installation"
    case "VerifyingApplication": return "Verifying app"
    case "CreatingContainer": return "Creating app container"
    case "ExtractingPackage": return "Extracting app"
    case "InspectingPackage": return "Inspecting app"
    case "PreflightingApplication": return "Checking app requirements"
    case "InstallingApplication": return "Installing app"
    case "PostflightingApplication": return "Finishing installation"
    case "SandboxingApplication": return "Securing app container"
    case "GeneratingApplicationMap": return "Registering app"
    case "InstallComplete", "DataComplete": return "Installation complete"
    default: return "Installing on device"
    }
}
