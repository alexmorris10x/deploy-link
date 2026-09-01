import AppKit
import Foundation
import SwiftUI

@main
struct DeployLinkStatusApp: App {
    @StateObject private var store = DeployLinkStatusStore()

    var body: some Scene {
        MenuBarExtra {
            DeployLinkStatusPanel()
                .environmentObject(store)
        } label: {
            HStack(spacing: 4) {
                Image(systemName: store.menuBarSymbol)
                if let percent = store.activePercent {
                    Text("\(percent)%")
                        .monospacedDigit()
                }
            }
            .accessibilityLabel(store.menuBarAccessibilityLabel)
        }
        .menuBarExtraStyle(.window)
    }
}

@MainActor
final class DeployLinkStatusStore: ObservableObject {
    @Published private(set) var devices: [DeviceInstallStatus] = []

    private var refreshTimer: Timer?
    private let reader = DeployLinkReceiptReader()

    init() {
        refresh()
        refreshTimer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            Task { @MainActor in
                self?.refresh()
            }
        }
    }

    var activePercent: Int? {
        devices.first(where: { $0.job?.isActive == true })?.job?.progress.percent
    }

    var menuBarSymbol: String {
        if devices.contains(where: { $0.job?.isStalled == true || $0.job?.state == "failed" || $0.job?.state == "interrupted" }) {
            return "exclamationmark.arrow.trianglehead.2.clockwise.rotate.90"
        }
        if devices.contains(where: { $0.job?.isActive == true }) {
            return "arrow.up.circle.fill"
        }
        if devices.contains(where: { $0.job?.state == "installed" }) {
            return "checkmark.circle.fill"
        }
        return "link.circle"
    }

    var menuBarAccessibilityLabel: String {
        if let active = devices.first(where: { $0.job?.isActive == true }), let job = active.job {
            return "Deploy Link, \(active.deviceLabel), \(job.displayStage), \(job.progress.percent) percent"
        }
        return "Deploy Link status"
    }

    private func refresh() {
        devices = reader.readStatuses()
    }
}

struct DeployLinkStatusPanel: View {
    @EnvironmentObject private var store: DeployLinkStatusStore

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Label("Deploy Link", systemImage: "link.circle.fill")
                    .font(.headline)
                Spacer()
                Text("Live")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
            }

            if store.devices.isEmpty {
                VStack(spacing: 8) {
                    Image(systemName: "iphone.slash")
                        .font(.title)
                        .foregroundStyle(.secondary)
                    Text("No device profiles")
                        .font(.headline)
                    Text("Deploy Link has no saved device profiles.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, minHeight: 150)
            } else {
                ForEach(store.devices) { device in
                    DeviceInstallCard(device: device)
                    if device.id != store.devices.last?.id {
                        Divider()
                    }
                }
            }

            Divider()
            HStack {
                Text("Receipts update automatically")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
                Button("Quit") {
                    NSApplication.shared.terminate(nil)
                }
                .buttonStyle(.plain)
                .foregroundStyle(.secondary)
            }
        }
        .padding(16)
        .frame(width: 360)
    }
}

struct DeviceInstallCard: View {
    let device: DeviceInstallStatus

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                Text(device.deviceLabel)
                    .font(.subheadline.weight(.semibold))
                Spacer()
                Text(device.job?.stateLabel ?? "Ready")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(device.job?.stateColor ?? .secondary)
            }

            if let job = device.job {
                VStack(alignment: .leading, spacing: 4) {
                    Text(job.artifact.bundleIdentifier)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    Text(job.versionLine)
                        .font(.caption2)
                        .foregroundStyle(.tertiary)
                }

                Text(job.displayStage)
                    .font(.subheadline)

                ProgressView(value: Double(job.progress.percent), total: 100)
                    .tint(job.isStalled ? .orange : job.stateColor)
                    .accessibilityLabel("Installation progress")
                    .accessibilityValue("\(job.progress.percent) percent")

                HStack {
                    Text("\(job.progress.percent)%")
                    Spacer()
                    Text(job.transferredLine)
                }
                .font(.caption.monospacedDigit())
                .foregroundStyle(.secondary)

                HStack {
                    Label(job.elapsedLine, systemImage: "clock")
                    Spacer()
                    Text("Last progress \(job.lastProgressLine)")
                }
                .font(.caption)
                .foregroundStyle(.secondary)

                if job.isStalled {
                    Label("No byte or device progress for five minutes", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption)
                        .foregroundStyle(.orange)
                } else if let error = job.errorLine {
                    Label(error, systemImage: "exclamationmark.triangle.fill")
                        .font(.caption)
                        .foregroundStyle(.red)
                }
            } else {
                Text("No install receipt")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }
}

struct DeviceInstallStatus: Identifiable, Equatable {
    let id: String
    let deviceLabel: String
    let job: InstallJobReceipt?
}

struct DeployLinkProfile: Decodable {
    let deviceLabel: String

    enum CodingKeys: String, CodingKey {
        case deviceLabel = "device_label"
    }
}

struct InstallJobReceipt: Decodable, Equatable {
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

        enum CodingKeys: String, CodingKey {
            case bundleIdentifier = "bundle_identifier"
            case version, build
        }
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

    var isStalled: Bool {
        state == "running" && Date().timeIntervalSince(updatedDate) >= 300
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
        case .some: return "Transferring app"
        case .none: return progress.status ?? stateLabel
        }
    }

    var versionLine: String {
        let version = artifact.version ?? "Unknown version"
        let build = artifact.build ?? "unknown"
        return "\(version) · build \(build)"
    }

    var transferredLine: String {
        guard let sent = progress.bytesSent, let total = progress.bytesTotal, total > 0 else { return "—" }
        return String(format: "%.1f / %.1f MB", Double(sent) / 1_000_000, Double(total) / 1_000_000)
    }

    var elapsedLine: String {
        let start = startedAt.flatMap(parseDeployLinkDate) ?? parseDeployLinkDate(submittedAt) ?? Date()
        let end = finishedAt.flatMap(parseDeployLinkDate) ?? Date()
        return formatDuration(max(0, end.timeIntervalSince(start)))
    }

    var lastProgressLine: String {
        formatDuration(max(0, Date().timeIntervalSince(updatedDate))) + " ago"
    }

    var errorLine: String? {
        guard state == "failed" || state == "interrupted" else { return nil }
        if let errorCode, !errorCode.isEmpty {
            return "Deploy Link stopped with \(errorCode)"
        }
        return "The install did not finish"
    }

    private var updatedDate: Date {
        parseDeployLinkDate(updatedAt) ?? Date.distantPast
    }
}

struct DeployLinkReceiptReader {
    private let fileManager = FileManager.default
    private let decoder = JSONDecoder()

    func readStatuses() -> [DeviceInstallStatus] {
        let profilesDirectory = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".local/share/deploy-link/profiles", isDirectory: true)
        guard let profileURLs = try? fileManager.contentsOfDirectory(
            at: profilesDirectory,
            includingPropertiesForKeys: nil,
            options: [.skipsHiddenFiles]
        ) else {
            return []
        }

        return profileURLs
            .filter { $0.pathExtension == "json" }
            .compactMap { profileURL in
                guard let data = try? Data(contentsOf: profileURL),
                      let profile = try? decoder.decode(DeployLinkProfile.self, from: data) else {
                    return nil
                }
                let stem = profileURL.deletingPathExtension().lastPathComponent
                let jobsRoot = profilesDirectory.appendingPathComponent(".\(stem)-install-jobs", isDirectory: true)
                return DeviceInstallStatus(
                    id: stem,
                    deviceLabel: profile.deviceLabel,
                    job: latestReceipt(in: jobsRoot)
                )
            }
            .sorted { left, right in
                if left.job?.isActive != right.job?.isActive {
                    return left.job?.isActive == true
                }
                return left.deviceLabel.localizedCaseInsensitiveCompare(right.deviceLabel) == .orderedAscending
            }
    }

    private func latestReceipt(in jobsRoot: URL) -> InstallJobReceipt? {
        guard let jobDirectories = try? fileManager.contentsOfDirectory(
            at: jobsRoot,
            includingPropertiesForKeys: nil,
            options: [.skipsHiddenFiles]
        ) else {
            return nil
        }
        return jobDirectories.compactMap { directory in
            let receiptURL = directory.appendingPathComponent("receipt.json")
            guard let data = try? Data(contentsOf: receiptURL) else { return nil }
            return try? decoder.decode(InstallJobReceipt.self, from: data)
        }
        .max { left, right in
            (parseDeployLinkDate(left.updatedAt) ?? .distantPast) < (parseDeployLinkDate(right.updatedAt) ?? .distantPast)
        }
    }
}

private func parseDeployLinkDate(_ value: String) -> Date? {
    let fractional = ISO8601DateFormatter()
    fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    if let date = fractional.date(from: value) { return date }
    return ISO8601DateFormatter().date(from: value)
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
