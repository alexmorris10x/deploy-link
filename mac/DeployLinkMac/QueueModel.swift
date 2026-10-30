import AppKit
import Foundation
import Observation
import ServiceManagement
import UniformTypeIdentifiers

struct QueueRelease: Decodable, Identifiable {
    let id: String
    let title: String
    let version: String
    let build: String
    let note: String
}

struct QueueSnapshot: Decodable {
    var configured = false
    var running = false
    var queueURL = ""
    var queueAtLogin = false
    var releases: [QueueRelease] = []
    var xcode = false
    var tailscale = false
    var connected = false
}

private struct CommandFailure: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

private enum QueueCommand {
    static func run(_ arguments: [String]) throws -> QueueSnapshot {
        let paths = ["/opt/homebrew/bin/python3", "/usr/local/bin/python3", "/usr/bin/python3"]
        guard let python = paths.first(where: { FileManager.default.isExecutableFile(atPath: $0) }) else {
            throw CommandFailure(message: "Install Python 3.9 or later from python.org, then reopen Deploy Link.")
        }
        guard let helper = Bundle.main.url(forResource: "mac_control", withExtension: "py") else {
            throw CommandFailure(message: "The Mac app is missing its server helper. Download a fresh copy of Deploy Link.")
        }
        let process = Process()
        process.executableURL = URL(fileURLWithPath: python)
        process.arguments = [helper.path] + arguments
        var environment = ProcessInfo.processInfo.environment
        environment["PATH"] = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
        environment["PYTHONDONTWRITEBYTECODE"] = "1"
        process.environment = environment
        let output = Pipe()
        process.standardOutput = output
        process.standardError = FileHandle.nullDevice
        try process.run()
        let data = output.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        if let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
           let error = object["error"] as? String {
            throw CommandFailure(message: error)
        }
        guard process.terminationStatus == 0 else {
            throw CommandFailure(message: "The server helper could not run. Check that Python 3.9 or later and full Xcode are installed.")
        }
        return try JSONDecoder().decode(QueueSnapshot.self, from: data)
    }
}

@MainActor @Observable
final class QueueModel {
    var snapshot = QueueSnapshot()
    var busy = false
    var loading = true
    var error: String?
    var notice: String?
    var selectedBuild: URL?
    var releaseNote = ""
    var loginStatus = SMAppService.mainApp.status
    private var refreshing = false

    var statusText: String {
        if loading { return "Checking your Mac…" }
        if snapshot.running { return "Server running" }
        return snapshot.configured ? "Server stopped" : "Set up your Mac"
    }

    init() {
        Task { [weak self] in
            await self?.refresh()
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(20))
                await self?.refresh()
            }
        }
    }

    func refresh() async {
        guard !busy, !refreshing else { return }
        refreshing = true
        defer { refreshing = false; loading = false }
        do {
            snapshot = try await Task.detached(priority: .utility) { try QueueCommand.run(["status"]) }.value
            loginStatus = SMAppService.mainApp.status
        } catch {
            self.error = error.localizedDescription
        }
    }

    func perform(_ arguments: [String], success: String? = nil) {
        guard !busy else { return }
        busy = true
        error = nil
        notice = nil
        Task {
            defer { busy = false; loading = false }
            do {
                snapshot = try await Task.detached(priority: .userInitiated) { try QueueCommand.run(arguments) }.value
                notice = success
                // launchd may return before the server starts answering.
                if arguments.first == "start" || arguments.first == "setup" {
                    try? await Task.sleep(for: .seconds(1))
                    snapshot = try await Task.detached(priority: .utility) { try QueueCommand.run(["status"]) }.value
                }
            } catch {
                self.error = error.localizedDescription
            }
        }
    }

    func chooseBuild() {
        let panel = NSOpenPanel()
        panel.title = "Add a signed iOS build"
        panel.prompt = "Choose Build"
        panel.allowedContentTypes = [.applicationBundle, UTType(filenameExtension: "ipa") ?? .data]
        panel.canChooseDirectories = false
        panel.canChooseFiles = true
        panel.treatsFilePackagesAsDirectories = false
        panel.allowsMultipleSelection = false
        if panel.runModal() == .OK { selectedBuild = panel.url; releaseNote = "" }
    }

    func publish() {
        guard let selectedBuild else { return }
        perform(["publish", "--source", selectedBuild.path, "--note", releaseNote], success: "Build added to your queue.")
        self.selectedBuild = nil
    }

    func openQueue() {
        guard let url = URL(string: snapshot.queueURL), url.scheme == "https" else { return }
        NSWorkspace.shared.open(url)
    }

    func copyAddress() {
        guard !snapshot.queueURL.isEmpty else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(snapshot.queueURL, forType: .string)
        notice = "Queue address copied. Paste it into Deploy Link on your iPhone or iPad."
    }

    func setLaunchAtLogin(_ enabled: Bool) {
        do {
            if enabled {
                try SMAppService.mainApp.register()
            } else {
                try SMAppService.mainApp.unregister()
            }
            loginStatus = SMAppService.mainApp.status
            if loginStatus == .requiresApproval { SMAppService.openSystemSettingsLoginItems() }
        } catch { self.error = error.localizedDescription }
    }
}
